package sessionx

import (
	"reflect"
	"slices"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agentreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/kinds"
)

// The registry moves to internal/agentreg (#2870). Until sessionx's own map is deleted, the
// two must register the same agent type under every key and answer every lookup the same.
func TestAgentregEqualsSessionxRegistry(t *testing.T) {
	if got, want := agentreg.Kinds(), RegisteredKinds(); !slices.Equal(got, want) {
		t.Fatalf("agentreg.Kinds() = %v, sessionx RegisteredKinds() = %v", got, want)
	}
	if got, want := slices.Sorted(slices.Values(kinds.All())), RegisteredKinds(); !slices.Equal(got, want) {
		t.Fatalf("kinds.All() sorted = %v, sessionx RegisteredKinds() = %v", got, want)
	}
	for k, old := range agentRegistry {
		a, ok := agentreg.Lookup(k)
		if !ok {
			t.Errorf("%s: not in agentreg", k)
			continue
		}
		if reflect.TypeOf(a) != reflect.TypeOf(old) {
			t.Errorf("%s: agentreg registers %T, sessionx %T", k, a, old)
		}
		if !reflect.DeepEqual(a.Caps(), old.Caps()) {
			t.Errorf("%s: Caps differ: agentreg %+v, sessionx %+v", k, a.Caps(), old.Caps())
		}
	}
	probes := append(RegisteredKinds(), "", "Claude", " codex", "kiro-cli", "llama.cpp", "pi")
	for _, k := range probes {
		if got, want := reflect.TypeOf(agentreg.Of(k)), reflect.TypeOf(AgentOf(k)); got != want {
			t.Errorf("Of(%q) = %v, AgentOf = %v", k, got, want)
		}
		if got, want := agentreg.Normalize(k), NormalizeKind(k); got != want {
			t.Errorf("Normalize(%q) = %q, NormalizeKind = %q", k, got, want)
		}
		if got, want := agentreg.ManagedDefault(k), ManagedDefaultKind(k); got != want {
			t.Errorf("ManagedDefault(%q) = %v, ManagedDefaultKind = %v", k, got, want)
		}
		if got, want := errString(agentreg.ValidateCreate(k)), errString(ValidateCreateKind(k)); got != want {
			t.Errorf("ValidateCreate(%q) = %q, ValidateCreateKind = %q", k, got, want)
		}
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
