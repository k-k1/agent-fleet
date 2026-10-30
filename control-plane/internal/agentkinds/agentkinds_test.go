package agentkinds

import (
	"slices"
	"strings"
	"testing"
)

// The snapshot's equality with the Agent's registry is held on the Agent side
// (TestKindCatalogSnapshot); this pins what the CP relies on when it reads it.
func TestSnapshotShape(t *testing.T) {
	all := All()
	var kinds []string
	for _, e := range all {
		kinds = append(kinds, e.Kind)
		for field, v := range map[string]string{"mcpClient": e.MCPClient, "instructions": e.Instructions} {
			if v != "yes" && v != "no" && v != "unverified" {
				t.Errorf("%s.%s = %q, want yes / no / unverified", e.Kind, field, v)
			}
		}
	}
	if !slices.IsSorted(kinds) || len(slices.Compact(slices.Clone(kinds))) != len(kinds) {
		t.Errorf("kinds = %v, want sorted and unique", kinds)
	}
	for _, k := range []string{"claude", "codex", "lcpp", "muse", "shell"} {
		if !slices.Contains(kinds, k) {
			t.Errorf("snapshot lacks %q", k)
		}
	}
	mcp := MCPClientKinds()
	if !mcp["claude"] || !mcp["muse"] || !mcp["lcpp"] || mcp["shell"] || mcp["ssm"] {
		t.Errorf("MCPClientKinds() = %v", mcp)
	}
}

// The set of kinds that start managed by default is the hand list the CP kept before it was
// derived (codex opencode copilot cursor kiro) plus lcpp and muse, which have no Terminal route
// and which the Agent defaults to managed anyway. Pinned as a literal on purpose: a kind added to
// the registry with ManagedDefault, or one dropped, changes this set and must be decided here,
// not slip through a derivation.
func TestManagedDefaultKindsAreTheOldListPlusManagedOnly(t *testing.T) {
	oldList := []string{"codex", "opencode", "copilot", "cursor", "kiro"}
	managedOnly := []string{"lcpp", "muse"}
	want := map[string]bool{}
	for _, k := range append(slices.Clone(oldList), managedOnly...) {
		want[k] = true
	}
	got := ManagedDefaultKinds()
	if len(got) != len(want) {
		t.Errorf("ManagedDefaultKinds() = %v, want %v", got, want)
	}
	for k := range want {
		if !got[k] {
			t.Errorf("%q is missing from ManagedDefaultKinds() = %v", k, got)
		}
	}
	// Exact names only: nothing but a registered spelling may be in the set.
	for _, k := range []string{"", "claude", "agy", "shell", "ssm", "Codex", " codex", "kiro-cli",
		"cursor-agent", "github-copilot", "llamacpp", "llama.cpp", "Muse", "antigravity"} {
		if got[k] {
			t.Errorf("%q must not get the managed default", k)
		}
	}
	// Every managed-only kind is a managed default: it has nothing else.
	for _, e := range All() {
		if e.ManagedOnly && !e.ManagedDefault {
			t.Errorf("%s: managedOnly without managedDefault", e.Kind)
		}
	}
}

// CheckCreateKind takes exactly the snapshot's kinds and the empty kind, and its refusal lists
// every accepted kind.
func TestCheckCreateKind(t *testing.T) {
	for _, e := range All() {
		if err := CheckCreateKind(e.Kind); err != nil {
			t.Errorf("CheckCreateKind(%q) = %v", e.Kind, err)
		}
	}
	if err := CheckCreateKind(""); err != nil {
		t.Errorf("CheckCreateKind(\"\") = %v, want nil (claude)", err)
	}
	for _, k := range []string{"Codex", " codex", "kiro-cli", "llamacpp"} {
		err := CheckCreateKind(k)
		if err == nil {
			t.Errorf("CheckCreateKind(%q) = nil, want a refusal", k)
			continue
		}
		for _, e := range All() {
			if !strings.Contains(err.Error(), e.Kind) {
				t.Errorf("CheckCreateKind(%q) = %q, does not list %q", k, err, e.Kind)
			}
		}
	}
}
