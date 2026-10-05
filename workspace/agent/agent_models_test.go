package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/modelfallback"
)

// TestAgentModelsClaudeFixedAliases pins the claude branch of /agents/{kind}/models:
// the fixed tier aliases (no live catalog — launch takes --model <alias>), served so
// the MCP list_models resolves claude ids like the other kinds.
func TestAgentModelsClaudeFixedAliases(t *testing.T) {
	writeUIPrefs(t, `{"hiddenModels":{}}`) // saved as "nothing hidden" (unsaved hides fable — see below)
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

func TestAgentModelsClaudeIncludesRegisteredModels(t *testing.T) {
	writeUIPrefs(t, `{"hiddenModels":{},"claudeCustomModels":["claude-opus-4-8"," claude-opus-4-7 ","CLAUDE-OPUS-4-8",42,"opus","bad model"]}`)
	req := httptest.NewRequest(http.MethodGet, "/agents/claude/models", nil)
	req.SetPathValue("kind", "claude")
	rec := httptest.NewRecorder()
	handleAgentModels(rec, req)
	var got struct {
		Models []agents.ModelChoice `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := []string{"fable", "opus", "sonnet", "haiku", "claude-opus-4-8", "claude-opus-4-7"}
	if len(got.Models) != len(want) {
		t.Fatalf("models = %+v, want ids %v", got.Models, want)
	}
	for i, id := range want {
		if got.Models[i].ID != id {
			t.Fatalf("models[%d].id = %q, want %q", i, got.Models[i].ID, id)
		}
	}
}

// An empty menu is the same picture whatever emptied it — "only the default model is
// available" — and until now the Console had to guess a cause out loud ("check the connection
// and the plan") even when the truth was "you excluded them all in settings". emptyReason
// names the outermost step that was already empty, because that is the one to act on.
func TestEmptyReasonNamesTheStepThatEmptiedTheMenu(t *testing.T) {
	for _, c := range []struct {
		name                     string
		enumerated, offered, fin int
		want                     string
	}{
		{"a menu that works says nothing", 8, 8, 8, ""},
		{"…even when the route dropped most of it", 61, 8, 8, ""},
		{"the kind answered with nothing", 0, 0, 0, "catalog_empty"},
		{"a kind that does no shaping answered with nothing", -1, 0, 0, "catalog_empty"},
		{"the billing route dropped every id", 61, 0, 0, "route"},
		{"settings exclude every id that was left", 61, 8, 0, "hidden"},
		{"…and for a kind that does no shaping too", -1, 4, 0, "hidden"},
	} {
		if got := emptyReason(c.enumerated, c.offered, c.fin); got != c.want {
			t.Errorf("%s: emptyReason(%d,%d,%d) = %q, want %q", c.name, c.enumerated, c.offered, c.fin, got, c.want)
		}
	}
}

// A menu that works carries no reason. Worth pinning on the wire rather than only in
// emptyReason: the picker shows the reason instead of the model list, so a reason attached to
// a healthy catalog would replace a working picker with an explanation of a problem nobody
// has. claude is the kind whose catalog cannot be empty for environmental reasons (four fixed
// aliases, no CLI to fail), which is what makes this deterministic in CI.
//
// The empty cases are decided by emptyReason above and exercised there: every kind that can
// reach one needs a CLI this test cannot count on, and claude's own all-hidden fail-safe
// (model_deny.go) deliberately refuses to empty itself.
func TestAgentModelsSaysNothingWhenTheMenuWorks(t *testing.T) {
	writeUIPrefs(t, `{"hiddenModels":{"claude":["fable"]}}`)
	req := httptest.NewRequest(http.MethodGet, "/agents/claude/models", nil)
	req.SetPathValue("kind", "claude")
	rec := httptest.NewRecorder()
	handleAgentModels(rec, req)

	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if models, _ := got["models"].([]any); len(models) != 3 {
		t.Fatalf("models = %v, want the three tiers left after the exclusion", got["models"])
	}
	if _, ok := got["reason"]; ok {
		t.Errorf("reason = %v on a working menu; the picker would show it instead of the models", got["reason"])
	}
}

// When codex's catalog cannot be read (a CLI that is not signed in yet, say), the chat
// recommendation is the pinned modelfallback.ChatCodex, and the answer has to say so:
// list_models is what an assistant reads before naming a model, and an id the account never
// confirmed should not look like one it did. The fake codex fails like a signed-out one; the
// package caches only successful reads, and nothing else in this package reads codex's
// catalog, so the cache is still empty here.
func TestAgentModelsSaysWhenTheRecommendationIsAFallback(t *testing.T) {
	writeUIPrefs(t, `{"hiddenModels":{}}`)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	req := httptest.NewRequest(http.MethodGet, "/agents/codex/models", nil)
	req.SetPathValue("kind", "codex")
	rec := httptest.NewRecorder()
	handleAgentModels(rec, req)
	var got struct {
		Models      []agents.ModelChoice `json:"models"`
		Recommended chatx.RecommendedSet `json:"recommended"`
		FromFB      []string             `json:"recommendedFromFallback"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Models) != 0 {
		t.Fatalf("models = %+v, want none from a codex that cannot list", got.Models)
	}
	if got.Recommended.Chat != modelfallback.ChatCodex {
		t.Fatalf("recommended.chat = %q, want the fallback %q", got.Recommended.Chat, modelfallback.ChatCodex)
	}
	// prose follows the same newest-luna rule as chat, so it lands on the same fallback.
	if strings.Join(got.FromFB, ",") != "chat,prose" {
		t.Fatalf("recommendedFromFallback = %v, want [chat prose]; body = %s", got.FromFB, rec.Body.String())
	}
}

func TestRecommendedFromFallback(t *testing.T) {
	rec := chatx.RecommendedSet{Chat: modelfallback.ChatCodex, Prose: "gpt-test-mini", Short: ""}
	if got := recommendedFromFallback(rec, map[string]bool{}); len(got) != 1 || got[0] != "chat" {
		t.Errorf("unlisted fallback: got %v, want [chat]", got)
	}
	// The same id is not a fallback once the catalog lists it: it was confirmed by the account.
	if got := recommendedFromFallback(rec, map[string]bool{modelfallback.ChatCodex: true}); got != nil {
		t.Errorf("listed fallback id: got %v, want nil", got)
	}
	// A catalog-derived id the catalog does not list (a hidden one, say) is not a fallback either.
	if got := recommendedFromFallback(chatx.RecommendedSet{Short: "sonnet"}, nil); got != nil {
		t.Errorf("non-registry id: got %v, want nil", got)
	}
}
