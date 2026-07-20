package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

// TestAgentModelsClaudeFixedAliases pins the claude branch of /agents/{kind}/models:
// the fixed tier aliases (no live catalog — launch takes --model <alias>), served so
// the MCP list_models resolves claude ids like the other kinds.
func TestAgentModelsClaudeFixedAliases(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/agents/claude/models", nil)
	req.SetPathValue("kind", "claude")
	rec := httptest.NewRecorder()
	handleAgentModels(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Models []agents.ModelChoice `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := []string{"fable", "opus", "sonnet", "haiku"}
	if len(got.Models) != len(want) {
		t.Fatalf("models = %+v, want ids %v", got.Models, want)
	}
	for i, id := range want {
		if got.Models[i].ID != id {
			t.Fatalf("models[%d].id = %q, want %q", i, got.Models[i].ID, id)
		}
	}

	// Unknown kind still 404s.
	req = httptest.NewRequest(http.MethodGet, "/agents/shell/models", nil)
	req.SetPathValue("kind", "shell")
	rec = httptest.NewRecorder()
	handleAgentModels(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown kind status = %d, want 404", rec.Code)
	}
}
