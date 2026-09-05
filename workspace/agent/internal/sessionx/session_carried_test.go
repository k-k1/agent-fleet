package sessionx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// The delivery wording is a function, not decoration. Measured in docs/log/75 §75.10 C:
// without the "do not ask again" line claude asks the question again (it is gone from the
// conversation tree). When rewriting the wording, check these two invariants still hold.
func TestCarriedQuestionPromptCarriesQuestionAndForbidsReasking(t *testing.T) {
	qs := []transcript.Question{{Header: "書き込む語", Question: "out.txt に書き込む語を選んでください。"}}
	got := buildCarriedQuestionPrompt(qs, []CarriedAnswer{{Labels: []string{"みかん"}}})
	if !strings.Contains(got, "質問し直さず") {
		t.Errorf("delivery text lacks the \"do not ask again\" line: %q", got)
	}
	if !strings.Contains(got, "out.txt に書き込む語を選んでください。") {
		t.Errorf("the question text itself is not carried (the answer alone says nothing about what was answered): %q", got)
	}
	if !strings.Contains(got, "みかん") {
		t.Errorf("the chosen label is missing: %q", got)
	}
}

// Input reaches the TUI byte by byte through send-keys -l, so a newline acts as Enter
// (docs/build/92). A delivery text is always a single line.
func TestCarriedPromptsAreSingleLine(t *testing.T) {
	qs := []transcript.Question{{Question: "改行\nを含む\r\n質問"}, {Question: "2 問目"}}
	answers := []CarriedAnswer{{Labels: []string{"A\nB"}, Notes: "補足\nの続き"}, {Labels: []string{"C"}}}
	for _, s := range []string{
		buildCarriedQuestionPrompt(qs, answers),
		buildCarriedPlanPrompt(true, "気を\nつけて"),
		buildCarriedPlanPrompt(false, "作り\n直して"),
		buildCarriedPermissionPrompt("Bash · rm\n-rf"),
	} {
		if strings.ContainsAny(s, "\n\r\t") {
			t.Errorf("delivery text still contains a control character: %q", s)
		}
		if s == "" {
			t.Error("delivery text is empty")
		}
	}
}

func TestCarriedQuestionPromptMultiSelectAndNotes(t *testing.T) {
	qs := []transcript.Question{{Question: "好きな果物は？"}}
	got := buildCarriedQuestionPrompt(qs, []CarriedAnswer{{Labels: []string{"りんご", "みかん"}, Notes: "皮ごと"}})
	if !strings.Contains(got, "りんご・みかん") {
		t.Errorf("a multi-select answer was not folded into one: %q", got)
	}
	if !strings.Contains(got, "（補足: 皮ごと）") {
		t.Errorf("the free-text note was dropped: %q", got)
	}
	// Free text with no option selected is valid too (the notes shape of an AUQ with preview).
	only := buildCarriedQuestionPrompt(qs, []CarriedAnswer{{Notes: "どちらでもない"}})
	if !strings.Contains(only, "どちらでもない") {
		t.Errorf("a note-only answer was dropped: %q", only)
	}
	if buildCarriedQuestionPrompt(qs, []CarriedAnswer{{}}) != "" {
		t.Error("built a delivery text from an empty answer")
	}
}

// An approval is an instruction to go ahead, not the word "approved" (§75.10 E). The wording
// must never be mistakable between approval and rejection.
func TestCarriedPlanPromptApproveRejectAreDistinct(t *testing.T) {
	ok := buildCarriedPlanPrompt(true, "")
	ng := buildCarriedPlanPrompt(false, "")
	if !strings.Contains(ok, "承認します") || strings.Contains(ok, "承認しません") {
		t.Errorf("the approval wording does not read as an approval: %q", ok)
	}
	if !strings.Contains(ng, "承認しません") {
		t.Errorf("the rejection wording does not read as a rejection: %q", ng)
	}
	if withFb := buildCarriedPlanPrompt(false, "手順 2 を分けて"); !strings.Contains(withFb, "手順 2 を分けて") {
		t.Errorf("the instruction given with the rejection was dropped: %q", withFb)
	}
}

// Promotion only moves pending-* into the carried slot. The priority is
// question > plan > permission (the same as EffectiveModal).
func TestPromoteCarriedPrefersQuestionOverPlan(t *testing.T) {
	withTempHome(t)
	sid := "sid-carried-1"
	status.WritePendingQuestion(sid, json.RawMessage(`[{"question":"どっち？","options":[{"label":"A"}]}]`))
	status.WritePendingPlan(sid, "# 計画\n- やる")
	status.WritePendingPermission(sid, "Bash · ls")
	status.AppendPendingText(sid, "その前置き")

	if !status.PromoteCarried(sid, "halt") {
		t.Fatal("PromoteCarried = false, want true")
	}
	c, ok := status.ReadCarried(sid)
	if !ok {
		t.Fatal("the carried interaction cannot be read")
	}
	if c.Kind != "question" {
		t.Errorf("Kind = %q, want question (a question takes priority over plan/permission)", c.Kind)
	}
	if !strings.Contains(string(c.Questions), "どっち？") {
		t.Errorf("the question payload was not carried: %s", c.Questions)
	}
	if c.Text != "その前置き" {
		t.Errorf("Text = %q, want その前置き (the card's context)", c.Text)
	}
	if c.Reason != "halt" {
		t.Errorf("Reason = %q, want halt", c.Reason)
	}
}

func TestPromoteCarriedPlanOnlyAndNoopWhenNothingPending(t *testing.T) {
	withTempHome(t)
	sid := "sid-carried-2"
	if status.PromoteCarried(sid, "stopped") {
		t.Error("promoted even though nothing was pending")
	}
	status.WritePendingPlan(sid, "# 計画")
	if !status.PromoteCarried(sid, "stopped") {
		t.Fatal("promoting a plan returned false")
	}
	c, _ := status.ReadCarried(sid)
	if c.Kind != "plan" || c.Plan != "# 計画" {
		t.Errorf("the plan was not carried: %+v", c)
	}
	// Never overwrite: after a promotion on halt, the boot hook on resume calls this again.
	status.WritePendingQuestion(sid, json.RawMessage(`[{"question":"後から"}]`))
	if status.PromoteCarried(sid, "stopped") {
		t.Error("overwrote an existing carried interaction (it would vanish in the halt->resume order)")
	}
	if c2, _ := status.ReadCarried(sid); c2.Kind != "plan" {
		t.Errorf("Kind = %q, want plan (unchanged)", c2.Kind)
	}
}

// status.Remove (which halt calls) must not delete the carried interaction: get the deletion
// order wrong and it throws away what was promoted a moment earlier.
func TestStatusRemoveKeepsCarried(t *testing.T) {
	withTempHome(t)
	sid := "sid-carried-3"
	status.WritePendingQuestion(sid, json.RawMessage(`[{"question":"残る？"}]`))
	status.PromoteCarried(sid, "halt")
	status.Remove(sid)
	if _, ok := status.ReadPendingQuestion(sid); ok {
		t.Error("pending-question is still there")
	}
	if _, ok := status.ReadCarried(sid); !ok {
		t.Error("status.Remove deleted the carried interaction as well")
	}
	status.RemoveCarried(sid)
	if _, ok := status.ReadCarried(sid); ok {
		t.Error("still there after RemoveCarried")
	}
}

// While a live modal is pending the carried interaction is not surfaced: doing so puts a
// "answer with a keypress" card and a "answer in prose" card side by side in the Console.
func TestSurfaceCarriedYieldsToLivePending(t *testing.T) {
	withTempHome(t)
	sid := "sid-carried-4"
	status.WritePendingQuestion(sid, json.RawMessage(`[{"question":"いま出ている"}]`))
	status.PromoteCarried(sid, "halt")

	resp := map[string]any{}
	surfacePendingPayloads(resp, sid, "question", nil)
	surfaceCarried(resp, sid)
	if _, ok := resp["carried"]; ok {
		t.Error("surfaced the carried interaction while a modal was still pending")
	}

	// Once the pending modal is gone (i.e. after resume) only the carried one is surfaced.
	status.RemovePendingQuestion(sid)
	resp2 := map[string]any{}
	surfacePendingPayloads(resp2, sid, "idle", nil)
	surfaceCarried(resp2, sid)
	c, ok := resp2["carried"].(map[string]any)
	if !ok {
		t.Fatal("the carried interaction is not surfaced")
	}
	if c["kind"] != "question" {
		t.Errorf("kind = %v, want question", c["kind"])
	}
}

// --- P5: carried interactions for kinds other than claude (docs/log/75) ----------------

// fakeModalAgent is a dummy kind that does nothing but implement ModalReporter. It pins down
// only the point where promoteCarriedOther asks the kind (how a real kind detects a pending
// modal — conversation DB, events.jsonl, pane, ACP handle — belongs to each package's own
// tests).
type fakeModalAgent struct {
	agents.Agent
	modal agents.PendingModal
	ok    bool
}

func (f fakeModalAgent) PendingModal(session.Meta) (agents.PendingModal, bool) {
	return f.modal, f.ok
}

// withFakeAgent swaps one kind's registry entry for the duration of a test.
func withFakeAgent(t *testing.T, kind string, a agents.Agent) {
	t.Helper()
	prev, had := agentRegistry[kind]
	agentRegistry[kind] = a
	t.Cleanup(func() {
		if had {
			agentRegistry[kind] = prev
			return
		}
		delete(agentRegistry, kind)
	})
}

// A non-claude kind with a question is carried together with its options, so it can be
// delivered as prose after a resume.
func TestPromoteCarriedOtherCarriesQuestionForm(t *testing.T) {
	withTempHome(t)
	m := session.Meta{Name: "slot-q", Dir: t.TempDir(), Kind: session.KindCodex}
	qs := []transcript.Question{{Question: "どちらで進めますか？",
		Options: []transcript.Option{{Label: "A 案"}, {Label: "B 案"}}}}
	withFakeAgent(t, m.Kind, fakeModalAgent{modal: agents.PendingModal{Kind: "question", Questions: qs}, ok: true})

	if !promoteCarriedOther(m) {
		t.Fatal("promoteCarriedOther = false, want true")
	}
	c, ok := status.ReadCarried(session.UUID(m.Dir, m.Name))
	if !ok {
		t.Fatal("the carried interaction cannot be read")
	}
	if c.Kind != "question" {
		t.Fatalf("Kind = %q, want question", c.Kind)
	}
	// The answer form has to survive the round trip: the Console renders it, the user picks,
	// and the chosen label becomes the delivery text. Broken here, a carried card appears
	// that nobody can answer.
	got := carriedQuestions(c)
	if len(got) != 1 || len(got[0].Options) != 2 || got[0].Options[1].Label != "B 案" {
		t.Fatalf("the question form did not survive the round trip: %+v", got)
	}
	if p := buildCarriedQuestionPrompt(got, []CarriedAnswer{{Labels: []string{"B 案"}}}); !strings.Contains(p, "B 案") ||
		!strings.Contains(p, "質問し直さず") {
		t.Errorf("the delivery text does not assemble: %q", p)
	}
}

// A permission is never carried as a question. The destination of the yes/no (the ACP
// JSON-RPC id, the TUI menu) died with the process, so a choice would reach nobody; only the
// fact is carried.
func TestPromoteCarriedOtherCarriesPermissionAsFactOnly(t *testing.T) {
	withTempHome(t)
	m := session.Meta{Name: "slot-p", Dir: t.TempDir(), Kind: session.KindKiro}
	withFakeAgent(t, m.Kind, fakeModalAgent{
		modal: agents.PendingModal{Kind: "permission", Detail: "shell requires approval"}, ok: true})

	if !promoteCarriedOther(m) {
		t.Fatal("promoteCarriedOther = false, want true")
	}
	c, _ := status.ReadCarried(session.UUID(m.Dir, m.Name))
	if c.Kind != "permission" {
		t.Fatalf("Kind = %q, want permission (carried as a question, it makes the user pick an answer that reaches nobody)", c.Kind)
	}
	if c.Permission != "shell requires approval" {
		t.Errorf("what was being asked has been dropped: %q", c.Permission)
	}
	if len(c.Questions) != 0 {
		t.Errorf("attached an answer form to a permission: %s", c.Questions)
	}
}

// With nothing pending, write nothing: miss this and every fold-up adds one empty card.
func TestPromoteCarriedOtherNoopWithoutModal(t *testing.T) {
	withTempHome(t)
	m := session.Meta{Name: "slot-n", Dir: t.TempDir(), Kind: session.KindAgy}
	withFakeAgent(t, m.Kind, fakeModalAgent{ok: false})
	if promoteCarriedOther(m) {
		t.Error("promoted with nothing pending")
	}
	if _, ok := status.ReadCarried(session.UUID(m.Dir, m.Name)); ok {
		t.Error("wrote an empty carried interaction")
	}
}

// Kinds that do not implement ModalReporter (shell / ssm) are out of scope — they have no
// notion of a pending modal.
func TestPromoteCarriedOtherSkipsKindsWithoutReporter(t *testing.T) {
	withTempHome(t)
	m := session.Meta{Name: "slot-s", Dir: t.TempDir(), Kind: session.KindShell}
	if promoteCarriedOther(m) {
		t.Error("promoted a shell session")
	}
}

// A carried interaction nobody can answer must not be written: a question without an answer
// form, or a Kind nothing knows how to render, would surface as a card that cannot be
// pressed.
func TestPutCarriedRejectsUnanswerableShapes(t *testing.T) {
	withTempHome(t)
	if status.PutCarried("sid-x", status.Carried{Kind: "question"}, "halt") {
		t.Error("wrote a question with no answer form (it becomes a card that cannot be pressed)")
	}
	if status.PutCarried("sid-x", status.Carried{Kind: "teleport"}, "halt") {
		t.Error("wrote an unknown Kind")
	}
	if !status.PutCarried("sid-x", status.Carried{Kind: "permission", Permission: "Bash · ls"}, "halt") {
		t.Fatal("a permission cannot be written")
	}
	// Never overwrite (after a promotion on halt, the shutdown and list routes call again).
	if status.PutCarried("sid-x", status.Carried{Kind: "permission", Permission: "別のもの"}, "stopped") {
		t.Error("overwrote an existing carried interaction")
	}
	if c, _ := status.ReadCarried("sid-x"); c.Permission != "Bash · ls" || c.Reason != "halt" {
		t.Errorf("the first carried interaction did not survive: %+v", c)
	}
}

// --- halt is not the only fold-up route (archive / recreate) --------------------

// orderingModalAgent is a ModalReporter that records WHEN PendingModal was called. If the
// tmux log already carries kill-session at that moment, the pending modal no longer exists —
// a rig for checking the ordering itself (ADR 0055 decision 12; the same bug has shipped
// before, promoting after dropManagedRuntime so that it never fired once).
type orderingModalAgent struct {
	agents.Agent
	modal     agents.PendingModal
	logPath   string
	calls     *int
	afterKill *bool
}

func (a orderingModalAgent) PendingModal(session.Meta) (agents.PendingModal, bool) {
	*a.calls++
	if b, err := os.ReadFile(a.logPath); err == nil && strings.Contains(string(b), "kill-session") {
		*a.afterKill = true
	}
	return a.modal, true
}

// fakeTmuxOnlyFor is the same tmux stub as fakeTmux except that has-session answers "alive"
// for the single session given. fakeTmux always succeeds on has-session, which never lets
// allocSessionName — the walk over slugs looking for a free one that recreate uses to draw a
// new slug — terminate.
func fakeTmuxOnlyFor(t *testing.T, tn string) string {
	t.Helper()
	bin := t.TempDir()
	logPath := filepath.Join(bin, "tmux.log")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$TMUX_TEST_LOG"
case "$1" in
  has-session) [ "$3" = "$TMUX_TEST_ALIVE" ] || exit 1 ;;
  list-panes) printf '1 %%7\n' ;;
  capture-pane) printf '\n' ;;
  load-buffer) /bin/cat > /dev/null ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("TMUX_TEST_LOG", logPath)
	t.Setenv("TMUX_TEST_ALIVE", session.ExactTarget(tn))
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(t.TempDir(), "sessions"))
	isolateAgentConfigDirs(t)
	return logPath
}

// Archive and recreate also fold a session up with a modal still on screen. halt alone being
// correct is not enough: unless these two everyday operations keep the same order, they lose
// the same data (ADR 0055 decision 2: fold up only when nothing is lost). Drive the handlers
// for real and check that carried-interaction is written, and written before the pane dies.
func TestArchiveAndRecreatePromoteCarriedBeforeKill(t *testing.T) {
	for _, tc := range []struct {
		route  string
		handle func(http.ResponseWriter, *http.Request)
	}{
		{"archive", HandleArchiveSession},
		{"recreate", HandleRecreateSession},
	} {
		t.Run(tc.route, func(t *testing.T) {
			const name = "slot_carried"
			// Only this slot is alive, i.e. there is a pane to be killed on the fold-up.
			logPath := fakeTmuxOnlyFor(t, session.TmuxName(name))
			m := session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindCodex}
			session.WriteMeta(m)

			calls, afterKill := 0, false
			withFakeAgent(t, m.Kind, orderingModalAgent{
				Agent:   AgentOf(m.Kind),
				modal:   agents.PendingModal{Kind: "permission", Detail: "shell requires approval"},
				logPath: logPath, calls: &calls, afterKill: &afterKill,
			})

			req := httptest.NewRequest(http.MethodPost, "/sessions/"+name+"/"+tc.route, nil)
			req.SetPathValue("name", name)
			rec := httptest.NewRecorder()
			tc.handle(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("%s: status = %d, body = %s", tc.route, rec.Code, rec.Body.String())
			}

			if calls == 0 {
				t.Fatalf("%s did not call the carried promotion (a pending question disappears silently)", tc.route)
			}
			if afterKill {
				t.Errorf("%s promoted AFTER kill-session (the pending modal is gone with the pane)", tc.route)
			}
			c, ok := status.ReadCarried(session.UUID(m.Dir, name))
			if !ok {
				t.Fatalf("%s: carried-interaction was not written", tc.route)
			}
			if c.Kind != "permission" || c.Permission != "shell requires approval" {
				t.Errorf("%s: wrong carried interaction contents: %+v", tc.route, c)
			}
		})
	}
}

// The generic /messages route for non-claude kinds must surface the carried interaction too.
//
// Without it, the carried interaction of a non-claude session folded up by tier1 is written
// and visible to nobody: the list badge says "stopped, has a question", yet opening it shows
// no card to answer and there is no entry point to POST /carried-answer anywhere.
func TestGenericMessagesSurfacesCarried(t *testing.T) {
	withTempHome(t)
	m := session.Meta{Name: "slot-g", Dir: t.TempDir(), Kind: session.KindCodex}
	sid := session.UUID(m.Dir, m.Name)
	if !status.PutCarried(sid, status.Carried{Kind: "permission", Permission: "shell requires approval"}, "halt") {
		t.Fatal("the carried interaction cannot be written")
	}

	rec := httptest.NewRecorder()
	handleGenericMessages(rec, httptest.NewRequest("GET", "/sessions/slot-g/messages", nil), m, false, "stopped")
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("the response is not JSON: %v (%s)", err, rec.Body.String())
	}
	c, ok := resp["carried"].(map[string]any)
	if !ok {
		t.Fatalf("carried is not present: %v", resp)
	}
	if c["kind"] != "permission" || c["permission"] != "shell requires approval" {
		t.Errorf("wrong carried contents: %v", c)
	}
}
