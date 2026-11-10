package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/memberorigin/memberorigintest"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
)

func putPrefs(t *testing.T, body string, sign bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/env/ui-prefs", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if sign {
		memberorigintest.Install(t).Sign(req, []byte(body))
	}
	rec := httptest.NewRecorder()
	handlePutUIPrefs(rec, req)
	return rec
}

// The consolidator sends memory text to a model and spends tokens unattended, so switching it on is
// the member's decision: a session's own token (no mark) cannot, the marked Console write can, and
// switching it off needs nothing. It is off by default and only on together with agent memory.
func TestMemoryConsolidationSwitchNeedsTheMemberOriginMarkToTurnOn(t *testing.T) {
	writeUIPrefs(t, `{"agentMemory":true}`)
	if uiprefs.MemoryConsolidation() || uiprefs.MemoryConsolidationEnabled() {
		t.Fatal("consolidation is on by default")
	}
	if rec := putPrefs(t, `{"agentMemory":true,"memoryConsolidation":true}`, false); rec.Code == http.StatusOK {
		t.Fatalf("an unmarked write switched consolidation on: %d", rec.Code)
	}
	if uiprefs.MemoryConsolidation() {
		t.Fatal("the refused write was stored")
	}
	if rec := putPrefs(t, `{"agentMemory":true,"memoryConsolidation":true}`, true); rec.Code != http.StatusOK {
		t.Fatalf("a marked write: %d %s", rec.Code, rec.Body)
	}
	if !uiprefs.MemoryConsolidationEnabled() {
		t.Fatal("the marked write did not switch it on")
	}
	// Agent memory off: the consolidator may not run, whatever its own switch says.
	writeUIPrefs(t, `{"agentMemory":false,"memoryConsolidation":true}`)
	if uiprefs.MemoryConsolidationEnabled() {
		t.Fatal("consolidation is enabled with agent memory off")
	}
	// Off needs no mark.
	writeUIPrefs(t, `{"agentMemory":true,"memoryConsolidation":true}`)
	if rec := putPrefs(t, `{"agentMemory":true,"memoryConsolidation":false}`, false); rec.Code != http.StatusOK {
		t.Fatalf("switching off without a mark: %d %s", rec.Code, rec.Body)
	}
	if uiprefs.MemoryConsolidation() {
		t.Fatal("switch off was not stored")
	}
	// Only a boolean true is on.
	writeUIPrefs(t, `{"agentMemory":true,"memoryConsolidation":"true"}`)
	if uiprefs.MemoryConsolidation() {
		t.Fatal(`the string "true" read as on`)
	}
}

// The scheduled lint publishes commits unattended, so like the consolidator its switch is the
// member's: an unmarked write cannot turn it on, switching off needs nothing, it is off when
// missing, and it is separate from memoryConsolidation (lint calls no model).
func TestMemoryLintScheduleSwitchNeedsTheMemberOriginMarkToTurnOn(t *testing.T) {
	writeUIPrefs(t, `{"agentMemory":true,"memoryConsolidation":true}`)
	if uiprefs.MemoryLintSchedule() || uiprefs.MemoryLintScheduleEnabled() {
		t.Fatal("the schedule is on by default, or follows the consolidator's switch")
	}
	if rec := putPrefs(t, `{"agentMemory":true,"memoryLintSchedule":true}`, false); rec.Code == http.StatusOK {
		t.Fatalf("an unmarked write switched the schedule on: %d", rec.Code)
	}
	if rec := putPrefs(t, `{"agentMemory":true,"memoryLintSchedule":true}`, true); rec.Code != http.StatusOK {
		t.Fatalf("a marked write: %d %s", rec.Code, rec.Body)
	}
	if !uiprefs.MemoryLintScheduleEnabled() || uiprefs.MemoryConsolidation() {
		t.Fatal("the marked write did not switch only the schedule on")
	}
	writeUIPrefs(t, `{"agentMemory":false,"memoryLintSchedule":true}`)
	if uiprefs.MemoryLintScheduleEnabled() {
		t.Fatal("enabled with agent memory off")
	}
	writeUIPrefs(t, `{"agentMemory":true,"memoryLintSchedule":true}`)
	if rec := putPrefs(t, `{"agentMemory":true,"memoryLintSchedule":false}`, false); rec.Code != http.StatusOK || uiprefs.MemoryLintSchedule() {
		t.Fatalf("switching off without a mark: %d", rec.Code)
	}
	writeUIPrefs(t, `{"agentMemory":true,"memoryLintSchedule":"true"}`)
	if uiprefs.MemoryLintSchedule() {
		t.Fatal(`the string "true" read as on`)
	}
}
