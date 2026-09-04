package main

import (
	"encoding/json"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
)

// The boundaries of the deny match. claude accepts either an alias or the full id on --model,
// so denying an alias must deny the full id too; conversely opencode's two billing routes
// (opencode/… and opencode-go/…) must not be taken out together.
func TestModelMatchesHiddenTokenBoundary(t *testing.T) {
	tests := []struct {
		requested, hidden string
		want              bool
	}{
		{"fable", "fable", true},
		{"Fable", "fable", true},          // case-insensitive
		{"claude-fable-5", "fable", true}, // denying an alias denies the full id too
		{"claude-fable-5-20260101", "fable", true},
		{"opus", "fable", false},
		{"claude-opus-5", "fable", false},
		{"fablet", "fable", false}, // token boundary (never a substring match)
		{"unfable", "fable", false},
		{"", "fable", false}, // unset = defer to the CLI's default
		{"fable", "", false},
		// Hiding a concrete id (several tokens) must not take out another model that merely has
		// it as a prefix. Regression for the bug where hiding GPT-5.4 removed mini as well.
		{"gpt-5.4-mini", "gpt-5.4", false},
		{"gpt-5.4", "gpt-5.4", true},
		{"gpt-5.4-mini", "gpt-5.4-mini", true},
		{"claude-fable-5-20260101", "claude-fable-5", false}, // as above (a concrete id, not an alias)
		// opencode: denying Zen leaves the Go subscription's twin of the same name
		{"opencode-go/glm-5.2", "opencode/glm-5.2", false},
		{"opencode/glm-5.2", "opencode/glm-5.2", true},
		// The bare name (glm-5.2) is several tokens too, so it no longer matches as a family.
		// Hiding both routes means denying both ids (the UI lists both). Erring towards taking
		// out too little.
		{"opencode/glm-5.2", "glm-5.2", false},
	}
	for _, tt := range tests {
		if got := sessionx.ModelMatchesHidden(tt.requested, tt.hidden); got != tt.want {
			t.Errorf("modelMatchesHidden(%q, %q) = %v, want %v", tt.requested, tt.hidden, got, tt.want)
		}
	}
}

func TestHiddenModelsForIgnoresJunkAndAllHiddenClaude(t *testing.T) {
	writeUIPrefs(t, `{"hiddenModels":{"claude":["fable"," ",42],"codex":"nope"}}`)
	if got := sessionx.HiddenModelsFor("claude"); len(got) != 1 || got[0] != "fable" {
		t.Fatalf("hiddenModelsFor(claude) = %v, want [fable]", got)
	}
	if got := sessionx.HiddenModelsFor("codex"); got != nil { // a wrong type means "nothing hidden"
		t.Fatalf("hiddenModelsFor(codex) = %v, want nil", got)
	}
	if got := sessionx.HiddenModelsFor("opencode"); got != nil { // unset
		t.Fatalf("hiddenModelsFor(opencode) = %v, want nil", got)
	}

	// A setting that denies every claude tier is ignored: there are only the four fixed tiers and
	// no "default" choice, so hiding them all leaves nothing to launch with.
	writeUIPrefs(t, `{"hiddenModels":{"claude":["fable","opus","sonnet","haiku"]}}`)
	if got := sessionx.HiddenModelsFor("claude"); got != nil {
		t.Fatalf("all-hidden claude = %v, want nil (fail-safe)", got)
	}
	if sessionx.ModelHidden("claude", "fable") {
		t.Fatal("modelHidden(claude, fable) = true under the all-hidden fail-safe")
	}
}

// It must actually disappear from the catalog — where the Console picker and MCP list_models
// meet.
func TestAgentModelsHidesExcludedClaudeTier(t *testing.T) {
	writeUIPrefs(t, `{"hiddenModels":{"claude":["fable"]}}`)
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
	want := []string{"opus", "sonnet", "haiku"}
	if len(got.Models) != len(want) {
		t.Fatalf("models = %+v, want ids %v", got.Models, want)
	}
	for i, id := range want {
		if got.Models[i].ID != id {
			t.Fatalf("models[%d].id = %q, want %q", i, got.Models[i].ID, id)
		}
	}
}

// Dropping it from the list alone lets an explicit id straight through, so the launch guard is
// the real mechanism: a scheduled run's model field, MCP create_session and a user-defined
// assistant's free-text input all pass here.
func TestCreateSessionRejectsHiddenModel(t *testing.T) {
	writeUIPrefs(t, `{"hiddenModels":{"claude":["fable"]}}`)
	// The guard stands before any side effect (clone / worktree / launch), so these calls create
	// nothing.
	for _, model := range []string{"fable", "Fable", "claude-fable-5"} {
		req := httptest.NewRequest(http.MethodPost, "/sessions",
			strings.NewReader(`{"kind":"claude","model":"`+model+`"}`))
		rec := httptest.NewRecorder()
		sessionx.HandleCreateSession(rec, req)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "model_hidden") {
			t.Fatalf("model %q: status = %d, body = %s, want 400 model_hidden", model, rec.Code, rec.Body.String())
		}
	}
	// A tier that is not denied must not be caught by this guard.
	if sessionx.ModelHidden("claude", "sonnet") {
		t.Fatal("modelHidden(claude, sonnet) = true, want false")
	}
}

// A denied model left in the assistant's settings is not adopted: it falls back to "unset" and
// from there to the recommendation or the CLI's default.
func TestAssistantModelPrefDropsHidden(t *testing.T) {
	writeUIPrefs(t, `{"hiddenModels":{"claude":["fable"]},"assistantModels":{"claude":"fable"},"assistantAutoTurnModel":"fable"}`)
	if v, ok := assistantChatModelPref("claude"); ok {
		t.Fatalf("assistantChatModelPref = %q, %v; want dropped", v, ok)
	}
	if v := chatAutoTurnModel(); v != "" {
		t.Fatalf("chatAutoTurnModel = %q, want \"\"", v)
	}
	// The "recommended" sentinel is not a real model id, so it survives.
	writeUIPrefs(t, `{"hiddenModels":{"claude":["fable"]},"assistantModels":{"claude":"recommended"}}`)
	if v, ok := assistantChatModelPref("claude"); !ok || v != chatx.AssistantRecommendedModel {
		t.Fatalf("assistantChatModelPref = %q, %v; want recommended", v, ok)
	}
}
