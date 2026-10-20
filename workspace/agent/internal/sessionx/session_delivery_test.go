package sessionx

// The pure part of delivery verification: detecting a draft left in the composer, which is
// what decides between re-sending Enter and retyping the whole prompt.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
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
