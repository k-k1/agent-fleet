// wiremap_convert_test.go — equivalence proofs for the map sites converted in mcpx
// (CONTRACT-MAP / leg 3).
//
// The wire types are unexported, so equivalence can only be measured from inside this
// package. The harness is internal/wiretest, shared machinery imported only by tests.
package mcpx

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/wiretest"
)

type mcpRegistryIn struct {
	Servers         []mcpServerWire
	TenantFetchedAt int64
	Shadowed        []string
}

func TestWireEquivMCPRegistry(t *testing.T) {
	inputs := []mcpRegistryIn{
		{Servers: []mcpServerWire{}, TenantFetchedAt: 1756800000, Shadowed: []string{"Tickets"}},
		// Shadowed can be nil (`null`); normalising it to an empty slice would make it `[]`,
		// which is a different value. The harness prepends the all-nil zero case itself, so
		// what these measure is the shapes where nil and empty are mixed.
		{Servers: []mcpServerWire{}, TenantFetchedAt: 0, Shadowed: nil},
		{Servers: nil, TenantFetchedAt: 0, Shadowed: []string{}},
	}
	got := wiretest.AssertEquiv(t, "HandleServersGet", inputs,
		func(in mcpRegistryIn) any { // old (a copy of the map literal in mcp_servers.go)
			return map[string]any{
				"servers":         in.Servers,
				"tenantFetchedAt": in.TenantFetchedAt,
				"shadowed":        in.Shadowed,
			}
		},
		func(in mcpRegistryIn) any {
			return mcpRegistryWire{
				Servers: in.Servers, TenantFetchedAt: in.TenantFetchedAt, Shadowed: in.Shadowed,
			}
		})
	t.Logf("comparison mode: %s", got)
}
