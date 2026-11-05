package sessionx

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// stubPane makes readPermissionMenu parse pane instead of capturing tmux.
func stubPane(t *testing.T, pane string) {
	t.Helper()
	prev := readPermissionMenu
	readPermissionMenu = func(string) (claude.PermissionMenu, bool) { return claude.ParsePermissionMenu(pane) }
	t.Cleanup(func() { readPermissionMenu = prev })
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
			keys, code := permissionKeys("s", choice)
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
		if keys, code := permissionKeys("s", c); keys != nil || code != "menu_unreadable" {
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
	status.WritePendingPermission(sid, "Bash · ls")
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
	status.WritePendingPermission(sid, "Bash · ls")
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
	pane := permFixture(t, "bash_ask_rule_two_options.txt")

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
