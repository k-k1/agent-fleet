// Package agentkinds is the CP's copy of the Agent's kind catalog (GET /agents/kinds, #1077).
//
// The CP needs kind facts where no Workspace is there to ask — validating a tenant MCP
// definition in the admin API is the first — so it embeds catalog.json, a snapshot the Agent
// module generates from its kind registry and holds equal to it
// (workspace/agent TestKindCatalogSnapshot). Never edit catalog.json by hand: regenerate it with
//
//	cd workspace/agent && go test -run TestKindCatalogSnapshot -update-kind-catalog .
//
// A consumer derives its list from here instead of keeping its own copy of kind names.
package agentkinds

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
)

//go:embed catalog.json
var catalogJSON []byte

// Entry is one kind as the snapshot records it. Tri-state fields hold "yes" / "no" /
// "unverified"; only "yes" means supported, and "unverified" must not be read as "no" by a
// consumer that shows the kind to a user.
type Entry struct {
	Kind           string `json:"kind"`
	ManagedOnly    bool   `json:"managedOnly"`
	ManagedDefault bool   `json:"managedDefault"`
	MCPClient      string `json:"mcpClient"`
	Instructions   string `json:"instructions"`
}

// Yes is the tri-state value that means supported.
const Yes = "yes"

var entries = func() []Entry {
	var c struct {
		Kinds []Entry `json:"kinds"`
	}
	// A snapshot that does not parse is a build defect, not a runtime condition: fail at
	// startup rather than refuse every kind later.
	if err := json.Unmarshal(catalogJSON, &c); err != nil {
		panic("agentkinds: catalog.json: " + err.Error())
	}
	return c.Kinds
}()

// All returns every kind in the snapshot, in its (sorted) order.
func All() []Entry {
	return append([]Entry(nil), entries...)
}

// MCPClientKinds is the set of kinds an MCP definition may be scoped to: mcpClient "yes".
// Matching is exact — the Agent's mcpreg refuses any other spelling, so accepting one here
// would distribute a definition no session receives.
func MCPClientKinds() map[string]bool {
	m := map[string]bool{}
	for _, e := range entries {
		if e.MCPClient == Yes {
			m[e.Kind] = true
		}
	}
	return m
}

// ManagedDefaultKinds is the set of kinds a caller that cannot drive a pane (MCP
// create_session, the scheduler) asks the managed driver for when it names none: the
// snapshot's managedDefault, which already includes the managed-only kinds. Matching is
// exact — the Agent registers a kind under one spelling and falls an unknown one back to
// claude, so a variant spelling must get no driver here, not "managed".
func ManagedDefaultKinds() map[string]bool {
	m := map[string]bool{}
	for _, e := range entries {
		if e.ManagedDefault {
			m[e.Kind] = true
		}
	}
	return m
}

// CheckCreateKind is the create paths' gate, the CP's copy of the Agent's
// sessionx.ValidateCreateKind: an empty kind means claude, a kind in the snapshot passes, and
// anything else is refused with the accepted list. Matching is exact — "Codex" or "kiro-cli" is
// an error, not a spelling of a registered kind — because the Agent would otherwise start a
// claude session for it. The CP asks here so a doomed create does not first wake a stopped
// Workspace; the Agent refuses the same kinds on its own.
func CheckCreateKind(kind string) error {
	if kind == "" {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.Kind == kind {
			return nil
		}
		names = append(names, e.Kind)
	}
	return fmt.Errorf("unknown kind %q; accepted kinds: %s (empty means claude)", kind, strings.Join(names, ", "))
}
