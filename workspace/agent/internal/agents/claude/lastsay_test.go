package claude

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// resetLastSayCache drops the memo between subtests. The cache is keyed by mtime, and two
// writes inside one test can land on the same one — without this a subtest would read the
// previous subtest's answer and pass for the wrong reason.
func resetLastSayCache() {
	lastSayMu.Lock()
	lastSayCache = map[string]lastSayEntry{}
	lastSayMu.Unlock()
}

// say builds an assistant record carrying one text block, plus whatever extra flags the
// caller needs on the line itself.
func say(text, extra string) string {
	return fmt.Sprintf(`{"type":"assistant"%s,"message":{"content":[{"type":"text","text":%q}]}}`, extra, text)
}

// TestLastSayReadsTheNewestUtterance pins what the overview card shows (ADR 0078 decision
// 12): the last thing the AGENT said, not the last thing that happened.
func TestLastSayReadsTheNewestUtterance(t *testing.T) {
	const sid = "bbbb2222-3333-5ccc-8ddd-444455556666"

	write := func(t *testing.T, lines ...string) string {
		t.Helper()
		resetLastSayCache()
		root := t.TempDir()
		t.Setenv("CLAUDE_CONFIG_DIR", root)
		dir := filepath.Join(root, "projects", "proj-a")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, sid+".jsonl")
		if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	toolOnly := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}}]}}`
	userTurn := `{"type":"user","message":{"content":"次はテストを書いて"}}`

	t.Run("the newest text block wins", func(t *testing.T) {
		write(t, say("古い答え", ""), say("新しい答え", ""))
		if got := LastSay(sid); got != "新しい答え" {
			t.Errorf("LastSay = %q, want the newest utterance", got)
		}
	})

	t.Run("walks past tool calls and the user's own words", func(t *testing.T) {
		write(t, say("直前に言ったこと", ""), toolOnly, userTurn)
		if got := LastSay(sid); got != "直前に言ったこと" {
			t.Errorf("LastSay = %q — a tool trace and a user prompt are not the agent speaking", got)
		}
	})

	t.Run("a subagent's turn is not this session speaking", func(t *testing.T) {
		write(t, say("親が言ったこと", ""), say("子が言ったこと", `,"isSidechain":true`))
		if got := LastSay(sid); got != "親が言ったこと" {
			t.Errorf("LastSay = %q, want the main conversation's line", got)
		}
	})

	t.Run("an API-error record is not an answer", func(t *testing.T) {
		// errors.go's measured shape: the text is the CLI-facing "Please run /login".
		bad := `{"type":"assistant","isApiErrorMessage":true,"apiErrorStatus":401,` +
			`"message":{"model":"<synthetic>","content":[{"type":"text","text":"Please run /login · API Error: 401"}]}}`
		write(t, say("落ちる前の答え", ""), bad)
		if got := LastSay(sid); got != "落ちる前の答え" {
			t.Errorf("LastSay = %q — a failed turn must not read as something the agent said", got)
		}
	})

	t.Run("a meta line is not an answer", func(t *testing.T) {
		write(t, say("本当の答え", ""), say("差し込まれた行", `,"isMeta":true`))
		if got := LastSay(sid); got != "本当の答え" {
			t.Errorf("LastSay = %q, want the real utterance", got)
		}
	})

	t.Run("nothing said yet", func(t *testing.T) {
		write(t, toolOnly, userTurn)
		if got := LastSay(sid); got != "" {
			t.Errorf("LastSay = %q, want \"\" — an invented line is worse than a blank card", got)
		}
	})

	t.Run("no transcript at all", func(t *testing.T) {
		resetLastSayCache()
		t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
		if got := LastSay(sid); got != "" {
			t.Errorf("LastSay = %q for a session with no log", got)
		}
	})

	t.Run("a stub sibling is skipped for the log that holds the conversation", func(t *testing.T) {
		resetLastSayCache()
		root := t.TempDir()
		t.Setenv("CLAUDE_CONFIG_DIR", root)
		for dir, body := range map[string]string{
			"proj-real": say("本物の会話", "") + "\n",
			"proj-stub": `{"type":"bridge-session","bridgeSessionId":"cse_x"}` + "\n",
		} {
			d := filepath.Join(root, "projects", dir)
			if err := os.MkdirAll(d, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(d, sid+".jsonl"), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		// The stub carries the NEWER mtime — the case TranscriptRead documents, where
		// reading the newest file alone would show an empty conversation.
		stub := filepath.Join(root, "projects", "proj-stub", sid+".jsonl")
		if err := os.Chtimes(stub, time.Now(), time.Now().Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		if got := LastSay(sid); got != "本物の会話" {
			t.Errorf("LastSay = %q, want the sibling that actually holds the conversation", got)
		}
	})

	t.Run("an unchanged transcript is not re-read", func(t *testing.T) {
		p := write(t, say("最初の答え", ""))
		if got := LastSay(sid); got != "最初の答え" {
			t.Fatalf("LastSay = %q on the first read", got)
		}
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		// Rewrite the content but restore the mtime: a reader that re-parses on every poll
		// would return the new text, one that honours the memo returns the old.
		if err := os.WriteFile(p, []byte(say("書き換えた答え", "")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, fi.ModTime(), fi.ModTime()); err != nil {
			t.Fatal(err)
		}
		if got := LastSay(sid); got != "最初の答え" {
			t.Errorf("LastSay = %q — the mtime is unchanged, so the memo must answer without a read", got)
		}
	})

	// The cost rule, stated as behaviour: only ONE tail window is read, so an utterance
	// buried under a window's worth of tool records is out of reach — where lastLineWhere
	// would widen to the whole multi-MB file on every 4 s poll (docs/log/96 §96.9).
	t.Run("never widens past the tail window", func(t *testing.T) {
		lines := []string{say("窓の外の答え", "")}
		filler := `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"` +
			strings.Repeat("x", 4096) + `"}]}}`
		for i := 0; i < (transcriptTailWindow/4096)+2; i++ {
			lines = append(lines, filler)
		}
		write(t, lines...)
		if got := LastSay(sid); got != "" {
			t.Errorf("LastSay = %q — the whole file must not be read to find it", got)
		}
	})

	// …and when the window holds nothing, what was already known is KEPT. Blanking the
	// card halfway through a long turn would read as "this session went quiet".
	t.Run("keeps the known line when the window holds no utterance", func(t *testing.T) {
		p := write(t, say("長いターンの前に言ったこと", ""))
		if got := LastSay(sid); got != "長いターンの前に言ったこと" {
			t.Fatalf("LastSay = %q on the first read", got)
		}
		f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		filler := `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"` +
			strings.Repeat("x", 4096) + `"}]}}` + "\n"
		for i := 0; i < (transcriptTailWindow/4096)+2; i++ {
			if _, err := f.WriteString(filler); err != nil {
				t.Fatal(err)
			}
		}
		f.Close()
		if got := LastSay(sid); got != "長いターンの前に言ったこと" {
			t.Errorf("LastSay = %q, want the line the session is still known to have said", got)
		}
	})
}

// TestLastSayLine pins the folding: a card gets one line, and the cut is by rune.
func TestLastSayLine(t *testing.T) {
	for _, c := range []struct {
		name, in, want string
	}{
		{"collapses the paragraph breaks of a Markdown answer",
			"実装しました。\n\n- 1 つ目\n- 2 つ目", "実装しました。 - 1 つ目 - 2 つ目"},
		{"trims the leading and trailing blank", "  答え\n", "答え"},
		{"tabs and full-width spaces collapse too", "a\t\tb　c", "a b c"},
		{"short text is untouched", "短い", "短い"},
		{"empty stays empty", "   \n  ", ""},
		{"caps at lastSayMax runes",
			strings.Repeat("あ", 200), strings.Repeat("あ", lastSayMax) + "…"},
		{"does not leave a space before the ellipsis",
			strings.Repeat("あ", lastSayMax-1) + " ののの", strings.Repeat("あ", lastSayMax-1) + "…"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := lastSayLine(c.in); got != c.want {
				t.Errorf("lastSayLine(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
	// Bytes would cut a Japanese answer mid-codepoint and put invalid UTF-8 on the wire.
	if got := []rune(lastSayLine(strings.Repeat("あ", 300))); len(got) != lastSayMax+1 {
		t.Errorf("capped length = %d runes, want %d + the ellipsis", len(got), lastSayMax)
	}
}
