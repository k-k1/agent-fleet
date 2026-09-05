package codex

import (
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// A rollout written a piece at a time, the way codex appends to one.
func writeRollout(t *testing.T, path string, chunk string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(chunk); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func snapshotOf(t *testing.T, path string) []transcript.Turn {
	t.Helper()
	var turns []transcript.Turn
	if !withRollout(path, "", func(p *rolloutParser) { turns, _, _, _ = p.snapshot() }) {
		t.Fatalf("withRollout(%s) = false, want a readable rollout", path)
	}
	return turns
}

func sameTurns(t *testing.T, got, want []transcript.Turn, what string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: %d turns, want %d\ngot  %+v\nwant %+v", what, len(got), len(want), got, want)
	}
	for i := range got {
		if got[i].Idx != want[i].Idx || got[i].Role != want[i].Role || got[i].Text != want[i].Text ||
			len(got[i].Parts) != len(want[i].Parts) {
			t.Fatalf("%s: turn %d = %+v, want %+v", what, i, got[i], want[i])
		}
		for j := range got[i].Parts {
			if !reflect.DeepEqual(got[i].Parts[j], want[i].Parts[j]) {
				t.Fatalf("%s: turn %d part %d = %+v, want %+v", what, i, j, got[i].Parts[j], want[i].Parts[j])
			}
		}
	}
}

// Resuming over appended lines must produce exactly what a parse of the whole file
// produces — including each turn's absolute Idx, which the Console pages over.
func TestRolloutResumeEqualsWholeParse(t *testing.T) {
	head := `{"timestamp":"2026-06-29T00:00:00Z","type":"session_meta","payload":{"cwd":"/home/dev/repos/x","git":{"branch":"main"}}}
{"type":"event_msg","payload":{"type":"task_started","turn_id":"t1"}}
{"timestamp":"2026-06-29T00:00:01Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello codex"}]}}
{"type":"event_msg","payload":{"type":"item_completed","item":{"noise":"never read"}}}
{"timestamp":"2026-06-29T00:00:02Z","type":"response_item","payload":{"type":"function_call","name":"shell","call_id":"c1","arguments":"{\"command\":[\"ls\"]}"}}
`
	tail := `{"timestamp":"2026-06-29T00:00:03Z","type":"response_item","payload":{"type":"function_call_output","call_id":"c1","output":"total 0"}}
{"timestamp":"2026-06-29T00:00:04Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi there"}]}}
{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":1000,"cached_input_tokens":800,"output_tokens":42},"model_context_window":258400}}}
{"type":"event_msg","payload":{"type":"task_complete","turn_id":"t1"}}
`
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	writeRollout(t, path, head)
	snapshotOf(t, path) // fold the head only, as a poll mid-conversation would
	writeRollout(t, path, tail)
	got := snapshotOf(t, path)

	lines, err := rolloutLines(path)
	if err != nil {
		t.Fatal(err)
	}
	want, _, _, _ := parseRolloutFull(lines)
	sameTurns(t, got, want, "resumed parse")
	// The output attached to the call parsed in the EARLIER chunk — the whole point of
	// keeping the parser rather than its result.
	if got[1].Parts[0].Output != "total 0" {
		t.Fatalf("tool output = %q, want it attached across the resume", got[1].Parts[0].Output)
	}
	if got[2].InTok != 200 || got[2].OutTok != 42 {
		t.Fatalf("usage = %d/%d, want 200/42 attached to the assistant turn", got[2].InTok, got[2].OutTok)
	}
}

// codex may be halfway through writing a line when a poll reads. That line must not be
// consumed: it has to keep its index for when the rest of it lands.
func TestRolloutHoldsBackPartialLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	writeRollout(t, path, `{"timestamp":"2026-06-29T00:00:01Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"first"}]}}
`)
	writeRollout(t, path, `{"timestamp":"2026-06-29T00:00:02Z","type":"response_item","payload":{"type":"message","role":"user","con`)
	if got := snapshotOf(t, path); len(got) != 1 {
		t.Fatalf("%d turns while a line is half-written, want 1: %+v", len(got), got)
	}
	writeRollout(t, path, `tent":[{"type":"input_text","text":"second"}]}}
`)
	got := snapshotOf(t, path)
	if len(got) != 2 || got[1].Text != "second" || got[1].Idx != 1 {
		t.Fatalf("after the line completed: %+v, want a second turn at Idx 1", got)
	}
}

// A rollout that shrank is not the same rollout (a resumed session rewrote it, or the
// glob landed on a replacement): the parse starts over rather than appending to a
// history that no longer exists.
func TestRolloutRestartsWhenFileShrinks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	writeRollout(t, path, `{"timestamp":"2026-06-29T00:00:01Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"old one"}]}}
{"timestamp":"2026-06-29T00:00:02Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"old two"}]}}
`)
	snapshotOf(t, path)
	if err := os.WriteFile(path, []byte(`{"timestamp":"2026-06-29T00:00:03Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"fresh"}]}}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	got := snapshotOf(t, path)
	if len(got) != 1 || got[0].Text != "fresh" {
		t.Fatalf("after the file was replaced: %+v, want only the new content", got)
	}
}

// A replacement can also be LONGER than what we had folded, and then the size alone says
// nothing. The file's head is the identity that catches it — without this the new file's
// lines would be appended to a parse of a conversation that no longer exists.
func TestRolloutRestartsWhenFileIsReplaced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	writeRollout(t, path, `{"timestamp":"2026-06-29T00:00:01Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"old"}]}}
`)
	snapshotOf(t, path)
	if err := os.WriteFile(path, []byte(`{"timestamp":"2026-06-30T09:00:00Z","type":"session_meta","payload":{"cwd":"/x","git":{"branch":"main"}}}
{"timestamp":"2026-06-30T09:00:01Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"one"}]}}
{"timestamp":"2026-06-30T09:00:02Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"two"}]}}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	got := snapshotOf(t, path)
	if len(got) != 2 || got[0].Text != "one" || got[0].Idx != 1 {
		t.Fatalf("after a longer file replaced the old one: %+v, want the new conversation alone", got)
	}
}

// The snapshot is the caller's own copy: /messages rewrites userfile paths in place and
// drops parts, and those edits must not reach the parse the next poll resumes.
func TestRolloutSnapshotDoesNotAliasTheCache(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	writeRollout(t, path, `{"timestamp":"2026-06-29T00:00:01Z","type":"response_item","payload":{"type":"function_call","name":"image_gen","call_id":"c1","arguments":"{}"}}
{"timestamp":"2026-06-29T00:00:02Z","type":"response_item","payload":{"type":"function_call_output","call_id":"c1","output":"Generated images are saved to /tmp as /tmp/a.png by default."}}
`)
	first := snapshotOf(t, path)
	if len(first) != 1 || len(first[0].Parts) != 2 || first[0].Parts[1].Files[0] != "/tmp/a.png" {
		t.Fatalf("setup: %+v, want a tool turn with a userfile part", first)
	}
	first[0].Parts[1].Files[0] = "rewritten/by/the/caller.png"
	first[0].Parts = first[0].Parts[:1]

	second := snapshotOf(t, path)
	if len(second[0].Parts) != 2 || second[0].Parts[1].Files[0] != "/tmp/a.png" {
		t.Fatalf("second snapshot = %+v, want the cached parse untouched by the first caller", second[0].Parts)
	}
}

// The lifecycle the missed-Stop heal reads (rolloutCompletedAfter) is folded in as the
// lines go past, so it must be current after a resume.
func TestRolloutLifecycleFollowsAppends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	writeRollout(t, path, `{"timestamp":"2026-06-29T00:00:01Z","type":"event_msg","payload":{"type":"task_started","turn_id":"t1"}}
`)
	read := func() (string, time.Time) {
		var state string
		var at time.Time
		withRollout(path, "", func(p *rolloutParser) { state, at = p.lifecycle, p.lifecycleAt })
		return state, at
	}
	if state, _ := read(); state != "task_started" {
		t.Fatalf("lifecycle = %q, want task_started", state)
	}
	writeRollout(t, path, `{"timestamp":"2026-06-29T00:00:09Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"t1"}}
`)
	state, at := read()
	want, _ := time.Parse(time.RFC3339, "2026-06-29T00:00:09Z")
	if state != "task_complete" || !at.Equal(want) {
		t.Fatalf("lifecycle = %q at %v, want task_complete at %v", state, at, want)
	}
}

// peekKinds is an optimization, so what matters is that it never reports a kind that
// isn't there: an unexpected shape must fall back to decoding the line.
func TestPeekKinds(t *testing.T) {
	for _, tc := range []struct {
		name        string
		line        string
		outer, kind string
		ok          bool
	}{
		{"event", `{"timestamp":"t","ordinal":7,"type":"event_msg","payload":{"type":"item_completed","item":{}}}`, "event_msg", "item_completed", true},
		{"response", `{"timestamp":"t","type":"response_item","payload":{"type":"custom_tool_call_output","output":""}}`, "response_item", "custom_tool_call_output", true},
		{"payload type not first", `{"type":"event_msg","payload":{"info":{},"type":"token_count"}}`, "", "", false},
		{"no payload", `{"type":"turn_context","payload":{"turn_id":"t1"}}`, "", "", false},
		{"not json at all", `half a li`, "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outer, kind, ok := peekKinds([]byte(tc.line))
			if outer != tc.outer || kind != tc.kind || ok != tc.ok {
				t.Fatalf("peekKinds = (%q, %q, %v), want (%q, %q, %v)", outer, kind, ok, tc.outer, tc.kind, tc.ok)
			}
		})
	}
}

// The fallback the peek leans on: a payload whose "type" is not the first key is still
// read, so skipping event_msgs can never silently drop a token_count.
func TestUsageFoldsWhenPayloadTypeIsNotFirst(t *testing.T) {
	lines := [][]byte{
		[]byte(`{"timestamp":"2026-06-29T00:00:01Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}}`),
		[]byte(`{"type":"event_msg","payload":{"info":{"last_token_usage":{"input_tokens":10,"cached_input_tokens":4,"output_tokens":2}},"type":"token_count"}}`),
	}
	turns, _, _, _ := parseRolloutFull(lines)
	if len(turns) != 1 || turns[0].InTok != 6 || turns[0].OutTok != 2 {
		t.Fatalf("turns = %+v, want usage 6/2 folded onto the assistant turn", turns)
	}
}

// One session is read by several callers at once (the chat poll, the sessions list, a
// usage read). They share one parse, so the fold and the snapshot have to be serialized —
// run under -race, this is the check that says so.
func TestRolloutConcurrentReaders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	writeRollout(t, path, `{"timestamp":"2026-06-29T00:00:00Z","type":"session_meta","payload":{"cwd":"/x","git":{"branch":"main"}}}
`)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				withRollout(path, "", func(p *rolloutParser) {
					turns, _, _, _ := p.snapshot()
					_ = turns
				})
			}
		}()
	}
	for i := 0; i < 20; i++ {
		writeRollout(t, path, `{"timestamp":"2026-06-29T00:00:01Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"more"}]}}
`)
	}
	wg.Wait()
	if got := snapshotOf(t, path); len(got) != 20 {
		t.Fatalf("%d turns after 20 appends, want 20", len(got))
	}
}

// An entry no one has read for a while is dropped; one that is fresh stays.
func TestSweepRolloutCacheDropsIdleEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	writeRollout(t, path, `{"timestamp":"2026-06-29T00:00:01Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}}
`)
	snapshotOf(t, path)
	sweepRolloutCache(time.Now())
	if _, ok := rolloutCache.Load(path); !ok {
		t.Fatal("a just-read parse was swept")
	}
	sweepRolloutCache(time.Now().Add(rolloutCacheIdle + time.Minute))
	if _, ok := rolloutCache.Load(path); ok {
		t.Fatal("an idle parse was kept")
	}
}
