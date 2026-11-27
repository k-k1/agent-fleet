package agy

import (
	"encoding/binary"
	"strconv"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
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
