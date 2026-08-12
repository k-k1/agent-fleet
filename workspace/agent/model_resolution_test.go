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

// A requested id that exactly matches one choice must win even when it is also a
// prefix of another (sakana/fugu vs sakana/fugu-ultra vs sakana/fugu-ultra-20260615):
// launching "sakana/fugu" failed with a false "ambiguous" error before this was fixed,
// because the prefix-match pass ran unconditionally and picked up the longer ids too.
func TestResolveLiveModelPrefersExactMatchOverPrefixCollision(t *testing.T) {
	choices := []agents.ModelChoice{
		{ID: "sakana/fugu"}, {ID: "sakana/fugu-ultra"}, {ID: "sakana/fugu-ultra-20260615"},
	}
	got, err := resolveLiveModel("sakana/fugu", choices)
	if err != nil || got != "sakana/fugu" {
		t.Fatalf("resolveLiveModel(sakana/fugu) = %q, %v", got, err)
	}
	got, err = resolveLiveModel("sakana/fugu-ultra", choices)
	if err != nil || got != "sakana/fugu-ultra" {
		t.Fatalf("resolveLiveModel(sakana/fugu-ultra) = %q, %v", got, err)
	}
}
