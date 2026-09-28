package lcpp

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/harness"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

func cachedEntry(t *testing.T, s *Store) *recordEntry {
	t.Helper()
	v, ok := recordCache.Load(s.Path())
	if !ok {
		t.Fatalf("no cache entry for %s", s.Path())
	}
	return v.(*recordEntry)
}

func appendUsers(t *testing.T, s *Store, from, n int) {
	t.Helper()
	for i := from; i < from+n; i++ {
		if _, err := s.AppendUser(fmt.Sprintf("turn-%d", i)); err != nil {
			t.Fatalf("AppendUser %d: %v", i, err)
		}
	}
}

// wantContents checks both of a read's products against the user turns the log should hold.
func wantContents(t *testing.T, s *Store, want ...string) {
	t.Helper()
	recs, truncated, err := s.Records()
	if err != nil || truncated {
		t.Fatalf("Records: truncated=%v err=%v", truncated, err)
	}
	if len(recs) != len(want) {
		t.Fatalf("Records = %d records, want %d", len(recs), len(want))
	}
	for i, r := range recs {
		if r.Content != want[i] {
			t.Fatalf("recs[%d].Content = %q, want %q", i, r.Content, want[i])
		}
	}
	// The transcript is cached beside the records and must follow them through a reset.
	turns, err := s.TranscriptFor("")
	if err != nil || len(turns) != len(want) {
		t.Fatalf("TranscriptFor = %d turns, err %v; want %d", len(turns), err, len(want))
	}
	for i, tr := range turns {
		if tr.Text != want[i] {
			t.Fatalf("turns[%d].Text = %q, want %q", i, tr.Text, want[i])
		}
	}
}

func appendRaw(t *testing.T, s *Store, raw string) {
	t.Helper()
	f, err := os.OpenFile(s.Path(), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(raw); err != nil {
		t.Fatal(err)
	}
}

// TestRecordsDecodesOnlyWhatWasAppended is #954's acceptance: a read after an append decodes
// the new lines alone, and a read with nothing new decodes none.
func TestRecordsDecodesOnlyWhatWasAppended(t *testing.T) {
	testHome(t)
	s := Open("sid-incr")
	appendUsers(t, s, 0, 5)
	wantContents(t, s, "turn-0", "turn-1", "turn-2", "turn-3", "turn-4")
	e := cachedEntry(t, s)
	if e.decoded != 5 {
		t.Fatalf("first read decoded %d lines, want 5", e.decoded)
	}

	// A fresh Open, as every poll does, still hits the same entry.
	wantContents(t, Open("sid-incr"), "turn-0", "turn-1", "turn-2", "turn-3", "turn-4")
	if e.decoded != 5 {
		t.Fatalf("a read with nothing appended decoded %d lines in total, want still 5", e.decoded)
	}

	appendUsers(t, s, 5, 2)
	wantContents(t, s, "turn-0", "turn-1", "turn-2", "turn-3", "turn-4", "turn-5", "turn-6")
	if e.decoded != 7 {
		t.Fatalf("a read after 2 appends decoded %d lines in total, want 7 (5 + only the 2 new)", e.decoded)
	}
}

// TestRecordsCopyDoesNotReachTheCache: Records hands out its own slice, so a caller writing to
// it cannot change what the next poll sees.
func TestRecordsCopyDoesNotReachTheCache(t *testing.T) {
	testHome(t)
	s := Open("sid-copy")
	appendUsers(t, s, 0, 2)
	recs, _, err := s.Records()
	if err != nil {
		t.Fatal(err)
	}
	recs[0].Content = "scribbled"
	_ = append(recs[:1], Record{Content: "appended"})
	wantContents(t, s, "turn-0", "turn-1")
}

// TestRecordsRereadsARecreatedLog: a session log deleted and written again on the same path is
// another file, whether it came back shorter, longer, or rewritten in place.
func TestRecordsRereadsARecreatedLog(t *testing.T) {
	testHome(t)

	t.Run("shorter", func(t *testing.T) {
		s := Open("sid-shorter")
		appendUsers(t, s, 0, 3)
		wantContents(t, s, "turn-0", "turn-1", "turn-2")
		s.Close()
		if err := os.Remove(s.Path()); err != nil {
			t.Fatal(err)
		}
		s2 := Open("sid-shorter")
		defer s2.Close()
		if _, err := s2.AppendUser("new"); err != nil {
			t.Fatal(err)
		}
		wantContents(t, s2, "new")
	})

	t.Run("longer", func(t *testing.T) {
		s := Open("sid-longer")
		appendUsers(t, s, 0, 1)
		wantContents(t, s, "turn-0")
		s.Close()
		if err := os.Remove(s.Path()); err != nil {
			t.Fatal(err)
		}
		s2 := Open("sid-longer")
		defer s2.Close()
		appendUsers(t, s2, 10, 3)
		wantContents(t, s2, "turn-10", "turn-11", "turn-12")
	})

	// Same inode, grown: only the head check can tell this apart from an append.
	t.Run("rewritten in place", func(t *testing.T) {
		s := Open("sid-inplace")
		appendUsers(t, s, 0, 1)
		wantContents(t, s, "turn-0")
		s.Close()
		other := testRecordLines(t, "a", "b")
		if err := os.WriteFile(s.Path(), other, 0o600); err != nil {
			t.Fatal(err)
		}
		wantContents(t, s, "a", "b")
	})

	// Same size, another inode: the size alone says nothing was appended, so only the
	// identity check stops the old records being served. The replacement is renamed over the
	// path while the old file still exists, so the two cannot share an inode.
	t.Run("same size", func(t *testing.T) {
		s := Open("sid-samesize")
		if err := os.MkdirAll(s.dir, 0o700); err != nil {
			t.Fatal(err)
		}
		line := func(id, content string) []byte {
			return []byte(fmt.Sprintf(`{"id":%q,"ts":"t","kind":"user","content":%q}`+"\n", id, content))
		}
		if err := os.WriteFile(s.Path(), line("1", "aaaa"), 0o600); err != nil {
			t.Fatal(err)
		}
		wantContents(t, s, "aaaa")
		if err := os.WriteFile(s.Path()+".new", line("2", "bbbb"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(s.Path()+".new", s.Path()); err != nil {
			t.Fatal(err)
		}
		wantContents(t, s, "bbbb")
	})
}

// testRecordLines builds log lines in another scratch store, so their ids differ from any
// record already on the path under test.
func testRecordLines(t *testing.T, contents ...string) []byte {
	t.Helper()
	src := &Store{dir: t.TempDir(), sid: "src"}
	defer src.Close()
	for _, c := range contents {
		if _, err := src.AppendUser(c); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(src.Path())
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// TestRecordsTornTailIsNeverCached: a line without its newline is judged afresh on every read,
// so the cache holds nothing a full re-read would not.
func TestRecordsTornTailIsNeverCached(t *testing.T) {
	testHome(t)
	s := Open("sid-torn")
	defer s.Close()
	appendUsers(t, s, 0, 2)
	wantContents(t, s, "turn-0", "turn-1")
	e := cachedEntry(t, s)

	appendRaw(t, s, `{"id":"x","ts":"t","kind":"user","content":"hal`)
	recs, truncated, err := s.Records()
	if err != nil || !truncated || len(recs) != 2 {
		t.Fatalf("torn tail: %d records, truncated=%v, err=%v; want 2, true, nil", len(recs), truncated, err)
	}

	appendRaw(t, s, `f"}`) // decodes now, but has no newline yet
	wantContents(t, s, "turn-0", "turn-1", "half")
	if len(e.recs) != 2 {
		t.Fatalf("cache holds %d records, want 2 — a line without its newline must not be cached", len(e.recs))
	}

	appendRaw(t, s, "\n")
	wantContents(t, s, "turn-0", "turn-1", "half")
	if len(e.recs) != 3 {
		t.Fatalf("cache holds %d records, want 3 once the line is complete", len(e.recs))
	}
}

// TestRecordsBadLastLineErrorsOnceFollowed: a complete undecodable line reads as truncated
// while it is last, and as mid-stream corruption once a record lands after it — exactly what a
// full re-read of the same bytes would say at each point.
func TestRecordsBadLastLineErrorsOnceFollowed(t *testing.T) {
	testHome(t)
	s := Open("sid-bad")
	defer s.Close()
	appendUsers(t, s, 0, 2)
	wantContents(t, s, "turn-0", "turn-1")

	appendRaw(t, s, "{not json\n")
	recs, truncated, err := s.Records()
	if err != nil || !truncated || len(recs) != 2 {
		t.Fatalf("bad last line: %d records, truncated=%v, err=%v; want 2, true, nil", len(recs), truncated, err)
	}

	appendUsers(t, s, 2, 1)
	recs, truncated, err = s.Records()
	if err == nil || truncated || len(recs) != 2 {
		t.Fatalf("bad line then a record: %d records, truncated=%v, err=%v; want 2, false, an error", len(recs), truncated, err)
	}
}

// fullTranscript is the transcript a from-scratch read of the log gives, bypassing the cache.
func fullTranscript(t *testing.T, s *Store, sessionModel string) []transcript.Turn {
	t.Helper()
	raw, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	var recs []Record
	for _, ln := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var r Record
		if err := json.Unmarshal([]byte(ln), &r); err != nil {
			t.Fatal(err)
		}
		recs = append(recs, r)
	}
	return transcriptFromRecords(recs, sessionModel)
}

// TestTranscriptIncrementalMatchesFullBuild: after every append the cached transcript equals
// a from-scratch build, including what an append changes in turns already built (a tool
// result's output, a usage count, and the model label a later switch note takes away), and a
// transcript already handed out does not change under its holder.
func TestTranscriptIncrementalMatchesFullBuild(t *testing.T) {
	testHome(t)
	s := Open("sid-tb")
	defer s.Close()
	must := func(_ Record, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	steps := []func(){
		func() { must(s.AppendUser("q1")) },
		func() {
			must(s.AppendMessage(harness.Message{Role: harness.RoleAssistant, Content: "a1",
				ToolCalls: []harness.ToolCall{{ID: "c1", Name: "read", Arguments: `{"path":"a.go"}`}}}))
		},
		func() {
			must(s.AppendMessage(harness.Message{Role: harness.RoleTool, Content: "out1", ToolCallID: "c1"}))
		},
		func() { must(s.AppendUsage(harness.Usage{PromptTokens: 10, CompletionTokens: 2}, 4096)) },
		func() { must(s.AppendMessage(harness.Message{Role: harness.RoleAssistant, Content: "a2"})) },
		func() { must(s.AppendModelChangeNote("m2")) },
		func() { must(s.AppendUser("q2")) },
		func() { must(s.AppendMessage(harness.Message{Role: harness.RoleAssistant, Content: "a3"})) },
		func() { must(s.AppendTurnErrorNote("no engine")) },
		func() { must(s.AppendMessage(harness.Message{Role: harness.RoleSystem, Content: "summary"})) },
	}
	type held struct {
		got, snapshot []transcript.Turn
	}
	var handedOut []held
	for i, step := range steps {
		step()
		for _, model := range []string{"m-session", ""} {
			got, err := Open("sid-tb").TranscriptFor(model)
			if err != nil {
				t.Fatalf("step %d: TranscriptFor: %v", i, err)
			}
			if want := fullTranscript(t, s, model); !reflect.DeepEqual(got, want) {
				t.Fatalf("step %d, model %q:\n got  %+v\n want %+v", i, model, got, want)
			}
			handedOut = append(handedOut, held{got, deepCopyTurns(got)})
		}
	}
	for i, h := range handedOut {
		if !reflect.DeepEqual(h.got, h.snapshot) {
			t.Fatalf("transcript %d changed after it was handed out:\n now  %+v\n then %+v", i, h.got, h.snapshot)
		}
	}
	// And the unlabelled turn really did flip: step 5's note is what took the label away.
	if got := handedOut[2*4].got[1].Model; got != "m-session" {
		t.Fatalf("before any note, a1 = %q, want the session model", got)
	}
	if got := handedOut[2*5].got[1].Model; got != "" {
		t.Fatalf("after a note, a1 = %q, want unlabelled", got)
	}
}

func deepCopyTurns(turns []transcript.Turn) []transcript.Turn {
	b, _ := json.Marshal(turns)
	var out []transcript.Turn
	_ = json.Unmarshal(b, &out)
	return out
}

// TestTranscriptConvertsOnlyWhatWasAppended is the transcript half of #954's acceptance.
func TestTranscriptConvertsOnlyWhatWasAppended(t *testing.T) {
	testHome(t)
	s := Open("sid-tb-incr")
	defer s.Close()
	appendUsers(t, s, 0, 4)
	if _, err := s.TranscriptFor(""); err != nil {
		t.Fatal(err)
	}
	e := cachedEntry(t, s)
	if e.converted != 4 {
		t.Fatalf("first transcript converted %d records, want 4", e.converted)
	}
	if _, err := Open("sid-tb-incr").TranscriptFor(""); err != nil {
		t.Fatal(err)
	}
	appendUsers(t, s, 4, 1)
	turns, err := Open("sid-tb-incr").TranscriptFor("")
	if err != nil || len(turns) != 5 {
		t.Fatalf("TranscriptFor = %d turns, err %v; want 5", len(turns), err)
	}
	if e.converted != 5 {
		t.Fatalf("converted %d records in total, want 5 (4 + only the 1 new)", e.converted)
	}
}

// TestReadsHandOutTheirOwnCopies: what Records, Full and TranscriptFor return is the caller's
// to scribble on, down to tool calls, usage and edits; the next read still says what the log
// says.
func TestReadsHandOutTheirOwnCopies(t *testing.T) {
	testHome(t)
	s := Open("sid-own")
	defer s.Close()
	args := `{"path":"a.go","old_string":"x","new_string":"y"}`
	if _, err := s.AppendUser("q"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendMessage(harness.Message{Role: harness.RoleAssistant,
		ToolCalls: []harness.ToolCall{{ID: "c1", Name: "edit", Arguments: args}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendMessage(harness.Message{Role: harness.RoleTool, Content: "done", ToolCallID: "c1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendUsage(harness.Usage{PromptTokens: 7}, 0); err != nil {
		t.Fatal(err)
	}

	recs, _, err := s.Records()
	if err != nil {
		t.Fatal(err)
	}
	recs[1].ToolCalls[0].Arguments = "scribbled"
	recs[3].Usage.PromptTokens = 999
	full, err := s.Full()
	if err != nil {
		t.Fatal(err)
	}
	full[1].ToolCalls[0].Name = "scribbled"
	turns, err := s.TranscriptFor("")
	if err != nil {
		t.Fatal(err)
	}
	if len(turns[1].Parts) != 1 || len(turns[1].Parts[0].Edits) != 1 {
		t.Fatalf("setup: turn 1 parts = %+v, want one edit part", turns[1].Parts)
	}
	turns[1].Parts[0].Output = "scribbled"
	turns[1].Parts[0].Edits[0].New = "scribbled"
	// The copies share one backing array per read; appending to one turn must not reach the next.
	_ = append(turns[0].Parts, transcript.Part{Kind: "text", Text: "appended"})
	if turns[1].Parts[0].Kind != "tool" {
		t.Fatalf("appending to turn 0's parts overwrote turn 1's: %+v", turns[1].Parts[0])
	}

	recs, _, _ = s.Records()
	if recs[1].ToolCalls[0].Arguments != args || recs[3].Usage.PromptTokens != 7 {
		t.Fatalf("Records after scribbling: %+v / %+v", recs[1].ToolCalls, *recs[3].Usage)
	}
	if u, _, _ := s.LastUsage(); u.PromptTokens != 7 {
		t.Fatalf("LastUsage after scribbling = %d, want 7", u.PromptTokens)
	}
	if full, _ = s.Full(); full[1].ToolCalls[0].Name != "edit" {
		t.Fatalf("Full after scribbling: %+v", full[1].ToolCalls)
	}
	turns, _ = s.TranscriptFor("")
	if p := turns[1].Parts[0]; p.Output != "done" || p.Edits[0].New != "y" {
		t.Fatalf("TranscriptFor after scribbling: %+v", p)
	}
}

// TestRecordsOverlongLineReturnsNothing keeps the full read's contract for a read error: no
// records at all, where a line that merely fails to decode returns the ones before it.
func TestRecordsOverlongLineReturnsNothing(t *testing.T) {
	testHome(t)
	s := Open("sid-long")
	defer s.Close()
	appendUsers(t, s, 0, 2)
	wantContents(t, s, "turn-0", "turn-1")
	appendRaw(t, s, strings.Repeat("x", maxRecordLine+1)+"\n")
	recs, truncated, err := s.Records()
	if err == nil || truncated || recs != nil {
		t.Fatalf("overlong line: %d records, truncated=%v, err=%v; want nil, false, an error", len(recs), truncated, err)
	}
}

// TestSweepRunsWhilePollingAnotherSession: an Agent that keeps polling one session still lets go
// of one it stopped reading, and the one it keeps polling survives.
func TestSweepRunsWhilePollingAnotherSession(t *testing.T) {
	testHome(t)
	idle, polled := Open("sid-idle"), Open("sid-polled")
	defer idle.Close()
	defer polled.Close()
	appendUsers(t, idle, 0, 1)
	appendUsers(t, polled, 0, 1)
	wantContents(t, idle, "turn-0")
	wantContents(t, polled, "turn-0")
	stale := cachedEntry(t, idle)
	stale.mu.Lock()
	stale.used = time.Now().Add(-recordCacheIdle - time.Minute)
	stale.mu.Unlock()
	lastRecordSweep.Store(0)

	wantContents(t, polled, "turn-0") // a hit, not a miss
	if _, ok := recordCache.Load(idle.Path()); ok {
		t.Fatal("the idle session's entry survived a sweep")
	}
	if !stale.dead {
		t.Fatal("a swept entry must be marked dead for a reader that loaded it just before")
	}
	if _, ok := recordCache.Load(polled.Path()); !ok {
		t.Fatal("the polled session's entry was swept")
	}
	wantContents(t, idle, "turn-0") // and reading it again simply starts over
}

// TestFoldTakesIdentityFromTheOpenedFile: a replacement landing between the stat and the open,
// with the same first bytes, must still be read from the start rather than from the old offset.
func TestFoldTakesIdentityFromTheOpenedFile(t *testing.T) {
	testHome(t)
	s := Open("sid-race")
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	line := func(id, content string) string {
		return fmt.Sprintf(`{"id":%q,"ts":"t","kind":"user","content":%q}`+"\n", id, content)
	}
	first := line("1", strings.Repeat("h", recordHeadLen)) // the shared head, longer than the window
	if err := os.WriteFile(s.Path(), []byte(first+line("2", "old")), 0o600); err != nil {
		t.Fatal(err)
	}
	e, size, err := lockEntry(s.Path())
	if err != nil || e == nil {
		t.Fatalf("lockEntry: %v", err)
	}
	if _, _, err := e.refresh(s.Path(), size); err != nil {
		t.Fatal(err)
	}
	e.mu.Unlock()

	// Stat the old file, then let the replacement land before the fold opens the path.
	e, size, err = lockEntry(s.Path())
	if err != nil || e == nil {
		t.Fatalf("lockEntry: %v", err)
	}
	if err := os.WriteFile(s.Path()+".new", []byte(first+line("3", "new")+line("4", "newer")), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(s.Path()+".new", s.Path()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.refresh(s.Path(), size+1); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range e.view() {
		got = append(got, r.ID)
	}
	e.mu.Unlock()
	if !reflect.DeepEqual(got, []string{"1", "3", "4"}) {
		t.Fatalf("records = %v, want [1 3 4] — the new file read from its start", got)
	}
}
