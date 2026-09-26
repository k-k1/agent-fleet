package lcpp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/harness"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// testHome redirects paths.HomeDir (and so sessionsDir) at a scratch directory for the
// duration of one test — the same idiom the rest of this module's tests use for paths.* seams
// (e.g. internal/userinstr/userinstr_test.go), since paths.AgentStateDir offers no injectable
// base function of its own.
func testHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

// fakeClient is a minimal harness.Client double: Send always answers with a fixed summary
// (the only Send this file's tests ever trigger is Compact's own summarization turn), and
// InputTokens is unused by Compact directly so it just returns a constant.
type fakeClient struct{ summary string }

func (c fakeClient) Send(context.Context, []harness.Message, []harness.ToolDef) (harness.Turn, error) {
	return harness.Turn{Content: c.summary}, nil
}
func (c fakeClient) InputTokens(context.Context, []harness.Message, []harness.ToolDef) (int, error) {
	return 0, nil
}

func TestOpenPathLayoutAndLazyCreation(t *testing.T) {
	home := testHome(t)
	s := Open("sid-1")
	want := filepath.Join(home, ".local", "state", "agent-fleet", "lcpp", "sessions", "sid-1.jsonl")
	if s.Path() != want {
		t.Fatalf("Path() = %q, want %q", s.Path(), want)
	}
	if _, err := os.Stat(s.Path()); !os.IsNotExist(err) {
		t.Fatalf("Open must not touch disk before the first Append; stat err = %v", err)
	}
	if recs, truncated, err := s.Records(); err != nil || recs != nil || truncated {
		t.Fatalf("Records() on a never-written session = %v, %v, %v; want nil, false, nil", recs, truncated, err)
	}
}

func TestAppendAndRecordsRoundTrip(t *testing.T) {
	testHome(t)
	s := Open("sid-2")

	if _, err := s.AppendUser("hello"); err != nil {
		t.Fatalf("AppendUser: %v", err)
	}
	toolCalls := []harness.ToolCall{{ID: "call-1", Name: "read", Arguments: `{"path":"a.go"}`}}
	if _, err := s.AppendMessage(harness.Message{
		Role: harness.RoleAssistant, Content: "reading the file", Reasoning: "should check a.go first", ToolCalls: toolCalls,
	}); err != nil {
		t.Fatalf("AppendMessage(assistant): %v", err)
	}
	if _, err := s.AppendMessage(harness.Message{Role: harness.RoleTool, Content: "package main\n", ToolCallID: "call-1"}); err != nil {
		t.Fatalf("AppendMessage(tool): %v", err)
	}
	if _, err := s.AppendModelChangeNote("qwen3-30b"); err != nil {
		t.Fatalf("AppendModelChangeNote: %v", err)
	}
	if _, err := s.AppendUsage(harness.Usage{PromptTokens: 42, CompletionTokens: 7}, 0); err != nil {
		t.Fatalf("AppendUsage: %v", err)
	}

	recs, _, err := s.Records()
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	if len(recs) != 5 {
		t.Fatalf("Records() len = %d, want 5: %+v", len(recs), recs)
	}
	kinds := make([]Kind, len(recs))
	for i, r := range recs {
		kinds[i] = r.Kind
	}
	wantKinds := []Kind{KindUser, KindAssistant, KindToolResult, KindSystemNote, KindUsage}
	if !reflect.DeepEqual(kinds, wantKinds) {
		t.Fatalf("kinds = %v, want %v", kinds, wantKinds)
	}
	if recs[1].Reasoning != "should check a.go first" || len(recs[1].ToolCalls) != 1 || recs[1].ToolCalls[0].ID != "call-1" {
		t.Fatalf("assistant record lost its reasoning/toolCalls: %+v", recs[1])
	}
	if recs[2].ToolCallID != "call-1" || recs[2].Content != "package main\n" {
		t.Fatalf("tool_result record wrong: %+v", recs[2])
	}
	if recs[3].Note != NoteModelChange || recs[3].Model != "qwen3-30b" {
		t.Fatalf("model-change note wrong: %+v", recs[3])
	}
	if recs[4].Usage == nil || recs[4].Usage.PromptTokens != 42 || recs[4].Usage.CompletionTokens != 7 {
		t.Fatalf("usage record wrong: %+v", recs[4])
	}
	// Every record gets a distinct, non-empty id (the fork/mirror anchor decision 3 asks for).
	seen := map[string]bool{}
	for _, r := range recs {
		if r.ID == "" || seen[r.ID] {
			t.Fatalf("record id %q is empty or duplicated across %+v", r.ID, recs)
		}
		seen[r.ID] = true
	}
}

// TestAppendIsMonotonic pins decision 3's core append-only rule directly: a compaction adds a
// record, it never removes or rewrites one that was already there — the same property the ADR
// records measuring live ("full は 29→30 エントリ"). Mutating append to truncate/rewrite past
// entries would turn this red.
func TestAppendIsMonotonic(t *testing.T) {
	testHome(t)
	s := Open("sid-3")
	for i := 0; i < 5; i++ {
		if _, err := s.AppendUser("turn"); err != nil {
			t.Fatalf("AppendUser %d: %v", i, err)
		}
	}
	before, _, err := s.Records()
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	if len(before) != 5 {
		t.Fatalf("len(before) = %d, want 5", len(before))
	}

	if _, err := s.AppendMessage(harness.Message{Role: harness.RoleSystem, Content: "[lcpp compaction summary]\nsummary text"}); err != nil {
		t.Fatalf("AppendMessage(compaction note): %v", err)
	}

	after, _, err := s.Records()
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	if len(after) != 6 {
		t.Fatalf("len(after) = %d, want 6 (5 preserved + 1 new boundary)", len(after))
	}
	for i := range before {
		if after[i].ID != before[i].ID || after[i].Content != before[i].Content {
			t.Fatalf("record %d changed after compaction: before=%+v after=%+v", i, before[i], after[i])
		}
	}
	if after[5].Kind != KindSystemNote || after[5].Note != NoteCompaction {
		t.Fatalf("appended record is not a compaction note: %+v", after[5])
	}
}

func TestFullReconstructsMessagesAndDropsBookkeeping(t *testing.T) {
	testHome(t)
	s := Open("sid-4")
	mustAppend := func(k string, r Record) {
		t.Helper()
		var err error
		switch k {
		case "user":
			_, err = s.AppendUser(r.Content)
		default:
			_, err = s.append(r)
		}
		if err != nil {
			t.Fatalf("append %s: %v", k, err)
		}
	}
	mustAppend("user", Record{Content: "hi"})
	mustAppend("assistant", Record{Kind: KindAssistant, Content: "hello", Reasoning: "greet back",
		ToolCalls: []harness.ToolCall{{ID: "c1", Name: "noop"}}})
	mustAppend("tool", Record{Kind: KindToolResult, Content: "ok", ToolCallID: "c1"})
	mustAppend("compaction", Record{Kind: KindSystemNote, Note: NoteCompaction, Content: "[lcpp compaction summary]\nsummary"})
	mustAppend("model-change", Record{Kind: KindSystemNote, Note: NoteModelChange, Model: "other-model"})
	mustAppend("usage", Record{Kind: KindUsage, Usage: &harness.Usage{PromptTokens: 1, CompletionTokens: 1}})
	mustAppend("continuation", Record{Kind: KindContinuation, Content: "Continue with the task."})

	full, err := s.Full()
	if err != nil {
		t.Fatalf("Full: %v", err)
	}
	want := []harness.Message{
		{Role: harness.RoleUser, Content: "hi"},
		{Role: harness.RoleAssistant, Content: "hello", Reasoning: "greet back", ToolCalls: []harness.ToolCall{{ID: "c1", Name: "noop"}}},
		{Role: harness.RoleTool, Content: "ok", ToolCallID: "c1"},
		{Role: harness.RoleSystem, Content: "[lcpp compaction summary]\nsummary"},
		// model-change note and usage contribute nothing.
		{Role: harness.RoleUser, Content: "Continue with the task."}, // continuation IS replayed to the engine
	}
	if !reflect.DeepEqual(full, want) {
		t.Fatalf("Full() =\n%+v\nwant\n%+v", full, want)
	}
}

// TestSendMessagesAcrossCompactionBoundary builds the file order a real driver would produce
// around a decision-7 compaction (harness.Compact re-used verbatim, not re-implemented) and
// checks representation 1's three invariants compact.go's own doc comments call out as the
// specific chat-template rejections seen live: exactly one leading system message, the
// request ends on a real user turn when one is pending, and no carried-over reasoning rides
// the wire (present in Full, stripped from SendMessages).
func TestSendMessagesAcrossCompactionBoundary(t *testing.T) {
	testHome(t)
	s := Open("sid-5")

	if _, err := s.AppendUser("hi"); err != nil {
		t.Fatalf("AppendUser: %v", err)
	}
	if _, err := s.AppendMessage(harness.Message{Role: harness.RoleAssistant, Content: "reply1", Reasoning: "think1"}); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	before, err := s.Full()
	if err != nil {
		t.Fatalf("Full: %v", err)
	}
	// The driver's own in-memory shape right before calling PrepareTurn/Compact: history so
	// far plus the new question that triggered the budget check (Compact's own doc comment:
	// "PrepareTurn's usual calling shape").
	pending := append(append([]harness.Message(nil), before...), harness.Message{Role: harness.RoleUser, Content: "question2"})
	compacted, err := harness.Compact(context.Background(), fakeClient{summary: "the recap"}, "sys prompt", pending)
	if err != nil {
		t.Fatalf("harness.Compact: %v", err)
	}
	if len(compacted) != 4 || compacted[2].Role != harness.RoleSystem || compacted[3].Content != "question2" {
		t.Fatalf("harness.Compact produced an unexpected shape: %+v", compacted)
	}

	// Persist the delta in the LOGICAL order Compact returned (boundary before the pending
	// question), not the real-world order the question arrived in — store.go itself imposes
	// no ordering; getting this right is the driver's job, exercised here directly.
	if _, err := s.AppendMessage(compacted[2]); err != nil { // the new compaction boundary
		t.Fatalf("AppendMessage(boundary): %v", err)
	}
	if _, err := s.AppendUser("question2"); err != nil { // genuine top-level input, not AppendMessage
		t.Fatalf("AppendUser(question2): %v", err)
	}

	send, err := s.SendMessages("sys prompt")
	if err != nil {
		t.Fatalf("SendMessages: %v", err)
	}
	sysCount := 0
	for i, m := range send {
		if m.Role == harness.RoleSystem {
			sysCount++
			if i != 0 {
				t.Fatalf("system message at index %d, want only index 0: %+v", i, send)
			}
		}
	}
	if sysCount != 1 {
		t.Fatalf("sysCount = %d, want exactly 1: %+v", sysCount, send)
	}
	last := send[len(send)-1]
	if last.Role != harness.RoleUser || last.Content != "question2" {
		t.Fatalf("send must end on the pending user turn, got %+v", last)
	}

	// Now answer question2, carrying reasoning, and confirm Full keeps it while a fresh
	// SendMessages strips it from what actually rides the wire.
	if _, err := s.AppendMessage(harness.Message{Role: harness.RoleAssistant, Content: "reply2", Reasoning: "think2"}); err != nil {
		t.Fatalf("AppendMessage(reply2): %v", err)
	}
	full, err := s.Full()
	if err != nil {
		t.Fatalf("Full: %v", err)
	}
	if got := full[len(full)-1]; got.Reasoning != "think2" {
		t.Fatalf("Full() dropped reasoning it must keep: %+v", got)
	}
	send2, err := s.SendMessages("sys prompt")
	if err != nil {
		t.Fatalf("SendMessages: %v", err)
	}
	if got := send2[len(send2)-1]; got.Reasoning != "" {
		t.Fatalf("SendMessages() must strip reasoning from every carried-over message, got %+v", got)
	}
}

// TestTranscriptDropsContinuationOnly is this file's positive/negative control pair for the
// synthetic continuation turn (loop.go's continuationPrompt / harness.IsContinuationPrompt).
//
// Positive: a message that arrives through AppendMessage AND satisfies
// harness.IsContinuationPrompt is recorded as KindContinuation and never becomes a
// transcript.Turn.
//
// Negative: "a real user happens to type the exact same sentence" (the case this package's
// AppendMessage doc comment calls out) is exercised by routing THAT text through AppendUser —
// the call a driver only ever makes for genuine top-level input, never for something
// harness.Run produced. It comes back Kind==KindUser and IS rendered, proving the distinction
// is carried by which method was called (provenance), not by sniffing Content (which is
// identical in both cases here on purpose).
func TestTranscriptDropsContinuationOnly(t *testing.T) {
	testHome(t)
	s := Open("sid-6")
	const text = "Continue with the task." // harness's own continuationPrompt wording, deliberately hardcoded here (this test's whole point is that content alone must not decide the outcome)

	synthetic, err := s.AppendMessage(harness.Message{Role: harness.RoleUser, Content: text})
	if err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	if synthetic.Kind != KindContinuation {
		t.Fatalf("AppendMessage(continuation-shaped message).Kind = %q, want %q", synthetic.Kind, KindContinuation)
	}

	genuine, err := s.AppendUser(text)
	if err != nil {
		t.Fatalf("AppendUser: %v", err)
	}
	if genuine.Kind != KindUser {
		t.Fatalf("AppendUser(same text).Kind = %q, want %q — negative control failed", genuine.Kind, KindUser)
	}

	turns, err := s.Transcript()
	if err != nil {
		t.Fatalf("Transcript: %v", err)
	}
	var anchors []string
	for _, tn := range turns {
		anchors = append(anchors, tn.AnchorID)
	}
	if contains(anchors, synthetic.ID) {
		t.Fatalf("Transcript() rendered the synthetic continuation turn (anchor %s): %+v", synthetic.ID, turns)
	}
	if !contains(anchors, genuine.ID) {
		t.Fatalf("Transcript() dropped the genuine user turn (anchor %s) — negative control failed: %+v", genuine.ID, turns)
	}
	if len(turns) != 1 {
		t.Fatalf("Transcript() len = %d, want exactly 1 (the genuine turn only): %+v", len(turns), turns)
	}

	// Full, by contrast, must keep BOTH: the continuation turn is dropped only from the
	// mirror, never from what the engine replays (decision 3: "送信用 messages には必要).
	full, err := s.Full()
	if err != nil {
		t.Fatalf("Full: %v", err)
	}
	if len(full) != 2 || full[0].Content != text || full[1].Content != text {
		t.Fatalf("Full() = %+v, want both occurrences of %q", full, text)
	}
}

func contains(ss []string, v string) bool {
	for _, s := range ss {
		if s == v {
			return true
		}
	}
	return false
}

// TestTranscriptMergesToolResultIntoTheCallingTurn checks representation 2's tool-call/result
// pairing: the two records are written on separate lines (an assistant record naming the
// call, a later tool_result record answering it), but the mirror must show ONE part carrying
// both the call and its output, matching every other kind's transcript.Turn convention.
func TestTranscriptMergesToolResultIntoTheCallingTurn(t *testing.T) {
	testHome(t)
	s := Open("sid-7")
	if _, err := s.AppendMessage(harness.Message{
		Role: harness.RoleAssistant, Content: "checking",
		ToolCalls: []harness.ToolCall{{ID: "call-9", Name: "bash", Arguments: `{"command":"ls"}`}},
	}); err != nil {
		t.Fatalf("AppendMessage(assistant): %v", err)
	}
	if _, err := s.AppendMessage(harness.Message{Role: harness.RoleTool, Content: "a.go\nb.go\n", ToolCallID: "call-9"}); err != nil {
		t.Fatalf("AppendMessage(tool): %v", err)
	}
	if _, err := s.AppendUsage(harness.Usage{PromptTokens: 10, CompletionTokens: 3}, 0); err != nil {
		t.Fatalf("AppendUsage: %v", err)
	}

	turns, err := s.Transcript()
	if err != nil {
		t.Fatalf("Transcript: %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("turns = %+v, want exactly 1 (the tool result must not become its own turn)", turns)
	}
	turn := turns[0]
	if turn.InTok != 10 || turn.OutTok != 3 {
		t.Fatalf("usage did not attach to the assistant turn: %+v", turn)
	}
	found := false
	for _, p := range turn.Parts {
		if p.Kind == "tool" && p.Tool == "bash" {
			found = true
			if p.Output != "a.go\nb.go" {
				t.Fatalf("tool part output = %q, want the tool_result content (trimmed)", p.Output)
			}
		}
	}
	if !found {
		t.Fatalf("no tool part found in %+v", turn.Parts)
	}
}

// TestTranscriptCarriesRecordedWindow pins ADR 0093 decision 8: this kind's own resolved
// context window (driver.go's harness.EngineWindow call, threaded through AppendUsage) must
// reach transcript.Turn.CtxWindow, because that is exactly the field session_usage.go's
// AggregateUsage and usage_fold.go's foldTurnRows key off to report WindowSource="recorded"
// instead of falling back to usagex.WindowGuess.
func TestTranscriptCarriesRecordedWindow(t *testing.T) {
	testHome(t)
	s := Open("sid-window")
	if _, err := s.AppendMessage(harness.Message{Role: harness.RoleAssistant, Content: "hi"}); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	if _, err := s.AppendUsage(harness.Usage{PromptTokens: 100, CompletionTokens: 20}, 8192); err != nil {
		t.Fatalf("AppendUsage: %v", err)
	}
	turns, err := s.Transcript()
	if err != nil {
		t.Fatalf("Transcript: %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("turns = %+v, want exactly 1", turns)
	}
	if turns[0].CtxWindow != 8192 {
		t.Fatalf("CtxWindow = %d, want 8192 (the window AppendUsage was given)", turns[0].CtxWindow)
	}
	u, window, ok := s.LastUsage()
	if !ok {
		t.Fatal("LastUsage: ok = false after a completed turn")
	}
	if u.PromptTokens != 100 || u.CompletionTokens != 20 || window != 8192 {
		t.Fatalf("LastUsage = %+v, window %d, want {100 20}, 8192", u, window)
	}
}

// TestTranscriptOmitsUnresolvedWindow is the negative control: a turn whose window could not
// be resolved (driver.go passes 0 when harness.EngineWindow answers nothing) must leave
// CtxWindow at 0, not a fabricated value — that is what lets the generic reader fall back to
// WindowSource="estimated" instead of wrongly claiming "recorded".
func TestTranscriptOmitsUnresolvedWindow(t *testing.T) {
	testHome(t)
	s := Open("sid-window-unresolved")
	if _, err := s.AppendMessage(harness.Message{Role: harness.RoleAssistant, Content: "hi"}); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	if _, err := s.AppendUsage(harness.Usage{PromptTokens: 100, CompletionTokens: 20}, 0); err != nil {
		t.Fatalf("AppendUsage: %v", err)
	}
	turns, err := s.Transcript()
	if err != nil {
		t.Fatalf("Transcript: %v", err)
	}
	if turns[0].CtxWindow != 0 {
		t.Fatalf("CtxWindow = %d, want 0 (unresolved window must not be fabricated)", turns[0].CtxWindow)
	}
}

func TestForkAt(t *testing.T) {
	testHome(t)
	s := Open("sid-src")
	var ids []string
	for i := 0; i < 4; i++ {
		r, err := s.AppendUser("turn")
		if err != nil {
			t.Fatalf("AppendUser %d: %v", i, err)
		}
		ids = append(ids, r.ID)
	}

	dst, err := s.ForkAt("sid-fork", ids[1])
	if err != nil {
		t.Fatalf("ForkAt: %v", err)
	}
	dstRecs, _, err := dst.Records()
	if err != nil {
		t.Fatalf("dst.Records: %v", err)
	}
	if len(dstRecs) != 2 {
		t.Fatalf("forked session has %d records, want 2 (up to and including the anchor)", len(dstRecs))
	}
	if dstRecs[0].ID != ids[0] || dstRecs[1].ID != ids[1] {
		t.Fatalf("forked ids = [%s %s], want [%s %s]", dstRecs[0].ID, dstRecs[1].ID, ids[0], ids[1])
	}

	// The source is untouched.
	srcRecs, _, err := s.Records()
	if err != nil {
		t.Fatalf("s.Records: %v", err)
	}
	if len(srcRecs) != 4 {
		t.Fatalf("source session mutated by ForkAt: %d records, want 4", len(srcRecs))
	}

	if _, err := s.ForkAt("sid-fork", ids[2]); err == nil {
		t.Fatal("ForkAt into an already-used sid must fail, not overwrite it")
	}
	if _, err := s.ForkAt("sid-fork-2", "no-such-anchor"); err == nil {
		t.Fatal("ForkAt with an unknown anchor must fail")
	}
}

// TestAppendConcurrentNoTornLines is the "read-modify-write の禁止" acceptance point: several
// goroutines append at once (harness's own runToolCalls dispatches one turn's tool calls
// exactly this way), and every line in the resulting file must still decode — no line
// interleaved with another, no truncated line if a write raced a read.
func TestAppendConcurrentNoTornLines(t *testing.T) {
	testHome(t)
	s := Open("sid-8")
	const n = 200
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := s.AppendMessage(harness.Message{
				Role: harness.RoleTool, Content: strings.Repeat("x", 500), ToolCallID: "c",
			}); err != nil {
				t.Errorf("append %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	raw, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) != n {
		t.Fatalf("got %d lines, want %d", len(lines), n)
	}
	for i, line := range lines {
		var rec Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("line %d does not decode (torn write?): %v\nline: %s", i, err, line)
		}
	}
}

// TestRecordsTruncatedFinalLineIsTolerated is Records' own positive control: a torn LAST
// line (what a process death mid-write, per append's own doc comment, can leave behind) must
// not cost the whole session's history — only that one incomplete line is dropped.
func TestRecordsTruncatedFinalLineIsTolerated(t *testing.T) {
	testHome(t)
	s := Open("sid-trunc")
	var ids []string
	for i := 0; i < 3; i++ {
		r, err := s.AppendUser(fmt.Sprintf("turn-%d", i))
		if err != nil {
			t.Fatalf("AppendUser %d: %v", i, err)
		}
		ids = append(ids, r.ID)
	}

	// Append a partial line directly — no closing quote/brace, no trailing newline — the
	// shape a single interrupted os.File.Write call leaves behind.
	f, err := os.OpenFile(s.Path(), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	if _, err := f.WriteString(`{"id":"torn","ts":"2026-01-01T00:00:00Z","kind":"user","content":"cut off mid-str`); err != nil {
		t.Fatalf("WriteString: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	recs, truncated, err := s.Records()
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	if !truncated {
		t.Fatal("truncated = false, want true — a torn last line must be reported, not silently indistinguishable from a clean file")
	}
	if len(recs) != 3 {
		t.Fatalf("recs = %+v, want exactly the 3 complete records before the torn line", recs)
	}
	for i, r := range recs {
		if r.ID != ids[i] {
			t.Fatalf("recs[%d].ID = %q, want %q", i, r.ID, ids[i])
		}
	}
}

// TestRecordsMidStreamCorruptionStillErrors is the same fix's negative control: corruption
// anywhere but the last line means the append-only invariant itself broke (not a tolerated
// in-flight write), and Records must keep erroring on that rather than silently swallowing it
// too.
func TestRecordsMidStreamCorruptionStillErrors(t *testing.T) {
	testHome(t)
	s := Open("sid-corrupt")
	for i := 0; i < 3; i++ {
		if _, err := s.AppendUser(fmt.Sprintf("turn-%d", i)); err != nil {
			t.Fatalf("AppendUser %d: %v", i, err)
		}
	}
	raw, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("setup: got %d lines, want 3", len(lines))
	}
	lines[1] = "{not valid json" // the middle line — never what a torn LAST write could produce
	if err := os.WriteFile(s.Path(), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	recs, truncated, err := s.Records()
	if err == nil {
		t.Fatal("Records: want an error for mid-stream corruption, got nil")
	}
	if truncated {
		t.Fatal("truncated = true, want false — this is a mid-stream corruption, not a tolerated last-line tear")
	}
	if len(recs) != 1 {
		t.Fatalf("recs = %+v, want exactly the 1 record before the corrupted line", recs)
	}
}

// TestCloseThenAppendReopens pins Close's own contract: it is optional cleanup, not a
// one-way valve — a Store that keeps being used after Close must transparently reopen its
// cached write handle rather than erroring or silently going nowhere.
func TestCloseThenAppendReopens(t *testing.T) {
	testHome(t)
	s := Open("sid-close")
	if _, err := s.AppendUser("one"); err != nil {
		t.Fatalf("AppendUser: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := s.Close(); err != nil { // idempotent: a second Close on an already-closed store
		t.Fatalf("second Close: %v", err)
	}
	if _, err := s.AppendUser("two"); err != nil {
		t.Fatalf("AppendUser after Close: %v", err)
	}
	recs, _, err := s.Records()
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	if len(recs) != 2 || recs[0].Content != "one" || recs[1].Content != "two" {
		t.Fatalf("recs = %+v, want two records surviving Close+reopen", recs)
	}
}

func TestAppendMessageUnknownRole(t *testing.T) {
	testHome(t)
	s := Open("sid-9")
	if _, err := s.AppendMessage(harness.Message{Role: harness.Role("bogus")}); err == nil {
		t.Fatal("AppendMessage with an unrecognized role must error, not silently drop it")
	}
}

// TestTranscriptToolPartsCarryTheirEdits pins what Transcript() puts on an edit-family tool
// part. Only File+Edits make the mirror open the trace as a diff and make the changed-files
// strip count the call at all (transcript.FileEditsInTurn skips a tool part with no File);
// every other tool stays the one-line trace it was, which is the negative control that keeps
// the strip from listing files nobody edited.
func TestTranscriptToolPartsCarryTheirEdits(t *testing.T) {
	testHome(t)
	s := Open("sid-edits")
	if _, err := s.AppendMessage(harness.Message{
		Role: harness.RoleAssistant, Content: "editing",
		ToolCalls: []harness.ToolCall{
			{ID: "c1", Name: "write", Arguments: `{"path":"new.txt","content":"one\ntwo\n"}`},
			{ID: "c2", Name: "edit", Arguments: `{"path":"a.go","old_string":"old\n","new_string":"new\n"}`},
			{ID: "c3", Name: "read", Arguments: `{"path":"b.go"}`},
		},
	}); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	turns, err := s.Transcript()
	if err != nil {
		t.Fatalf("Transcript: %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("Transcript() len = %d, want 1: %+v", len(turns), turns)
	}
	byTool := map[string]transcript.Part{}
	for _, p := range turns[0].Parts {
		if p.Kind == "tool" {
			byTool[p.Tool] = p
		}
	}
	if len(byTool) != 3 {
		t.Fatalf("tool parts = %+v, want all three calls rendered", byTool)
	}
	if got := byTool["write"]; got.File != "new.txt" || len(got.Edits) != 1 || got.Edits[0].New != "one\ntwo\n" {
		t.Fatalf("write part = %+v, want new.txt with its content as the after-image", got)
	}
	if got := byTool["edit"]; got.File != "a.go" || len(got.Edits) != 1 || got.Edits[0].Old != "old\n" {
		t.Fatalf("edit part = %+v, want a.go with both sides", got)
	}
	if got := byTool["read"]; got.File != "" || got.Edits != nil {
		t.Fatalf("read part = %+v, want a plain trace — a read is not a change", got)
	}
}

// Each response is badged with the model that answered it. The meta only holds the current
// model, so it may label unrecorded responses only while no switch note exists.
func TestTranscriptModelPerTurn(t *testing.T) {
	testHome(t)
	answer := func(s *Store, model string) {
		t.Helper()
		if _, err := s.AppendUser("q"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.AppendMessageFrom(harness.Message{Role: harness.RoleAssistant, Content: "a"}, model); err != nil {
			t.Fatal(err)
		}
	}
	models := func(s *Store, sessionModel string) []string {
		t.Helper()
		turns, err := s.TranscriptFor(sessionModel)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, tn := range turns {
			if tn.Role == "assistant" {
				out = append(out, tn.Model)
			}
		}
		return out
	}

	never := Open("sid-model-never-switched")
	answer(never, "")
	answer(never, "A")
	if got := models(never, "A"); !slices.Equal(got, []string{"A", "A"}) {
		t.Fatalf("no switch: got %q", got)
	}

	switched := Open("sid-model-switched")
	answer(switched, "") // written before models were recorded, launched on an unknown model
	if _, err := switched.AppendModelChangeNote("B"); err != nil {
		t.Fatal(err)
	}
	answer(switched, "")
	answer(switched, "C")
	if got := models(switched, "C"); !slices.Equal(got, []string{"", "B", "C"}) {
		t.Fatalf("after a switch: got %q, want [\"\" B C]", got)
	}
}
