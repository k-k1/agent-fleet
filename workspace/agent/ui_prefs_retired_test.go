package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
)

// The memory review's keys (ADR 0130 decision 2) are retired: a file that still holds them reads
// as any other, and the next save drops them, whoever sent them. A session's own token can write
// them without the member-origin mark now, because nothing is armed by them.
func TestRetiredMemoryReviewPrefsAreIgnoredOnReadAndDroppedOnSave(t *testing.T) {
	writeUIPrefs(t, `{"agentMemory":true,"memoryReview":"propose","memoryReviewAuto":true,"iconSet":"vscode"}`)
	if got := uiprefs.Read(); got["iconSet"] != "vscode" || !uiprefs.AgentMemory() {
		t.Fatalf("a file with the retired keys did not read: %v", got)
	}

	body := `{"agentMemory":true,"memoryReview":"propose","memoryReviewAuto":true,"iconSet":"x"}`
	req := httptest.NewRequest(http.MethodPut, "/env/ui-prefs", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handlePutUIPrefs(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("an unmarked write of the retired keys: %d %s", rec.Code, rec.Body)
	}
	b, err := os.ReadFile(uiprefs.Path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "memoryReview") {
		t.Fatalf("the retired keys were saved: %s", b)
	}
	if got := uiprefs.Read(); got["iconSet"] != "x" || got["agentMemory"] != true {
		t.Fatalf("the other keys did not survive: %v", got)
	}
}
