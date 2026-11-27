package agy

import (
	"encoding/binary"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

const canceledStepFailure = "expected pending tool step at index 3, got status CORTEX_STEP_STATUS_CANCELED"

func failurePayload(message string) []byte {
	detail := append(binary.AppendUvarint([]byte{0x12}, uint64(len(message))), message...)
	return append(binary.AppendUvarint([]byte{0x1a}, uint64(len(detail))), detail...)
}

func TestExecutorFailureRequiresCurrentTerminalEvidence(t *testing.T) {
	for _, tc := range []struct {
		name                string
		stepType, end, mark int
		message, want       string
	}{
		{"terminal executor error", stepTypeError, 4, 1, canceledStepFailure, canceledStepFailure},
		{"command output with same text", 21, 4, 1, canceledStepFailure, ""},
		{"ordinary tool error", stepTypeError, 4, 1, "invalid tool call", ""},
		{"interrupted before error step", stepTypeError, 3, 1, canceledStepFailure, ""},
		{"previous turn before delivered input", stepTypeError, 4, 4, canceledStepFailure, ""},
		{"previous turn with newer input", stepTypeError, 4, 5, canceledStepFailure, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, sid := hookFixture(t, "failure")
			conv := "conv-failure"
			sids.Write(sid, conv)
			mkConvDBWithTurns(t, conv, [][3]any{
				{stepTypeUser, stepStatusDone, []byte("first")},
				{15, stepStatusDone, []byte("answer")},
				{stepTypeUser, stepStatusDone, []byte("watch notice")},
				{21, stepStatusDone, []byte("tool succeeded")},
				{tc.stepType, stepStatusDone, failurePayload(tc.message)},
			}, [][]byte{executorRow(4, 1), executorRow(4, tc.end)})
			if got := turnFailure(m, strconv.Itoa(tc.mark)); got != tc.want {
				t.Fatalf("failure = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestExecutorFailureStopWithoutDatabaseErrorStep(t *testing.T) {
	m, sid := hookFixture(t, "hook-failure")
	conv := "conv-hook"
	sids.Write(sid, conv)
	mkConvDBWithTurns(t, conv, [][3]any{
		{stepTypeUser, stepStatusDone, []byte("first")},
		{15, stepStatusDone, []byte("answer")},
		{stepTypeUser, stepStatusDone, []byte("watch notice")},
		{21, stepStatusDone, []byte("tool succeeded")},
	}, [][]byte{executorRow(4, 1)})
	status.Persist(sid, "working")
	input := hookInput{ConversationID: conv, FullyIdle: ptr(true), Error: canceledStepFailure}
	for _, reason := range []string{"model_stop", "canceled", "error"} {
		input.TerminationReason = reason
		recordGen(m.Name, "Stop", input, time.Now())
		got := turnFailure(m, "1")
		after, known := EndedAfter(m, "1")
		wantFailure := reason == "error"
		if (got == canceledStepFailure) != wantFailure || !known || after != wantFailure {
			t.Fatalf("Stop reason %s: failure %q, ended %v/%v", reason, got, after, known)
		}
	}
	status.Persist(sid, "working")
	if got := turnFailure(m, "1"); got != "" {
		t.Fatalf("new revision reused old Stop: %q", got)
	}
}

func TestTranscriptShowsExecutorFailureOmittedFromJSONL(t *testing.T) {
	m, sid := hookFixture(t, "mirror-failure")
	conv := "conv-mirror"
	sids.Write(sid, conv)
	mkConvDBWithTurns(t, conv, [][3]any{
		{stepTypeUser, stepStatusDone, []byte("watch notice")},
		{21, stepStatusDone, []byte("tool succeeded")},
		{stepTypeError, stepStatusDone, failurePayload(canceledStepFailure)},
		{stepTypeUser, stepStatusDone, []byte("continue")},
		{15, stepStatusDone, []byte("continued")},
	}, [][]byte{executorRow(4, 2), executorRow(4, 4)})
	writeTranscript(t, conv, "transcript_full.jsonl", `{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","content":"watch notice"}
{"step_index":1,"source":"MODEL","type":"RUN_COMMAND","content":"tool succeeded"}
{"step_index":3,"source":"USER_EXPLICIT","type":"USER_INPUT","content":"continue"}
{"step_index":4,"source":"MODEL","type":"PLANNER_RESPONSE","content":"continued"}
`)
	td, _ := agentImpl{}.Transcript(m)
	if len(td.Turns) != 4 {
		t.Fatalf("turns = %+v", td.Turns)
	}
	parts := td.Turns[1].Parts
	if len(parts) != 2 || parts[0].Kind != "tool" || parts[1].Kind != "error" || parts[1].Text != canceledStepFailure {
		t.Fatalf("failed turn parts = %+v", parts)
	}
	if next := td.Turns[3].Parts; len(next) != 1 || next[0].Kind != "text" {
		t.Fatalf("continuation parts = %+v", next)
	}
}

func TestTranscriptBackfillsFailureWithStableIdentity(t *testing.T) {
	for _, promptPresent := range []bool{true, false} {
		t.Run("prompt-present="+strconv.FormatBool(promptPresent), func(t *testing.T) {
			m, sid := hookFixture(t, "checkpoint-failure")
			const conv = "conv-checkpoint"
			sids.Write(sid, conv)
			mkConvDB(t, conv, [][3]any{
				{stepTypeUser, stepStatusDone, []byte("first")},
				{15, stepStatusDone, []byte("answered")},
				{stepTypeUser, stepStatusDone, []byte("failed prompt")},
				{21, stepStatusDone, []byte("tool")},
				{stepTypeError, stepStatusDone, failurePayload(canceledStepFailure)},
				{stepTypeUser, stepStatusDone, []byte("continue")},
				{15, stepStatusDone, []byte("continued")},
			})
			const prompt = `{"step_index":2,"source":"USER_EXPLICIT","type":"USER_INPUT","content":"failed prompt"}` + "\n"
			log := `{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","content":"first"}
{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","content":"answered"}
{"step_index":5,"source":"USER_EXPLICIT","type":"USER_INPUT","content":"continue"}
{"step_index":6,"source":"MODEL","type":"PLANNER_RESPONSE","content":"continued"}
`
			if promptPresent {
				log += prompt
			}
			writeTranscript(t, conv, "transcript.jsonl", log)
			before, _ := agentImpl{}.Transcript(m)
			want := []int{0, 1, 5, 10, 11}
			if promptPresent {
				want = []int{0, 1, 4, 5, 10, 11}
			}
			check := func(turns []transcript.Turn, indexes []int, kinds ...string) {
				t.Helper()
				var got []int
				var parts []transcript.Part
				for _, turn := range turns {
					got = append(got, turn.Idx)
					if turn.Idx == 5 && turn.Role == "assistant" {
						parts = turn.Parts
					}
				}
				if !slices.Equal(got, indexes) || len(parts) != len(kinds) {
					t.Fatalf("indexes = %v, want %v, failure parts = %+v", got, indexes, parts)
				}
				for i, kind := range kinds {
					if parts[i].Kind != kind || (kind == "error" && parts[i].Text != canceledStepFailure) {
						t.Fatalf("failure parts = %+v, want %v", parts, kinds)
					}
				}
			}
			check(before.Turns, want, "error")

			// A later read has both sources for the error and an earlier tool row.
			if !promptPresent {
				log += prompt
			}
			writeTranscript(t, conv, "transcript_full.jsonl", log+`{"step_index":3,"source":"MODEL","type":"RUN_COMMAND","content":"tool"}
{"step_index":4,"source":"SYSTEM","type":"ERROR_MESSAGE","content":"unredacted CLI error"}
`)
			after, _ := agentImpl{}.Transcript(m)
			check(after.Turns, []int{0, 1, 4, 5, 10, 11}, "tool", "error")
		})
	}
}

func TestTranscriptShowsSeparateDatabaseFailuresWithoutJSONL(t *testing.T) {
	m, sid := hookFixture(t, "database-only-failures")
	const conv = "conv-database-only"
	sids.Write(sid, conv)
	mkConvDB(t, conv, [][3]any{
		{stepTypeUser, stepStatusDone, []byte("first")},
		{stepTypeError, stepStatusDone, failurePayload(canceledStepFailure)},
		{stepTypeUser, stepStatusDone, []byte("second")},
		{stepTypeError, stepStatusDone, failurePayload(canceledStepFailure)},
	})
	td, _ := agentImpl{}.Transcript(m)
	if len(td.Turns) != 2 || td.Turns[0].Idx != 1 || td.Turns[1].Idx != 5 {
		t.Fatalf("database-only turns = %+v", td.Turns)
	}
	for _, turn := range td.Turns {
		if turn.Role != "assistant" || len(turn.Parts) != 1 || turn.Parts[0].Text != canceledStepFailure {
			t.Fatalf("database-only turn = %+v", turn)
		}
	}
}
