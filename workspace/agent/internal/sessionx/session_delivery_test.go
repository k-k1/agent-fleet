package sessionx

// The pure part of delivery verification: detecting a draft left in the composer, which is
// what decides between re-sending Enter and retyping the whole prompt.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

func TestPromptDraftVisible(t *testing.T) {
	captured := "…transcript…\n" +
		"──────────────── [AF] 定時 ──\n" +
		"❯ /scout\n" +
		"───────────────────────────\n" +
		"  ⏵⏵ bypass permissions on (shift+tab to cycle)\n"
	if !promptDraftVisible(captured, "/scout") {
		t.Fatal("draft sitting in the composer must be detected")
	}
	// Already submitted (empty composer) is false, so the caller branches to retyping.
	submitted := "…transcript…\n❯ \n  ⏵⏵ bypass permissions on (shift+tab to cycle)\n"
	if promptDraftVisible(submitted, "/scout") {
		t.Fatal("an empty composer must not read as a draft")
	}
	// A long or multi-line prompt matches on the first 12 runes of its first line (rune-safe
	// and tolerant of wrapping).
	long := "セッションの定時レビューを開始してください。対象は昨日の差分すべてで、結果はレポートにまとめること。"
	capturedLong := "❯ セッションの定時レビューを開\nください。…（折り返し）\n⏵⏵ bypass permissions on\n"
	if !promptDraftVisible(capturedLong, long+"\n二行目") {
		t.Fatal("long multi-line prompt must match on its first-line head")
	}
	if promptDraftVisible("", "/scout") || promptDraftVisible(capturedLong, "") {
		t.Fatal("empty capture or empty prompt must be false")
	}
}

// The work-item launch that sent its prompt twice: claude held a 5-line draft for review, the
// first line sat above the last 6 lines, and the retype was appended to the draft.
func TestPromptDraftVisibleMultiLineHeldDraft(t *testing.T) {
	prompt := "作業対象: k-k1/agent-fleet#978「Deliver AF_SESSION_NAME to Managed sessions (muse, ACP kinds, opencode; codex after daemon swap)」\n" +
		"URL: https://github.com/k-k1/agent-fleet/issues/978\n\n" +
		"本文とコメントは `gh issue view 978`（PR なら `gh pr view 978`）で読めます。\n" +
		"まず状況を調べ、実装に入る前に方針を提示してください。"
	screen := func(notice string) string {
		return " ▐▛███▛█   Claude Code v2.1.282\n" +
			notice + "\n" +
			"──── [AF:sfwk7vk] #978 Deliver AF_SESSION_NAME to Managed sessions (muse, ACP… ─\n" +
			"❯ 作業対象: k-k1/agent-fleet#978「Deliver AF_SESSION_NAME to Managed sessions\n" +
			"  (muse, ACP kinds, opencode; codex after daemon swap)」\n" +
			"  URL: https://github.com/k-k1/agent-fleet/issues/978\n" +
			"\n" +
			"  本文とコメントは `gh issue view 978`（PR なら `gh pr view 978`）で読めます。\n" +
			"  まず状況を調べ、実装に入る前に方針を提示してください。\n" +
			"────────────────────────────────────────────────────────────────────────────────\n" +
			"  ⏵⏵ bypass permissions on (shift+tab to cycle)\n"
	}
	if !promptDraftVisible(screen(""), prompt) {
		t.Fatal("a multi-line draft whose first line is above the pane tail must still be detected")
	}
	// The hold notice alone is enough, even when the draft text cannot be matched.
	held := strings.Replace(screen("Removed 1 invisible character · review and press Enter to send"), "作業対象", "作業対", 1)
	if !promptDraftVisible(held, prompt) {
		t.Fatal("claude holding the draft for review must read as a draft (resend Enter, never retype)")
	}
	pasted := "──── ─\n❯ [Pasted text #1 +5 lines]\n────\n  ⏵⏵ bypass permissions on\n"
	if !promptDraftVisible(pasted, prompt) {
		t.Fatal("a draft folded into a paste placeholder must read as a draft")
	}
}

// The same launch on an 80x23 pane, captured 12 s later when the self-heal looks: the hold
// notice has been replaced by another one, and the composer has scrolled the draft so its
// first line is off screen. Only the prompt's end is left to match.
func TestPromptDraftVisibleScrolledHeldDraft(t *testing.T) {
	prompt := "作業対象: k-k1/agent-fleet#989「opencode Managed: identify the calling session through a tool.execute.before plugin」\n" +
		"URL: https://github.com/k-k1/agent-fleet/issues/989\n\n" +
		"本文とコメントは `gh issue view 989`（PR なら `gh pr view 989`）で読めます。\n" +
		"まず状況を調べ、実装に入る前に方針を提示してください。"
	captured := " ▐▛███▛█   Claude Code v2.1.283\n" +
		"\n\n\n\n\n\n\n\n" +
		"  tmux focus-events off · add 'set -g focus-events on' to ~/.tmux.conf and re…\n" +
		"────── [AF:probe] #989 opencode Managed: identify the calling session through… ─\n" +
		"❯ session through a tool.execute.before plugin」\n" +
		"  URL: https://github.com/k-k1/agent-fleet/issues/989\n" +
		"\n" +
		"  本文とコメントは `gh issue view 989`（PR なら `gh pr view 989`）で読めます。\n" +
		"  まず状況を調べ、実装に入る前に方針を提示してください。\n" +
		"\n" +
		"────────────────────────────────────────────────────────────────────────────────\n" +
		"\n" +
		"  ⏵⏵ bypass permissions on (shift+tab to cycle)\n"
	if !promptDraftVisible(captured, prompt) {
		t.Fatal("a draft scrolled so only its end is visible must still read as a draft")
	}
	submitted := strings.Replace(captured,
		"❯ session through a tool.execute.before plugin」\n"+
			"  URL: https://github.com/k-k1/agent-fleet/issues/989\n"+
			"\n"+
			"  本文とコメントは `gh issue view 989`（PR なら `gh pr view 989`）で読めます。\n"+
			"  まず状況を調べ、実装に入る前に方針を提示してください。\n", "❯ \n", 1)
	if promptDraftVisible(submitted, prompt) {
		t.Fatal("an empty composer must not read as a draft")
	}
}

// Only the composer counts: the same prompt submitted earlier is in the scrollback as a
// "❯ …" line too (the scheduler's reuse send repeats one prompt), and must not read as a draft.
func TestPromptDraftVisibleIgnoresScrollback(t *testing.T) {
	captured := "❯ /scout\n● done\n──────── [AF] 定時 ──\n❯ \n────────\n  ⏵⏵ bypass permissions on\n"
	if promptDraftVisible(captured, "/scout") {
		t.Fatal("an earlier submission above an empty composer must not read as a draft")
	}
}

// A prompt typed into a busy claude is only enqueued (in memory, lost on a pane kill). The
// confirmation must say so (held) instead of calling it a started turn, must not self-heal by
// retyping, and must still end as started once the replayed user line appears.
func TestConfirmPromptDeliveryReportsQueuedAsHeld(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	m := session.Meta{Name: "held_claude", Dir: t.TempDir(), Kind: session.KindClaude}
	proj := filepath.Join(cfg, "projects", "-held")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(proj, session.UUID(m.Dir, m.Name)+".jsonl")
	if err := os.WriteFile(log, []byte(`{"type":"assistant","message":{"content":[]}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	const prompt = "please look at PR 2288 when you are free"
	base := deliveryBaseline(m)
	if base.logs == nil {
		t.Fatal("claude has a verification baseline")
	}
	appendLine := func(v map[string]any) {
		b, _ := json.Marshal(v)
		f, err := os.OpenFile(log, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		f.Write(append(b, '\n'))
	}
	appendLine(map[string]any{"type": "queue-operation", "operation": "enqueue", "content": prompt})

	held, err := confirmPromptDelivery(m, "no-such-pane", prompt, base)
	if err != nil || !held {
		t.Fatalf("enqueue only: held=%v err=%v, want held=true", held, err)
	}

	appendLine(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": prompt}})
	held, err = confirmPromptDelivery(m, "no-such-pane", prompt, base)
	if err != nil || held {
		t.Fatalf("enqueue then user line: held=%v err=%v, want delivered", held, err)
	}
}

// #2741: a resumed claude replays the notification of a background task that ended while it
// was stopped, and its turn puts the spinner up. Neither the notification's user line nor that
// spinner is evidence that the typed prompt started a turn. With nothing appended at all, the
// spinner keeps its old role as insurance against a delayed jsonl flush.
func TestDeliveryEvidencedIgnoresATurnClaudeStartedItself(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	m := session.Meta{Name: "resumed_claude", Dir: t.TempDir(), Kind: session.KindClaude}
	proj := filepath.Join(cfg, "projects", "-resumed")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(proj, session.UUID(m.Dir, m.Name)+".jsonl")
	if err := os.WriteFile(log, []byte(`{"type":"assistant","message":{"content":[]}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	busy := false
	orig := deliveryPaneBusy
	deliveryPaneBusy = func(string) bool { return busy }
	t.Cleanup(func() { deliveryPaneBusy = orig })

	const prompt = "[agent-fleet:peer from=sparent intent=question reply=required] push the branch"
	base := deliveryBaseline(m)
	busy = true
	if got := deliveryEvidenced(m, base, prompt); got != claude.PromptStarted {
		t.Fatalf("spinner with nothing appended (a delayed flush): got %v, want started", got)
	}

	b, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user",
		"content": "<task-notification>\n<task-id>m1</task-id>\n<status>killed</status>\n</task-notification>"}})
	f, err := os.OpenFile(log, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.Write(append(b, '\n'))
	f.Close()
	if got := deliveryEvidenced(m, base, prompt); got != claude.PromptAbsent {
		t.Fatalf("a replayed task notification running with its spinner up: got %v, want absent", got)
	}
}

// #2741: delivery_unconfirmed used to leave the "working" submitPromptTUI wrote on typing, so
// the session read working while it sat idle with the message lost. The withdrawal writes the
// unknown idle (never a turn end), and leaves alone a record a hook wrote since and a pane that
// was busy before the send.
func TestWithdrawOptimisticWorking(t *testing.T) {
	isolateAgentState(t)
	busy := false
	orig := deliveryPaneBusy
	deliveryPaneBusy = func(string) bool { return busy }
	t.Cleanup(func() { deliveryPaneBusy = orig })
	m := session.Meta{Name: "unconfirmed", Dir: t.TempDir(), Kind: session.KindClaude}
	sid := session.UUID(m.Dir, m.Name)
	mark := func() status.SessionStatus {
		status.Persist(sid, "working")
		st, _ := status.Read(sid)
		return st
	}

	withdrawOptimisticWorking(m, deliverySnapshot{}, mark())
	if st, _ := status.Read(sid); st.State != "idle" || st.TurnEnd {
		t.Fatalf("after an unconfirmed send: %+v, want the unknown idle", st)
	}

	marked := mark()
	status.Persist(sid, "working") // a hook's write after ours
	withdrawOptimisticWorking(m, deliverySnapshot{}, marked)
	if got := status.LiveState(sid); got != "working" {
		t.Fatalf("a record written since was overwritten: %q", got)
	}

	withdrawOptimisticWorking(m, deliverySnapshot{paneBusy: true}, mark())
	if got := status.LiveState(sid); got != "working" {
		t.Fatalf("a pane busy before the send is working: got %q", got)
	}
	busy = true
	withdrawOptimisticWorking(m, deliverySnapshot{}, mark())
	if got := status.LiveState(sid); got != "working" {
		t.Fatalf("a pane busy now is working: got %q", got)
	}
}
