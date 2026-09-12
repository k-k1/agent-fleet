package claude

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// resetTailCache drops the memo between subtests. The cache is keyed by mtime, and two
// writes inside one test can land on the same one — without this a subtest would read the
// previous subtest's answer and pass for the wrong reason.
func resetTailCache() {
	tailMu.Lock()
	tailCache = map[string]tailFacts{}
	tailMu.Unlock()
}

// saidBy is the utterance half of TailFacts, which most of these tests are about.
func saidBy(sid string) string {
	s, _ := TailFacts(sid)
	return s
}

// spentBy is the trend half.
func spentBy(sid string) []int {
	_, sp := TailFacts(sid)
	return sp
}

// say builds an assistant record carrying one text block, plus whatever extra flags the
// caller needs on the line itself.
func say(text, extra string) string {
	return fmt.Sprintf(`{"type":"assistant"%s,"message":{"content":[{"type":"text","text":%q}]}}`, extra, text)
}

// usage builds an assistant record with token counts and no text — the shape of the rows a
// reply is made of between its words (a tool call, a continuation).
func usage(in, out, read, create int) string {
	return fmt.Sprintf(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t","name":"Bash","input":{}}],`+
		`"usage":{"input_tokens":%d,"output_tokens":%d,"cache_read_input_tokens":%d,"cache_creation_input_tokens":%d}}}`,
		in, out, read, create)
}

// sayWithUsage is an utterance that also carries token counts.
func sayWithUsage(text string, in, out, read, create int) string {
	return fmt.Sprintf(`{"type":"assistant","message":{"content":[{"type":"text","text":%q}],`+
		`"usage":{"input_tokens":%d,"output_tokens":%d,"cache_read_input_tokens":%d,"cache_creation_input_tokens":%d}}}`,
		text, in, out, read, create)
}

// tailToolResult is the user-typed row claude writes for a tool's output. It is NOT a person's
// turn, and treating it as one would split every reply at its first tool call.
const tailToolResult = `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t","content":"ok"}]}}`

// humanTurn is a person's actual prompt — the row that ends the reply before it.
const humanTurn = `{"type":"user","message":{"content":"次をお願いします"}}`

// TestLastSayReadsTheNewestUtterance pins what the overview card shows (ADR 0078 decision
// 12): the last thing the AGENT said, not the last thing that happened.
func TestLastSayReadsTheNewestUtterance(t *testing.T) {
	const sid = "bbbb2222-3333-5ccc-8ddd-444455556666"

	write := func(t *testing.T, lines ...string) string {
		t.Helper()
		resetTailCache()
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
		if got := saidBy(sid); got != "新しい答え" {
			t.Errorf("LastSay = %q, want the newest utterance", got)
		}
	})

	t.Run("walks past tool calls and the user's own words", func(t *testing.T) {
		write(t, say("直前に言ったこと", ""), toolOnly, userTurn)
		if got := saidBy(sid); got != "直前に言ったこと" {
			t.Errorf("LastSay = %q — a tool trace and a user prompt are not the agent speaking", got)
		}
	})

	t.Run("a subagent's turn is not this session speaking", func(t *testing.T) {
		write(t, say("親が言ったこと", ""), say("子が言ったこと", `,"isSidechain":true`))
		if got := saidBy(sid); got != "親が言ったこと" {
			t.Errorf("LastSay = %q, want the main conversation's line", got)
		}
	})

	t.Run("an API-error record is not an answer", func(t *testing.T) {
		// errors.go's measured shape: the text is the CLI-facing "Please run /login".
		bad := `{"type":"assistant","isApiErrorMessage":true,"apiErrorStatus":401,` +
			`"message":{"model":"<synthetic>","content":[{"type":"text","text":"Please run /login · API Error: 401"}]}}`
		write(t, say("落ちる前の答え", ""), bad)
		if got := saidBy(sid); got != "落ちる前の答え" {
			t.Errorf("LastSay = %q — a failed turn must not read as something the agent said", got)
		}
	})

	t.Run("a meta line is not an answer", func(t *testing.T) {
		write(t, say("本当の答え", ""), say("差し込まれた行", `,"isMeta":true`))
		if got := saidBy(sid); got != "本当の答え" {
			t.Errorf("LastSay = %q, want the real utterance", got)
		}
	})

	t.Run("nothing said yet", func(t *testing.T) {
		write(t, toolOnly, userTurn)
		if got := saidBy(sid); got != "" {
			t.Errorf("LastSay = %q, want \"\" — an invented line is worse than a blank card", got)
		}
	})

	t.Run("no transcript at all", func(t *testing.T) {
		resetTailCache()
		t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
		if got := saidBy(sid); got != "" {
			t.Errorf("LastSay = %q for a session with no log", got)
		}
	})

	t.Run("a stub sibling is skipped for the log that holds the conversation", func(t *testing.T) {
		resetTailCache()
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
		if got := saidBy(sid); got != "本物の会話" {
			t.Errorf("LastSay = %q, want the sibling that actually holds the conversation", got)
		}
	})

	t.Run("an unchanged transcript is not re-read", func(t *testing.T) {
		p := write(t, say("最初の答え", ""))
		if got := saidBy(sid); got != "最初の答え" {
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
		if got := saidBy(sid); got != "最初の答え" {
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
		if got := saidBy(sid); got != "" {
			t.Errorf("LastSay = %q — the whole file must not be read to find it", got)
		}
	})

	// …and when the window holds nothing, what was already known is KEPT. Blanking the
	// card halfway through a long turn would read as "this session went quiet".
	t.Run("keeps the known line when the window holds no utterance", func(t *testing.T) {
		p := write(t, say("長いターンの前に言ったこと", ""))
		if got := saidBy(sid); got != "長いターンの前に言ったこと" {
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
		if got := saidBy(sid); got != "長いターンの前に言ったこと" {
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

// TestTokenSpendsFoldsOneReplyIntoOnePoint pins the trend the overview card draws (ADR 0078
// decision 13). The arithmetic has to match the Console's spendOf + groupTurns exactly, or
// one session's card and its chat would show different trends for the same conversation.
//
// Against scanTail rather than TailFacts: this is about the arithmetic, and TailFacts adds a
// policy on top (a series of one point is not a trend, so it does not replace what is known).
func TestTokenSpendsFoldsOneReplyIntoOnePoint(t *testing.T) {
	write := func(t *testing.T, lines ...string) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "t.jsonl")
		if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	spendsIn := func(t *testing.T, lines ...string) []int {
		t.Helper()
		_, sp, _ := scanTail(write(t, lines...))
		return sp
	}
	eq := func(t *testing.T, got, want []int) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("spends = %v, want %v", got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("spends = %v, want %v", got, want)
			}
		}
	}

	t.Run("a reply's rows are ONE point: output sums, the prompt is the last row's", func(t *testing.T) {
		// One reply: a tool call, its result, then the words. Every row re-states the whole
		// prompt, so summing the inputs would count the same context once per tool call.
		got := spendsIn(t,
			usage(100, 20, 5000, 300),
			tailToolResult,
			sayWithUsage("できました", 150, 40, 6000, 10),
		)
		eq(t, got, []int{150 + 10 + 60}) // in 150 + create 10 + out (20+40)
	})

	t.Run("a person's turn ends a reply; a tool result does not", func(t *testing.T) {
		got := spendsIn(t,
			sayWithUsage("1 つ目", 10, 1, 0, 0),
			humanTurn,
			sayWithUsage("2 つ目", 20, 2, 0, 0),
			tailToolResult,
			usage(30, 3, 0, 0),
		)
		eq(t, got, []int{11, 35}) // the second reply is one point: in 30 + out (2+3)
	})

	t.Run("cache READS are not spend", func(t *testing.T) {
		eq(t, spendsIn(t, sayWithUsage("キャッシュだけ読んだ", 0, 7, 900000, 0)), []int{7})
	})

	t.Run("a subagent's reply is not this session's spend", func(t *testing.T) {
		got := spendsIn(t,
			sayWithUsage("親の答え", 10, 1, 0, 0),
			`{"type":"assistant","isSidechain":true,"message":{"content":[{"type":"text","text":"子"}],"usage":{"input_tokens":9999,"output_tokens":9999}}}`,
		)
		eq(t, got, []int{11})
	})

	t.Run("a reply that recorded no tokens is not a zero-token point", func(t *testing.T) {
		got := spendsIn(t, say("記録の無い返答", ""), humanTurn, sayWithUsage("ある返答", 10, 1, 0, 0))
		eq(t, got, []int{11})
	})

	t.Run("keeps only the newest tokenSpendMax replies", func(t *testing.T) {
		var lines []string
		for i := 1; i <= tokenSpendMax+10; i++ {
			lines = append(lines, sayWithUsage("x", i, 0, 0, 0), humanTurn)
		}
		got := spendsIn(t, lines...)
		if len(got) != tokenSpendMax {
			t.Fatalf("len(spends) = %d, want %d", len(got), tokenSpendMax)
		}
		if got[len(got)-1] != tokenSpendMax+10 {
			t.Errorf("last spend = %d, want the newest reply (%d)", got[len(got)-1], tokenSpendMax+10)
		}
	})

	t.Run("the reply the window cut in half is dropped, not drawn short", func(t *testing.T) {
		// A huge first reply straddles the window boundary, so only its tail survives —
		// which would read as a tiny turn next to whole ones.
		lines := []string{sayWithUsage("切られる返答", 111, 111, 0, 0)}
		filler := `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t","content":"` +
			strings.Repeat("x", 4096) + `"}]}}`
		for i := 0; i < (transcriptTailWindow/4096)+2; i++ {
			lines = append(lines, filler)
		}
		lines = append(lines, usage(222, 222, 0, 0), humanTurn, usage(333, 333, 0, 0), humanTurn)
		eq(t, spendsIn(t, lines...), []int{666})
	})
}

// TestTailFactsKeepsAKnownTrend pins the memo's policy: one point is not a trend, and the
// Console needs two to draw anything, so a window that yields fewer must not blank what the
// card is already showing.
func TestTailFactsKeepsAKnownTrend(t *testing.T) {
	const sid = "dddd4444-5555-5eee-8fff-666677778888"
	root := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", root)
	dir := filepath.Join(root, "projects", "proj-a")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, sid+".jsonl")
	put := func(lines ...string) {
		t.Helper()
		resetTailCache()
		if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	put(sayWithUsage("1 つ目", 10, 1, 0, 0), humanTurn, sayWithUsage("2 つ目", 20, 2, 0, 0))
	if got := spentBy(sid); len(got) != 2 {
		t.Fatalf("spends = %v, want two points", got)
	}
	// Same session, a window that now holds a single reply: the memo keeps the trend.
	if err := os.WriteFile(p, []byte(sayWithUsage("1 つだけ", 5, 5, 0, 0)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := spentBy(sid); len(got) != 2 || got[1] != 22 {
		t.Errorf("spends = %v, want the two points already known", got)
	}
	// The utterance, which IS drawable on its own, still follows the file.
	if got := saidBy(sid); got != "1 つだけ" {
		t.Errorf("LastSay = %q, want the newest utterance", got)
	}
}
