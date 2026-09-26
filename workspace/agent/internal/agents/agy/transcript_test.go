package agy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// A condensed fixture from a real transcript_full.jsonl. One conversation walks all four
// paths: unwrapping USER_INPUT, turning PLANNER_RESPONSE into body text, folding tool steps
// into parts, and skipping SYSTEM lines.
const fixtureJSONL = `{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","content":"<USER_REQUEST>\nRead marker.txt and reply with only its content.\n</USER_REQUEST>\n<ADDITIONAL_METADATA>\nThe current local time is: 2026-07-20T03:06:00+09:00.\n</ADDITIONAL_METADATA>"}
{"step_index":1,"source":"SYSTEM","type":"CONVERSATION_HISTORY","status":"DONE"}
{"step_index":2,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","content":""}
{"step_index":3,"source":"MODEL","type":"VIEW_FILE","status":"DONE","content":"Created At: 2026-07-20T03:06:11+09:00\nCompleted At: 2026-07-20T03:06:11+09:00\nFile Path: file:///tmp/x/marker.txt\nmarker-value-7291"}
{"step_index":4,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","content":"marker-value-7291"}
{"step_index":5,"source":"SYSTEM","type":"CHECKPOINT","status":"DONE","content":"{{ CHECKPOINT 0 }} truncated summary"}
{"step_index":6,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","content":"<USER_REQUEST>\nthanks\n</USER_REQUEST>"}
{"step_index":7,"source":"SYSTEM","type":"ERROR_MESSAGE","status":"DONE","content":"Created At: 2026-07-20T03:07:00+09:00\nError invalid tool call: parse problem"}
{"step_index":8,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","content":"done"}
`

func writeTranscript(t *testing.T, conv, name, body string) {
	t.Helper()
	logs := filepath.Join(brainDir(), conv, ".system_generated", "logs")
	if err := os.MkdirAll(logs, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logs, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestTranscriptParsesBrainJSONL(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := "/home/dev/repos/proj"
	m := session.Meta{Dir: dir, Name: "slot10", Kind: session.KindAgy}
	sids.Write(session.UUID(dir, "slot10"), "conv-1")
	writeTranscript(t, "conv-1", "transcript_full.jsonl", fixtureJSONL)

	td, ok := agentImpl{}.Transcript(m)
	if !ok {
		t.Fatal("Transcript reported unsupported")
	}
	if len(td.Turns) != 4 {
		t.Fatalf("got %d turns, want 4: %+v", len(td.Turns), td.Turns)
	}
	if td.Turns[0].Role != "user" || td.Turns[0].Text != "Read marker.txt and reply with only its content." {
		t.Fatalf("user turn not unwrapped: %+v", td.Turns[0])
	}
	a := td.Turns[1]
	if a.Role != "assistant" || a.Text != "marker-value-7291" {
		t.Fatalf("assistant turn wrong: %+v", a)
	}
	// Parts: VIEW_FILE tool (meta lines stripped) then the text.
	if len(a.Parts) != 2 || a.Parts[0].Kind != "tool" || a.Parts[0].Tool != "VIEW_FILE" {
		t.Fatalf("assistant parts wrong: %+v", a.Parts)
	}
	if got := a.Parts[0].Output; got != "File Path: file:///tmp/x/marker.txt\nmarker-value-7291" {
		t.Fatalf("tool output meta lines not stripped: %q", got)
	}
	// Second exchange: ERROR_MESSAGE surfaces as a tool part on the assistant turn.
	b := td.Turns[3]
	if b.Role != "assistant" || len(b.Parts) != 2 || b.Parts[0].Tool != "ERROR_MESSAGE" || b.Text != "done" {
		t.Fatalf("error surfacing wrong: %+v", b)
	}
}

// Idx must be a strictly increasing line number. While it was left at 0, the Console dropped
// polled turns on idx > lastIdx and the submitted prompt's 反映待ち echo never cleared on
// idx > sinceIdx either, which froze the mirror.
func TestTranscriptAssignsIncreasingIdx(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := "/home/dev/repos/proj"
	m := session.Meta{Dir: dir, Name: "slot12", Kind: session.KindAgy}
	sids.Write(session.UUID(dir, "slot12"), "conv-idx")
	writeTranscript(t, "conv-idx", "transcript_full.jsonl", fixtureJSONL)

	td, _ := agentImpl{}.Transcript(m)
	prev := -1
	for i, turn := range td.Turns {
		if turn.Idx <= prev {
			t.Fatalf("turn %d idx=%d not greater than previous %d: %+v", i, turn.Idx, prev, td.Turns)
		}
		prev = turn.Idx
	}
}

func TestTranscriptEmptyBeforeFirstPrompt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := session.Meta{Dir: "/d", Name: "slot11", Kind: session.KindAgy}
	td, ok := agentImpl{}.Transcript(m)
	if !ok || len(td.Turns) != 0 {
		t.Fatalf("want empty ok transcript before capture, got ok=%v %+v", ok, td.Turns)
	}
}

func TestStripCommandIndent(t *testing.T) {
	in := "The command completed successfully.\n\t\t\t\tOutput:\n\t\t\t\ttool-e2e-done\n\t\t\t\t\tkeep-own-tab"
	want := "The command completed successfully.\nOutput:\ntool-e2e-done\n\tkeep-own-tab"
	if got := stripCommandIndent(in); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestTranscriptPrefersFullOverTruncated(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := "/home/dev/repos/proj"
	m := session.Meta{Dir: dir, Name: "slot12", Kind: session.KindAgy}
	sids.Write(session.UUID(dir, "slot12"), "conv-2")
	writeTranscript(t, "conv-2", "transcript.jsonl",
		`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","content":"<USER_REQUEST>\nshort view\n</USER_REQUEST>"}`+"\n")
	writeTranscript(t, "conv-2", "transcript_full.jsonl",
		`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","content":"<USER_REQUEST>\nfull view\n</USER_REQUEST>"}`+"\n")
	td, _ := agentImpl{}.Transcript(m)
	if len(td.Turns) != 1 || td.Turns[0].Text != "full view" {
		t.Fatalf("did not prefer transcript_full.jsonl: %+v", td.Turns)
	}
}

// agy writes no model on its steps, so the mirror's per-response badge comes from the
// switch notes in the prompts and, failing those, the launch model.
func TestTranscriptStampsModelPerTurn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := "/home/dev/repos/proj"
	switchNote := func(from, to string) string {
		return "The user changed setting `Model Selection` from " + from + " to " + to + ". No need to comment on this change."
	}
	for _, tc := range []struct {
		name   string
		launch string
		lines  []string
		want   []string // model of each assistant turn
	}{
		{"launch model when nothing switched", "gemini-3.1-pro-high",
			[]string{userInput("", "hi"), planner, userInput("", "again"), planner},
			[]string{"gemini-3.1-pro-high", "gemini-3.1-pro-high"}},
		{"first prompt names the model", "",
			[]string{userInput(switchNote("None", "Gemini 3.8 Flash (Low)"), "hi"), planner},
			[]string{"Gemini 3.8 Flash (Low)"}},
		{"a switch relabels only later turns; its from labels earlier ones", "gemini-3.1-pro-high",
			[]string{userInput("", "hi"), planner, userInput(switchNote("Gemini 3.1 Pro (High)", "Gemini 3.6 Flash (High)"), "again"), planner},
			[]string{"Gemini 3.1 Pro (High)", "Gemini 3.6 Flash (High)"}},
		{"the sentence typed in a request is not a switch", "gemini-3.1-pro-high",
			[]string{userInput("", "explain: "+switchNote("A", "B")), planner},
			[]string{"gemini-3.1-pro-high"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			slot := "slot-model-" + tc.name
			conv := "conv-model-" + tc.name
			sids.Write(session.UUID(dir, slot), conv)
			writeTranscript(t, conv, "transcript_full.jsonl", strings.Join(tc.lines, "\n")+"\n")
			td, _ := agentImpl{}.Transcript(session.Meta{Dir: dir, Name: slot, Kind: session.KindAgy, Model: tc.launch})
			var got []string
			for _, turn := range td.Turns {
				if turn.Role == "assistant" {
					got = append(got, turn.Model)
				}
			}
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// Two display names for one id must not flip the badge between polls.
func TestModelLabelFollowsCatalogOrder(t *testing.T) {
	modelsMu.Lock()
	saved := modelsList
	modelsList = []agents.ModelChoice{{ID: "m", Label: "First"}, {ID: "m", Label: "Second"}}
	modelsMu.Unlock()
	t.Cleanup(func() { modelsMu.Lock(); modelsList = saved; modelsMu.Unlock() })
	for range 20 {
		if got := modelLabel("m"); got != "First" {
			t.Fatalf("got %q, want First", got)
		}
	}
	if got := modelLabel("unknown"); got != "unknown" {
		t.Fatalf("uncatalogued id: got %q", got)
	}
}
