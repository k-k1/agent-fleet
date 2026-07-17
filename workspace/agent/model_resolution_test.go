package main

import (
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

func TestResolveLiveModelAcceptsShortUniqueFamilyName(t *testing.T) {
	choices := []agents.ModelChoice{
		{ID: "gpt-5.6-terra", Label: "GPT-5.6-Terra"},
		{ID: "gpt-5.6-luna", Label: "GPT-5.6-Luna"},
	}
	got, err := resolveLiveModel("terra", choices)
	if err != nil || got != "gpt-5.6-terra" {
		t.Fatalf("resolveLiveModel(terra) = %q, %v", got, err)
	}
}

func TestResolveLiveModelRejectsUnavailableNameBeforeLaunch(t *testing.T) {
	_, err := resolveLiveModel("not-a-model", []agents.ModelChoice{{ID: "gpt-5.6-terra"}})
	if err == nil || !strings.Contains(err.Error(), "gpt-5.6-terra") {
		t.Fatalf("resolveLiveModel error = %v, want available model in message", err)
	}
}

func TestResolveLiveModelRejectsAmbiguousVersionPrefix(t *testing.T) {
	_, err := resolveLiveModel("gpt-5.6", []agents.ModelChoice{
		{ID: "gpt-5.6-terra"}, {ID: "gpt-5.6-luna"},
	})
	if err == nil || !strings.Contains(err.Error(), "曖昧") || !strings.Contains(err.Error(), "gpt-5.6-terra") {
		t.Fatalf("resolveLiveModel ambiguous error = %v", err)
	}
}
