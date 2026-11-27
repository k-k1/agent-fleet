package agy

import (
	"database/sql"
	"regexp"
	"strconv"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// stepTypeError is CORTEX_STEP_TYPE_ERROR_MESSAGE in agy 1.5.16 and 1.5.18. The canceled
// tool itself is not the failure evidence. The executor writes a separate
// error step after it (google-antigravity/antigravity-cli#1814).
const stepTypeError = 17

const executorErrorPrefix = "expected pending tool step at index "

var canceledPendingStep = regexp.MustCompile(`^` + executorErrorPrefix + `[0-9]+, got status CORTEX_STEP_STATUS_CANCELED$`)

// executorErrorText returns only the invariant error. Tool output, step
// payloads and hook errors can contain commands, paths and credentials.
func executorErrorText(raw string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(raw), "\n")
	line = strings.TrimPrefix(strings.TrimSpace(line), "agent executor error: ")
	return canceledPendingStep.FindString(line)
}

// Both CLI descriptors put CortexErrorDetails in CortexStepErrorMessage
// field 3. Only its short, full and user error strings can identify a failure.
func executorErrorPayload(payload []byte) string {
	detail := protoBytesField(payload, 3)
	for _, field := range []uint64{2, 3, 1} {
		if message := executorErrorText(string(protoBytesField(detail, field))); message != "" {
			return message
		}
	}
	return ""
}

type executorErrorStep struct {
	index int
	text  string
}

func executorErrorSteps(conv string, first, last int) []executorErrorStep {
	db, err := sql.Open("sqlite", "file:"+conversationDBPath(conv)+"?mode=ro&_pragma=busy_timeout(1000)")
	if err != nil {
		return nil
	}
	defer db.Close()
	rows, err := db.Query(`SELECT idx, step_payload FROM steps
		WHERE step_type=? AND status=? AND idx BETWEEN ? AND ? AND instr(step_payload, ?) > 0 ORDER BY idx`,
		stepTypeError, stepStatusDone, first, last, []byte(executorErrorPrefix))
	if err != nil {
		return nil
	}
	defer rows.Close()
	var result []executorErrorStep
	for rows.Next() {
		var index int
		var payload []byte
		if rows.Scan(&index, &payload) == nil {
			if message := executorErrorPayload(payload); message != "" {
				result = append(result, executorErrorStep{index, message})
			}
		}
	}
	return result
}

func hookExecutorFailure(m session.Meta, conv string) (executorErrorStep, bool) {
	sid := session.UUID(m.Dir, m.Name)
	rec, ok := currentRecord(sid)
	if !ok || rec.ConvID != conv || rec.StepIdx == nil || rec.Failure == "" || rec.StatusRev == nil {
		return executorErrorStep{}, false
	}
	if st, ok := status.Read(sid); ok && st.State == "working" && st.Rev != *rec.StatusRev {
		return executorErrorStep{}, false
	}
	return executorErrorStep{*rec.StepIdx, rec.Failure}, true
}

// turnFailure uses the input baseline to exclude a previous turn's late Stop
// or error record. A canceled tool and a user interrupt have neither an
// executor failure Stop nor an invariant error as their final step.
func turnFailure(m session.Meta, mark string) string {
	baseline, err := strconv.Atoi(mark)
	if err != nil {
		return ""
	}
	row, ok := lastStep(m)
	if !ok || row.idx <= baseline {
		return ""
	}
	first, found, ok := firstUserStepAfter(row.conv, baseline)
	if !ok || !found {
		return ""
	}
	if row.turnLog == turnLogRead && row.turnEnd == row.idx && row.turnEnd >= first &&
		row.stepType == stepTypeError && row.status == stepStatusDone {
		if message := executorErrorPayload(row.payload); message != "" {
			return message
		}
	}
	if failure, ok := hookExecutorFailure(m, row.conv); ok && failure.index == row.idx && failure.index >= first {
		return failure.text
	}
	return ""
}
