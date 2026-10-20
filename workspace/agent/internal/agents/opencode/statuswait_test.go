package opencode

import (
	"strings"
	"time"
)

// statusWaitTimeout bounds how long a contract test waits for a status report or a LiveState
// change that trails the turn's visible effects. It matches the idle wait later in
// TestContractSharedServeTerminalSessions; the pass case returns on the first poll that sees it.
const statusWaitTimeout = 10 * time.Second

// pollUntil calls cond every step until it holds or timeout passes, and reports whether it held.
// cond is evaluated once more at the deadline so a change landing in the last step is not lost.
func pollUntil(timeout, step time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(step)
	}
}

// missingStatusReports returns the "session-status <state> <sid>" lines of want that the fake
// agent's status log does not yet hold. A report under another slot's sid never satisfies it:
// the line is matched whole, newline included.
func missingStatusReports(log string, sid string, states ...string) []string {
	var miss []string
	for _, st := range states {
		if !strings.Contains(log, "session-status "+st+" "+sid+"\n") {
			miss = append(miss, st)
		}
	}
	return miss
}

// awaitStatusReports polls read (the status log) until every state has been reported under sid,
// or timeout passes. It returns the states still missing and the last log read, for the failure
// message.
func awaitStatusReports(read func() string, sid string, states []string, timeout, step time.Duration) (missing []string, last string) {
	pollUntil(timeout, step, func() bool {
		last = read()
		missing = missingStatusReports(last, sid, states...)
		return len(missing) == 0
	})
	return missing, last
}
