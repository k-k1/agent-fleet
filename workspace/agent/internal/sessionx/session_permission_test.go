package sessionx

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/notice"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// stubPane makes readPermissionMenu parse pane instead of capturing tmux.
func stubPane(t *testing.T, pane string) {
	t.Helper()
	prev := capturePermissionPane
	capturePermissionPane = func(string) string { return pane }
	t.Cleanup(func() { capturePermissionPane = prev })
}

func permFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "agents", "claude", "testdata", "permmenu", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestPermissionKeysByLabel pins the keystroke per choice on each measured menu (#3354). The
// two-row menu is the regression: position-fixed keys answered "always" with No and "deny"
// with Yes there.
func TestPermissionKeysByLabel(t *testing.T) {
	type want struct {
		keys []string
		code string
	}
	cases := []struct {
		file string
		want map[string]want
	}{
		{"bash_ask_rule_two_options.txt", map[string]want{
			claude.PermAllow:  {[]string{"1"}, ""},
			claude.PermAlways: {nil, "choice_unavailable"},
			claude.PermDeny:   {[]string{"2"}, ""},
		}},
		{"bash_four_options.txt", map[string]want{
			claude.PermAllow:  {[]string{"1"}, ""},
			claude.PermAlways: {[]string{"2"}, ""},
			claude.PermDeny:   {[]string{"4"}, ""},
		}},
		{"write_three_options.txt", map[string]want{
			claude.PermAllow:  {[]string{"1"}, ""},
			claude.PermAlways: {[]string{"2"}, ""},
			claude.PermDeny:   {[]string{"3"}, ""},
		}},
	}
	for _, c := range cases {
		stubPane(t, permFixture(t, c.file))
		for choice, w := range c.want {
			keys, code := permissionKeys("", "s", choice, "", "", "")
			if !reflect.DeepEqual(keys, w.keys) || code != w.code {
				t.Errorf("%s %s: keys=%v code=%q, want %v %q", c.file, choice, keys, code, w.keys, w.code)
			}
		}
	}
}

// With no readable dialog nothing is sent, deny included: the payload can outlive the dialog,
// and an Escape into a pane that went back to work interrupts the turn (#3357 review).
func TestPermissionKeysUnreadableMenu(t *testing.T) {
	stubPane(t, "❯ \n  ⏸ manual mode on\n")
	for _, c := range []string{claude.PermAllow, claude.PermAlways, claude.PermDeny} {
		if keys, code := permissionKeys("", "s", c, "", "", ""); keys != nil || code != "menu_unreadable" {
			t.Errorf("%s: %v %q", c, keys, code)
		}
	}
	if got := permissionChoices("s"); got != nil {
		t.Errorf("choices = %v, want nil", got)
	}
}

// TestPermissionRespondGuards pins the preconditions that fail before any key is sent.
func TestPermissionRespondGuards(t *testing.T) {
	withTempHome(t)
	do := func(name, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/sessions/"+name+"/permission-respond", strings.NewReader(body))
		req.SetPathValue("name", name)
		rec := httptest.NewRecorder()
		HandleSessionPermissionRespond(rec, req)
		return rec
	}
	if rec := do("slot71", `{"choice":"allow","expect":"x"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown session: %d %s", rec.Code, rec.Body.String())
	}
	session.WriteMeta(session.Meta{Name: "slot72", Dir: t.TempDir(), Kind: session.KindCodex})
	if rec := do("slot72", `{"choice":"allow","expect":"x"}`); rec.Code != http.StatusConflict {
		t.Fatalf("a kind without a pending permission payload: %d %s", rec.Code, rec.Body.String())
	}
	meta73 := session.Meta{Name: "slot73", Dir: t.TempDir(), Kind: session.KindClaude}
	session.WriteMeta(meta73)
	if rec := do("slot73", `{"choice":"maybe","expect":"x"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad choice: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do("slot73", `{"choice":"allow"}`); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "bad_expect") {
		t.Fatalf("missing expect: %d %s", rec.Code, rec.Body.String())
	}
	// The permission state alone, without the hook's payload, is not a dialog to answer.
	status.Persist(session.UUID(meta73.Dir, "slot73"), "permission")
	if rec := do("slot73", `{"choice":"allow","expect":"x"}`); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "no_permission") {
		t.Fatalf("no pending permission: %d %s", rec.Code, rec.Body.String())
	}
	// A plan approval raises "permission" too, but its dialog is not a tool-permission menu:
	// a digit there would answer the plan.
	dir := t.TempDir()
	session.WriteMeta(session.Meta{Name: "slot74", Dir: dir, Kind: session.KindClaude})
	sid := session.UUID(dir, "slot74")
	status.Persist(sid, "permission")
	status.WritePendingPermission(sid, "ExitPlanMode")
	status.WritePendingPlan(sid, "# plan")
	if rec := do("slot74", `{"choice":"deny","expect":"x"}`); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "no_permission") {
		t.Fatalf("plan under permission: %d %s", rec.Code, rec.Body.String())
	}
}

// An answer carries the prompt it was drawn for. Answering prompt P after the terminal moved on
// to prompt Q must not answer Q, and the same prompt cannot be answered twice (#3357 review).
func TestPermissionRespondIsBoundToThePrompt(t *testing.T) {
	withTempHome(t)
	dir := t.TempDir()
	session.WriteMeta(session.Meta{Name: "slot75", Dir: dir, Kind: session.KindClaude})
	sid := session.UUID(dir, "slot75")
	status.Persist(sid, "permission")
	status.WritePendingPermission(sid, "Bash · echo live-check-3357")
	p := permissionToken(sid)
	if p == "" {
		t.Fatal("no token for a pending prompt")
	}
	// The next prompt replaces the payload (a later mtime).
	time.Sleep(10 * time.Millisecond)
	status.WritePendingPermission(sid, "Bash · rm -rf build")
	if q := permissionToken(sid); q == p {
		t.Fatalf("token did not change across prompts: %s", q)
	}
	req := httptest.NewRequest(http.MethodPost, "/sessions/slot75/permission-respond", strings.NewReader(`{"choice":"allow","expect":"`+p+`"}`))
	req.SetPathValue("name", "slot75")
	rec := httptest.NewRecorder()
	HandleSessionPermissionRespond(rec, req)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "permission_changed") {
		t.Fatalf("stale answer: %d %s", rec.Code, rec.Body.String())
	}
	// A prompt already answered is refused before the pane is touched.
	permDecided.Store("slot75", permissionToken(sid))
	t.Cleanup(func() { permDecided.Delete("slot75") })
	req = httptest.NewRequest(http.MethodPost, "/sessions/slot75/permission-respond", strings.NewReader(`{"choice":"deny","expect":"`+permissionToken(sid)+`"}`))
	req.SetPathValue("name", "slot75")
	rec = httptest.NewRecorder()
	HandleSessionPermissionRespond(rec, req)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "no_permission") {
		t.Fatalf("second answer: %d %s", rec.Code, rec.Body.String())
	}
}

// changePromptDuringCapture stubs the pane capture so that, while it runs, the hook replaces
// prompt P with prompt Q (the race the token re-checks guard, #3357 review round 2).
func changePromptDuringCapture(t *testing.T, sid, pane string) {
	t.Helper()
	prev := readPermissionMenu
	readPermissionMenu = func(string) (claude.PermissionMenu, bool) {
		time.Sleep(10 * time.Millisecond)
		status.WritePendingPermission(sid, "Bash · rm -rf build")
		return claude.ParsePermissionMenu(pane)
	}
	t.Cleanup(func() { readPermissionMenu = prev })
}

func permissionSession(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	session.WriteMeta(session.Meta{Name: name, Dir: dir, Kind: session.KindClaude})
	sid := session.UUID(dir, name)
	status.Persist(sid, "permission")
	status.WritePendingPermissionCommand(sid, "Bash · echo live-check-3357", "echo live-check-3357")
	t.Cleanup(func() { permDecided.Delete(name) })
	return sid
}

func postPermission(name, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/sessions/"+name+"/permission-respond", strings.NewReader(body))
	req.SetPathValue("name", name)
	rec := httptest.NewRecorder()
	HandleSessionPermissionRespond(rec, req)
	return rec
}

// The expect is checked again after the capture: a prompt replaced meanwhile gets no key.
func TestPermissionRespondRechecksAfterCapture(t *testing.T) {
	logPath := fakeTmux(t)
	withTempHome(t)
	pane := permFixture(t, "bash_ask_rule_af_label.txt")

	// Control: an unchanged prompt is answered with the row's digit.
	sid := permissionSession(t, "slot76")
	stubPane(t, pane)
	if rec := postPermission("slot76", `{"choice":"allow","expect":"`+permissionToken(sid)+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("control: %d %s", rec.Code, rec.Body.String())
	}
	if b, _ := os.ReadFile(logPath); !strings.Contains(string(b), "send-keys -t %7 1") {
		t.Fatalf("control sent no digit:\n%s", b)
	}
	_ = os.Remove(logPath)

	sid = permissionSession(t, "slot77")
	p := permissionToken(sid)
	changePromptDuringCapture(t, sid, pane)
	rec := postPermission("slot77", `{"choice":"allow","expect":"`+p+`"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "permission_changed") {
		t.Fatalf("changed during capture: %d %s", rec.Code, rec.Body.String())
	}
	if b, _ := os.ReadFile(logPath); strings.Contains(string(b), "send-keys") {
		t.Fatalf("a key went out for the replaced prompt:\n%s", b)
	}
}

// The poll never pairs one prompt's text with another's id.
func TestSurfacePermissionPromptIsOneSnapshot(t *testing.T) {
	withTempHome(t)
	pane := permFixture(t, "bash_four_options.txt")

	sid := permissionSession(t, "slot78")
	stubPane(t, pane)
	before := permissionToken(sid)
	pending := map[string]any{"pendingPermission": "Bash · ls"}
	surfacePermissionPrompt(pending, sid, "slot78", before)
	if pending["pendingPermissionId"] != before || pending["pendingPermissionChoices"] == nil {
		t.Fatalf("steady prompt: %v", pending)
	}

	sid = permissionSession(t, "slot79")
	before = permissionToken(sid)
	changePromptDuringCapture(t, sid, pane)
	pending = map[string]any{"pendingPermission": "Bash · ls"}
	surfacePermissionPrompt(pending, sid, "slot79", before)
	if len(pending) != 0 {
		t.Fatalf("changed prompt must be dropped whole, got %v", pending)
	}
}

// A deny interrupts claude's turn and no hook follows it, so the answer itself retires the
// prompt: otherwise the card stays up with live buttons after the dialog closed.
func TestPermissionDenyRetiresThePrompt(t *testing.T) {
	logPath := fakeTmux(t)
	withTempHome(t)
	stubPane(t, permFixture(t, "bash_ask_rule_af_label.txt"))
	sid := permissionSession(t, "slot80")
	if rec := postPermission("slot80", `{"choice":"deny","expect":"`+permissionToken(sid)+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("deny: %d %s", rec.Code, rec.Body.String())
	}
	if b, _ := os.ReadFile(logPath); !strings.Contains(string(b), "send-keys -t %7 2") {
		t.Fatalf("deny sent no digit:\n%s", b)
	}
	if _, ok := status.ReadPendingPermission(sid); ok {
		t.Fatal("payload survived the deny")
	}
	if st, _ := status.Read(sid); st.State != "idle" {
		t.Fatalf("state = %q, want idle", st.State)
	}

	// A newer prompt that arrived meanwhile is not the one denied: it stays.
	sid = permissionSession(t, "slot81")
	p := permissionToken(sid)
	st, _ := status.Read(sid)
	time.Sleep(10 * time.Millisecond)
	status.WritePendingPermission(sid, "Bash · rm -rf build")
	settleDeniedPermission(sid, st.Rev, p)
	if _, ok := status.ReadPendingPermission(sid); !ok {
		t.Fatal("a newer prompt was retired")
	}
	if st, _ := status.Read(sid); st.State != "permission" {
		t.Fatalf("state = %q, want permission", st.State)
	}
}

// A bridge deny that finds the prompt already gone sends nothing, and so records nothing: the
// payload, the state and permDecided stay as the hook left them (#3376 review).
func TestBridgeDenyThatSendsNothingSettlesNothing(t *testing.T) {
	logPath := fakeTmux(t)
	withTempHome(t)
	sid := permissionSession(t, "slot82")
	pane := permFixture(t, "bash_ask_rule_af_label.txt")
	prev := readPermissionMenu
	readPermissionMenu = func(string) (claude.PermissionMenu, bool) {
		status.Persist(sid, "working") // the hook moved on; the payload is still the old one
		return claude.ParsePermissionMenu(pane)
	}
	t.Cleanup(func() { readPermissionMenu = prev })
	meta, _ := session.ReadMeta("slot82")
	if _, err := answerClaudePermission(meta, claude.PermDeny, permissionToken(sid), true); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(logPath); strings.Contains(string(b), "send-keys") {
		t.Fatalf("keys sent:\n%s", b)
	}
	if _, ok := permDecided.Load("slot82"); ok {
		t.Fatal("recorded as answered")
	}
	if _, ok := status.ReadPendingPermission(sid); !ok {
		t.Fatal("payload removed")
	}
	if st, _ := status.Read(sid); st.State != "working" {
		t.Fatalf("state = %q, want working", st.State)
	}
}

// A bridge deny that is sent retires the prompt like the card's.
func TestBridgeDenyRetiresThePrompt(t *testing.T) {
	fakeTmux(t)
	withTempHome(t)
	stubPane(t, permFixture(t, "bash_ask_rule_af_label.txt"))
	sid := permissionSession(t, "slot83")
	meta, _ := session.ReadMeta("slot83")
	if _, err := answerClaudePermission(meta, claude.PermDeny, permissionToken(sid), true); err != nil {
		t.Fatal(err)
	}
	if _, ok := status.ReadPendingPermission(sid); ok {
		t.Fatal("payload survived the deny")
	}
	if st, _ := status.Read(sid); st.State != "idle" {
		t.Fatalf("state = %q, want idle", st.State)
	}
}

// A bridge button names the prompt it was posted for (#3358): a click carrying an older prompt's
// token, or no token at all, sends nothing and leaves the current prompt open.
func TestBridgePermissionButtonIsBoundToThePrompt(t *testing.T) {
	logPath := fakeTmux(t)
	withTempHome(t)
	stubPane(t, permFixture(t, "bash_ask_rule_af_label.txt"))
	sid := permissionSession(t, "slot84")
	meta, _ := session.ReadMeta("slot84")
	cur := permissionToken(sid)
	if cur == "" {
		t.Fatal("no token for a pending prompt")
	}
	for name, tok := range map[string]string{"stale": cur + "x", "missing": ""} {
		out, err := answerClaudePermission(meta, claude.PermAllow, tok, true)
		if err != nil || !strings.Contains(out, "prompt changed") {
			t.Fatalf("%s: out=%q err=%v, want a refusal", name, out, err)
		}
		if b, _ := os.ReadFile(logPath); strings.Contains(string(b), "send-keys") {
			t.Fatalf("%s: keys sent:\n%s", name, b)
		}
		if _, ok := permDecided.Load("slot84"); ok {
			t.Fatalf("%s: recorded as answered", name)
		}
	}
	if _, err := answerClaudePermission(meta, claude.PermAllow, cur, true); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(logPath); !strings.Contains(string(b), "send-keys") {
		t.Fatal("the matching token sent nothing")
	}
}

// The permission-request event names the prompt it was raised for, so the chat buttons can bind to it.
func TestPermissionRequestEventCarriesTheToken(t *testing.T) {
	withTempHome(t)
	sid := permissionSession(t, "slot85")
	RecordSessionNotification(sid, "working", "permission", "")
	events := notice.List()
	if len(events) != 1 || events[0].Kind != "permission-request" {
		t.Fatalf("events = %+v", events)
	}
	if got, _ := events[0].Payload["permissionToken"].(string); got == "" || got != permissionToken(sid) {
		t.Fatalf("payload token = %q, want %q", got, permissionToken(sid))
	}
}

// The pane can already show prompt Q while the payload (and so the token) still names P, for the
// hook's latency (#3359). An answer that passes both token checks is refused when the dialog's
// target is not the payload's, from the card and from a chat button alike.
func TestPermissionAnswerIsBoundToTheDialogOnScreen(t *testing.T) {
	logPath := fakeTmux(t)
	withTempHome(t)
	// The pane shows `echo live-check-3357`; the payload still names the previous prompt.
	stubPane(t, permFixture(t, "bash_ask_rule_af_label.txt"))
	sid := permissionSession(t, "slot86")
	status.WritePendingPermission(sid, "Bash · rm -rf build")
	tok := permissionToken(sid)
	meta, _ := session.ReadMeta("slot86")

	rec := postPermission("slot86", `{"choice":"allow","expect":"`+tok+`"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "target_mismatch") {
		t.Fatalf("card: %d %s", rec.Code, rec.Body.String())
	}
	for _, c := range []string{claude.PermAllow, claude.PermDeny} {
		out, err := answerClaudePermission(meta, c, tok, true)
		if err != nil || !strings.Contains(out, "not the one this button") {
			t.Fatalf("button %s: out=%q err=%v", c, out, err)
		}
	}
	if b, _ := os.ReadFile(logPath); strings.Contains(string(b), "send-keys") {
		t.Fatalf("keys sent for a dialog the card was not drawn for:\n%s", b)
	}
	if _, ok := permDecided.Load("slot86"); ok {
		t.Fatal("recorded as answered")
	}
	// A payload that names no target is not compared: the token is all there is to bind to.
	status.WritePendingPermission(sid, "Claude needs your permission")
	if rec := postPermission("slot86", `{"choice":"allow","expect":"`+permissionToken(sid)+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("untargeted payload: %d %s", rec.Code, rec.Body.String())
	}
}

// replaceCommandLine swaps the dialog's command line, which is the LAST occurrence: the first is
// the transcript line above the dialog.
func replaceCommandLine(pane, old, repl string) string {
	i := strings.LastIndex(pane, old)
	if i < 0 {
		panic("command line not in fixture")
	}
	return pane[:i] + repl + pane[i+len(old):]
}

// The same hard-newline case through the card and the bridge button (review round 2).
func TestPermissionAnswerRefusesJoinedHardNewline(t *testing.T) {
	logPath := fakeTmux(t)
	withTempHome(t)
	a := strings.Repeat("a", 40)
	pane := replaceCommandLine(permFixture(t, "bash_ask_rule_af_label.txt"), " echo live-check-3357\n", " echo "+a+"\n rm -rf build\n")
	stubPane(t, pane)
	sid := permissionSession(t, "slot87")
	status.WritePendingPermissionCommand(sid, "Bash · echo "+a+" rm -rf build", "echo "+a+" rm -rf build")
	tok := permissionToken(sid)
	meta, _ := session.ReadMeta("slot87")
	if rec := postPermission("slot87", `{"choice":"allow","expect":"`+tok+`"}`); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "target_mismatch") {
		t.Fatalf("card: %d %s", rec.Code, rec.Body.String())
	}
	if out, err := answerClaudePermission(meta, claude.PermAllow, tok, true); err != nil || !strings.Contains(out, "not the one this button") {
		t.Fatalf("button: %q %v", out, err)
	}
	if b, _ := os.ReadFile(logPath); strings.Contains(string(b), "send-keys") {
		t.Fatalf("keys sent:\n%s", b)
	}
}

// Round 3 through the card and the bridge button: a wrapped Q whose first physical line equals P.
func TestPermissionAnswerRefusesWrappedSuffix(t *testing.T) {
	logPath := fakeTmux(t)
	withTempHome(t)
	first := "echo " + strings.Repeat("a", 73)
	pane := replaceCommandLine(permFixture(t, "bash_ask_rule_af_label.txt"), " echo live-check-3357\n", " "+first+"\n && rm -rf build\n")
	stubPane(t, pane)
	sid := permissionSession(t, "slot88")
	status.WritePendingPermissionCommand(sid, "Bash · "+first, first)
	tok := permissionToken(sid)
	meta, _ := session.ReadMeta("slot88")
	if rec := postPermission("slot88", `{"choice":"allow","expect":"`+tok+`"}`); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "target_mismatch") {
		t.Fatalf("card: %d %s", rec.Code, rec.Body.String())
	}
	if out, err := answerClaudePermission(meta, claude.PermAllow, tok, true); err != nil || !strings.Contains(out, "not the one this button") {
		t.Fatalf("button: %q %v", out, err)
	}
	if b, _ := os.ReadFile(logPath); strings.Contains(string(b), "send-keys") {
		t.Fatalf("keys sent:\n%s", b)
	}
}

// Round 4 through the card and the bridge button: an unknown-width first line with a continuation.
func TestPermissionAnswerRefusesUnknownWidthContinuation(t *testing.T) {
	logPath := fakeTmux(t)
	withTempHome(t)
	first := "echo " + strings.Repeat("a", 73) + "\u0301"
	pane := replaceCommandLine(permFixture(t, "bash_ask_rule_af_label.txt"), " echo live-check-3357\n", " "+first+"\n && rm -rf build\n")
	stubPane(t, pane)
	sid := permissionSession(t, "slot89")
	status.WritePendingPermissionCommand(sid, "Bash · "+first, first)
	tok := permissionToken(sid)
	meta, _ := session.ReadMeta("slot89")
	if rec := postPermission("slot89", `{"choice":"allow","expect":"`+tok+`"}`); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "target_mismatch") {
		t.Fatalf("card: %d %s", rec.Code, rec.Body.String())
	}
	if out, err := answerClaudePermission(meta, claude.PermAllow, tok, true); err != nil || !strings.Contains(out, "not the one this button") {
		t.Fatalf("button: %q %v", out, err)
	}
	if b, _ := os.ReadFile(logPath); strings.Contains(string(b), "send-keys") {
		t.Fatalf("keys sent:\n%s", b)
	}
}

// #3521: the command the hook saw travels beside the payload and is what the dialog is held to.
func TestPermissionKeysHoldsTheDialogToTheWholeCommand(t *testing.T) {
	stubPane(t, permFixture(t, "bash_multiline_command.txt"))
	detail := "Bash · cat > note.txt <<'EOF'"
	full := "cat > note.txt <<'EOF'\nhello\nEOF\nls -l note.txt"
	if keys, code := permissionKeys("", "s", claude.PermAllow, detail, full, ""); code != "" || len(keys) != 1 {
		t.Errorf("same command: keys=%v code=%q", keys, code)
	}
	for name, cmd := range map[string]string{
		"first line only":     "cat > note.txt <<'EOF'",
		"newlines as spaces":  "cat > note.txt <<'EOF' hello EOF ls -l note.txt",
		"no command recorded": "",
	} {
		if keys, code := permissionKeys("", "s", claude.PermAllow, detail, cmd, ""); code != "target_mismatch" || keys != nil {
			t.Errorf("%s: keys=%v code=%q", name, keys, code)
		}
	}
}

// The hook records the whole command in the same record as the one-line payload, and a prompt for
// another tool never inherits an earlier command.
func TestPermissionPayloadCarriesTheCommand(t *testing.T) {
	withTempHome(t)
	sid := "sid-3521"
	status.WriteLastToolCommand(sid, "Bash · a", "a\nb")
	applyPendingPayloads(sid, "permission", hookInput{})
	d, c, _ := status.ReadPendingPermissionCommand(sid)
	if d != "Bash · a" || c != "a\nb" {
		t.Fatalf("not carried: %q %q", d, c)
	}
	if d, _ := status.ReadPendingPermission(sid); d != "Bash · a" {
		t.Fatalf("display payload carries the command: %q", d)
	}
	status.WriteLastToolCommand(sid, "Edit · /x", "")
	applyPendingPayloads(sid, "permission", hookInput{})
	if _, c, _ := status.ReadPendingPermissionCommand(sid); c != "" {
		t.Fatalf("stale command kept: %q", c)
	}
	if got := bashCommand("Edit", "x"); got != "" {
		t.Fatalf("bashCommand(Edit) = %q", got)
	}
}

// #3521 review: the command is one record with the payload, so the prompt token of P cannot
// answer a record that now holds Q's command; and Q's extra lines are refused by both answer paths.
func TestPermissionAnswerIsBoundToTheWholeRecord(t *testing.T) {
	logPath := fakeTmux(t)
	withTempHome(t)
	pane := replaceCommandLine(permFixture(t, "bash_ask_rule_af_label.txt"), " echo live-check-3357\n", " echo live-check-3357\n rm -rf build\n")
	stubPane(t, pane)
	sid := permissionSession(t, "slot90") // P = `echo live-check-3357`
	tokP := permissionToken(sid)
	meta, _ := session.ReadMeta("slot90")
	// Same first line, Q's screen has an extra line: refused by the card and the button.
	if rec := postPermission("slot90", `{"choice":"allow","expect":"`+tokP+`"}`); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "target_mismatch") {
		t.Fatalf("card: %d %s", rec.Code, rec.Body.String())
	}
	if out, err := answerClaudePermission(meta, claude.PermAllow, tokP, true); err != nil || !strings.Contains(out, "not the one this button") {
		t.Fatalf("button: %q %v", out, err)
	}
	// Q's record replaces P's (command and payload together, new token): P's token is stale.
	time.Sleep(10 * time.Millisecond)
	status.WritePendingPermissionCommand(sid, "Bash · echo live-check-3357", "echo live-check-3357\nrm -rf build")
	if rec := postPermission("slot90", `{"choice":"allow","expect":"`+tokP+`"}`); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "permission_changed") {
		t.Fatalf("card, stale token: %d %s", rec.Code, rec.Body.String())
	}
	if out, _ := answerClaudePermission(meta, claude.PermAllow, tokP, true); strings.Contains(out, "Allowed") {
		t.Fatalf("button, stale token: %q", out)
	}
	// Control: Q's own token with Q's command matches.
	if rec := postPermission("slot90", `{"choice":"allow","expect":"`+permissionToken(sid)+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("control: %d %s", rec.Code, rec.Body.String())
	}
	_ = logPath
}

// A Bash payload with no command (the hook saw an empty one) is refused, not left unchecked.
func TestPermissionKeysRefusesBareBash(t *testing.T) {
	stubPane(t, permFixture(t, "bash_ask_rule_af_label.txt"))
	for _, d := range []string{"Bash", "Bash · "} {
		if keys, code := permissionKeys("", "s", claude.PermAllow, d, "", ""); code != "target_mismatch" || keys != nil {
			t.Errorf("%q: keys=%v code=%q", d, keys, code)
		}
	}
}

// A prompt Claude Code closed by itself fires no hook; the next poll retires it once the
// transcript holds the call's result, newer than the payload, AND the pane shows no dialog
// (#3725).
func TestSweepAnsweredPermission(t *testing.T) {
	withTempHome(t)
	const id = "toolu_01SweepMe"
	use := []byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"` + id + `","name":"Bash","input":{"command":"echo hi"}}]}}`)
	result := func(at time.Time) []byte {
		return []byte(`{"type":"user","timestamp":"` + at.UTC().Format(time.RFC3339Nano) + `","message":{"content":[{"type":"tool_result","tool_use_id":"` + id + `","is_error":true,"content":"Permission for this command was denied"}]}}`)
	}
	answered := [][]byte{use, result(time.Now().Add(time.Minute))}
	// The id is an earlier call's that finished before this prompt was raised.
	stale := [][]byte{use, result(time.Now().Add(-time.Minute))}
	noDialog := "❯ \n  ⏵⏵ bypass permissions on\n"
	dialog := permFixture(t, "bash_ask_rule_af_label.txt")

	cases := []struct {
		name, toolUseID, pane string
		lines                 [][]byte
		question, plan        bool
		swept                 bool
	}{
		{name: "answered and closed", toolUseID: id, pane: noDialog, lines: answered, swept: true},
		// The id can be a parallel sibling's that finished while the real prompt is still up.
		{name: "a dialog is still up", toolUseID: id, pane: dialog, lines: answered},
		// The dialog may not be drawn yet, or be one the parser cannot read.
		{name: "no result yet", toolUseID: id, pane: noDialog, lines: [][]byte{use}},
		{name: "result older than the payload", toolUseID: id, pane: noDialog, lines: stale},
		{name: "payload without an id", pane: noDialog, lines: answered},
		// A failed capture is not an absent dialog.
		{name: "pane not captured", toolUseID: id, pane: "", lines: answered},
		// AskUserQuestion / ExitPlanMode raise their own prompt with a stale id, and their menu
		// does not parse as a permission dialog.
		{name: "masked by a question", toolUseID: id, pane: noDialog, lines: answered, question: true},
		{name: "masked by a plan", toolUseID: id, pane: noDialog, lines: answered, plan: true},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stubPane(t, c.pane)
			name := "slot9" + strconv.Itoa(i)
			sid := permissionSession(t, name)
			if c.question {
				status.WritePendingQuestion(sid, []byte(`[{"question":"Which?"}]`))
			}
			if c.plan {
				status.WritePendingPlan(sid, "the plan")
			}
			status.WritePendingPermissionCall(sid, "Bash · echo hi", "echo hi", c.toolUseID)
			sweepAnsweredPermission(sid, name, c.lines)
			_, kept := status.ReadPendingPermission(sid)
			st, _ := status.Read(sid)
			if c.swept && (kept || st.State != "idle") {
				t.Fatalf("not swept: payload kept=%v state=%q", kept, st.State)
			}
			if !c.swept && (!kept || st.State != "permission") {
				t.Fatalf("swept a live prompt: payload kept=%v state=%q", kept, st.State)
			}
		})
	}
}

// The hook carries the PreToolUse call's id onto the pending payload, and the payload's
// command reads back without it.
func TestPendingPermissionKeepsTheToolUseID(t *testing.T) {
	withTempHome(t)
	sid := "sid-3725"
	status.WriteLastToolCall(sid, "Bash · echo hi", "echo hi", "toolu_01Keep")
	applyPendingPayloads(sid, "permission", hookInput{})
	if id, ok := status.ReadPendingPermissionToolUseID(sid); !ok || id != "toolu_01Keep" {
		t.Fatalf("id = %q %v", id, ok)
	}
	if d, cmd, ok := status.ReadPendingPermissionCommand(sid); !ok || d != "Bash · echo hi" || cmd != "echo hi" {
		t.Fatalf("detail=%q command=%q ok=%v", d, cmd, ok)
	}
}

// #3521: P wrapped by the terminal and Q with a real newline at that wrap draw the same screen,
// so the screen alone matches Q for P. Q's PreToolUse record, written before Q's dialog and
// before Q's Notification rewrites the payload, is what tells them apart: an answer for P is
// refused while that record also matches the screen.
func TestPermissionAnswerRefusesNewlineVariantOfTheNewerCall(t *testing.T) {
	logPath := fakeTmux(t)
	withTempHome(t)
	edge := "echo " + strings.Repeat("a", 73)  // reaches the pane edge: a hard wrap
	short := "echo " + strings.Repeat("a", 60) // stops short: the next word did not fit
	next := strings.Repeat("b", 20)
	layouts := []struct {
		name, first, second, p, q string
	}{
		{"pane edge", edge, "rm -rf build", edge + " rm -rf build", edge + "\nrm -rf build"},
		{"pane edge, no blank", edge, "rm -rf build", edge + "rm -rf build", edge + "\nrm -rf build"},
		{"word wrap", short, next, short + " " + next, short + "\n" + next},
		{"newline is P", short, next, short + "\n" + next, short + " " + next},
	}
	for i, l := range layouts {
		name := "nlq" + strconv.Itoa(i)
		stubPane(t, replaceCommandLine(permFixture(t, "bash_ask_rule_af_label.txt"), " echo live-check-3357\n", " "+l.first+"\n "+l.second+"\n"))
		sid := permissionSession(t, name)
		status.WritePendingPermissionCommand(sid, "Bash · "+l.first, l.p)
		tok := permissionToken(sid)
		meta, _ := session.ReadMeta(name)
		m, _ := readPermissionMenu(name)
		if m.CheckTargetCommand("Bash · "+l.first, "", l.p) != claude.TargetMatches || m.CheckTargetCommand("Bash · "+l.first, "", l.q) != claude.TargetMatches {
			t.Fatalf("%s: the screen no longer reads as both P and Q", l.name)
		}
		status.WriteLastToolCall(sid, "Bash · "+l.first, l.q, "toolu_q")
		for _, c := range []string{claude.PermAllow, claude.PermDeny} {
			if out, err := answerClaudePermission(meta, c, tok, true); err != nil || !strings.Contains(out, "not the one this button") {
				t.Errorf("%s button %s: %q %v", l.name, c, out, err)
			}
		}
		if rec := postPermission(name, `{"choice":"allow","expect":"`+tok+`"}`); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "target_mismatch") {
			t.Errorf("%s card: %d %s", l.name, rec.Code, rec.Body.String())
		}
	}
	if b, _ := os.ReadFile(logPath); strings.Contains(string(b), "send-keys") {
		t.Fatalf("an answer for P was sent to a dialog that may be Q:\n%s", b)
	}

	// The wrapped P still answers when no newer call is recorded and when the record is P itself.
	for i, last := range [][2]string{{"", ""}, {"Bash · " + short, short + " " + next}} {
		name := "nlp" + strconv.Itoa(i)
		stubPane(t, replaceCommandLine(permFixture(t, "bash_ask_rule_af_label.txt"), " echo live-check-3357\n", " "+short+"\n "+next+"\n"))
		sid := permissionSession(t, name)
		status.WritePendingPermissionCommand(sid, "Bash · "+short, short+" "+next)
		if last[0] != "" {
			status.WriteLastToolCall(sid, last[0], last[1], "toolu_p")
		}
		if rec := postPermission(name, `{"choice":"allow","expect":"`+permissionToken(sid)+`"}`); rec.Code != http.StatusOK {
			t.Errorf("last-tool %q: %d %s", last[0], rec.Code, rec.Body.String())
		}
	}
}

// Review round 1 of #4125: the evidence of a newer call must not be lost on the way.
//   - Q recorded, then a background call R (not on screen) overwrites the single slot.
//   - The record cannot be read (here: the path is a directory, EISDIR).
//
// Both refuse a wrap-ambiguous dialog; an unambiguous one still answers, since the screen then
// pins the dialog to P's exact text.
func TestPermissionAnswerRefusesWhenNewerCallEvidenceIsLost(t *testing.T) {
	logPath := fakeTmux(t)
	withTempHome(t)
	short := "echo " + strings.Repeat("a", 60)
	next := strings.Repeat("b", 20)
	wrapped := replaceCommandLine(permFixture(t, "bash_ask_rule_af_label.txt"), " echo live-check-3357\n", " "+short+"\n "+next+"\n")
	p, q := short+" "+next, short+"\n"+next
	setups := map[string]func(sid string){
		"overwritten by R": func(sid string) {
			status.WriteLastToolCall(sid, "Bash · "+short, q, "toolu_q")
			status.WriteLastToolCall(sid, "Bash · ls -la", "ls -la", "toolu_r")
		},
		"unrelated Edit": func(sid string) { status.WriteLastToolCall(sid, "Edit · /tmp/x.go", "", "toolu_r") },
		"unreadable": func(sid string) {
			if err := os.MkdirAll(filepath.Join(paths.AgentStateDir(), "pending-perm", sid+".tool"), 0o700); err != nil {
				t.Fatal(err)
			}
		},
	}
	i := 0
	for name, setup := range setups {
		sess := "nle" + strconv.Itoa(i)
		i++
		stubPane(t, wrapped)
		sid := permissionSession(t, sess)
		status.WritePendingPermissionCommand(sid, "Bash · "+short, p)
		setup(sid)
		tok := permissionToken(sid)
		meta, _ := session.ReadMeta(sess)
		if out, err := answerClaudePermission(meta, claude.PermAllow, tok, true); err != nil || !strings.Contains(out, "not the one this button") {
			t.Errorf("%s button: %q %v", name, out, err)
		}
		if rec := postPermission(sess, `{"choice":"allow","expect":"`+tok+`"}`); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "target_mismatch") {
			t.Errorf("%s card: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	if b, _ := os.ReadFile(logPath); strings.Contains(string(b), "send-keys") {
		t.Fatalf("an answer was sent while the newer call's evidence was lost:\n%s", b)
	}
	// The same records beside an unambiguous dialog (one short line) refuse nothing.
	j := 0
	for name, setup := range setups {
		sess := "nlu" + strconv.Itoa(j)
		j++
		stubPane(t, permFixture(t, "bash_ask_rule_af_label.txt"))
		sid := permissionSession(t, sess)
		setup(sid)
		if rec := postPermission(sess, `{"choice":"allow","expect":"`+permissionToken(sid)+`"}`); rec.Code != http.StatusOK {
			t.Errorf("%s, unambiguous: %d %s", name, rec.Code, rec.Body.String())
		}
	}
}

// Q's Notification completing while the answer for P reads the pane: the record is retired only
// after Q's payload is published, so the answer sees either Q's record or Q's token, never
// neither.
func TestPermissionAnswerRacingThePublish(t *testing.T) {
	logPath := fakeTmux(t)
	withTempHome(t)
	short := "echo " + strings.Repeat("a", 60)
	next := strings.Repeat("b", 20)
	wrapped := replaceCommandLine(permFixture(t, "bash_ask_rule_af_label.txt"), " echo live-check-3357\n", " "+short+"\n "+next+"\n")
	sid := permissionSession(t, "nlr")
	status.WritePendingPermissionCommand(sid, "Bash · "+short, short+" "+next)
	status.WriteLastToolCall(sid, "Bash · "+short, short+"\n"+next, "toolu_q")
	tok := permissionToken(sid)
	prev := readPermissionMenu
	readPermissionMenu = func(string) (claude.PermissionMenu, bool) {
		time.Sleep(10 * time.Millisecond) // a later mtime for Q's payload
		status.PublishPermissionPrompt(sid, "")
		return claude.ParsePermissionMenu(wrapped)
	}
	t.Cleanup(func() { readPermissionMenu = prev })
	if rec := postPermission("nlr", `{"choice":"allow","expect":"`+tok+`"}`); rec.Code != http.StatusConflict {
		t.Fatalf("answer for P during Q's publish: %d %s", rec.Code, rec.Body.String())
	}
	if b, _ := os.ReadFile(logPath); strings.Contains(string(b), "send-keys") {
		t.Fatalf("keys sent:\n%s", b)
	}
}
