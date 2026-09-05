// wiremap_convert_test.go — equivalence proofs for the map sites converted in tenantsrv
// (CONTRACT-MAP / leg 3).
//
// The wire types are unexported, so equivalence can only be measured from inside this
// package. The harness is internal/wiretest, shared machinery imported only from tests.
package tenantsrv

import (
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/wiretest"
)

// --- Admin.TenantNetwork (Console: NetworkView) ---

type tenantNetworkIn struct {
	Tenant       string
	AllowedCIDRs string
	ProxyHops    int
	YourIP       string
	Editable     bool
	Reason       string
}

func TestWireEquivTenantNetwork(t *testing.T) {
	inputs := []tenantNetworkIn{
		{Tenant: "acme", AllowedCIDRs: "192.0.2.0/24", ProxyHops: 1,
			YourIP: "192.0.2.7", Editable: true, Reason: ""},
		// The old code always writes your_ip as "" first and only overwrites it when
		// info.OK, so the key is always present rather than conditional (the scan's
		// cond mark overestimates it). This case measures the branch where no
		// overwrite happens and "" survives.
		{Tenant: "acme", AllowedCIDRs: "", ProxyHops: 0,
			YourIP: "", Editable: false, Reason: "proxy_not_configured"},
	}
	got := wiretest.AssertEquiv(t, "Admin.TenantNetwork", inputs,
		func(in tenantNetworkIn) any { // old shape (a copy of out in tenant_network.go)
			out := map[string]any{
				"tenant":        in.Tenant,
				"allowed_cidrs": in.AllowedCIDRs,
				"proxy_hops":    in.ProxyHops,
				"your_ip":       "",
				"editable":      in.Editable,
				"reason":        in.Reason,
			}
			if in.YourIP != "" {
				out["your_ip"] = in.YourIP
			}
			return out
		},
		func(in tenantNetworkIn) any {
			return tenantNetworkWire{
				Tenant: in.Tenant, AllowedCIDRs: in.AllowedCIDRs, ProxyHops: in.ProxyHops,
				YourIP: in.YourIP, Editable: in.Editable, Reason: in.Reason,
			}
		})
	t.Logf("comparison mode: %s", got)
}

// --- Admin.SetTenantNetwork (the PUT response; its key set differs from the GET) ---

func TestWireEquivTenantNetworkSaved(t *testing.T) {
	type in struct{ Tenant, Stored string }
	got := wiretest.AssertEquiv(t, "Admin.SetTenantNetwork",
		[]in{{Tenant: "acme", Stored: "192.0.2.0/24"}, {Tenant: "acme", Stored: ""}},
		func(v in) any { // old shape (a copy of tenant_network.go:143)
			return map[string]any{"tenant": v.Tenant, "allowed_cidrs": v.Stored}
		},
		func(v in) any {
			return tenantNetworkSavedWire{Tenant: v.Tenant, AllowedCIDRs: v.Stored}
		})
	t.Logf("comparison mode: %s", got)
}

// --- Admin.TenantSlotClass (Console: MachineView) ---

type tenantSlotClassIn struct {
	Tenant           string
	SlotClass        string
	Classes          []runtime.WorkspaceSlotClass
	DefaultSlotClass string
	Editable         bool
}

func TestWireEquivTenantSlotClass(t *testing.T) {
	inputs := []tenantSlotClassIn{
		{Tenant: "acme", SlotClass: "m1", Classes: []runtime.WorkspaceSlotClass{{ID: "m1"}},
			DefaultSlotClass: "m1", Editable: true},
		// Production builds the slice with make(…, 0, n), so it is never nil and
		// serialises as `[]`. slot_class may be "" (follow the deployment default),
		// but the key stays present.
		{Tenant: "acme", SlotClass: "", Classes: []runtime.WorkspaceSlotClass{},
			DefaultSlotClass: "", Editable: false},
	}
	got := wiretest.AssertEquiv(t, "Admin.TenantSlotClass", inputs,
		func(in tenantSlotClassIn) any { // old shape (a copy of tenant_slot_class.go)
			return map[string]any{
				"tenant":             in.Tenant,
				"slot_class":         in.SlotClass,
				"classes":            in.Classes,
				"default_slot_class": in.DefaultSlotClass,
				"editable":           in.Editable,
			}
		},
		func(in tenantSlotClassIn) any {
			return tenantSlotClassWire{
				Tenant: in.Tenant, SlotClass: in.SlotClass, Classes: in.Classes,
				DefaultSlotClass: in.DefaultSlotClass, Editable: in.Editable,
			}
		})
	t.Logf("comparison mode: %s", got)
}

// --- Admin.SetTenantLogin (Console: TenantLoginFields) ---

type tenantLoginIn struct {
	Tenant, AllowedProviders, AutoJoinDomains, AllowedDomains, HiddenProviders string
}

func TestWireEquivTenantLogin(t *testing.T) {
	inputs := []tenantLoginIn{
		{Tenant: "acme", AllowedProviders: "google,github", AutoJoinDomains: "acme.test",
			AllowedDomains: "acme.test", HiddenProviders: "github"},
		// A CSV field may be empty, which means "no restriction". With omitempty that
		// becomes indistinguishable from "unset", so the key stays present.
		{Tenant: "acme"},
	}
	got := wiretest.AssertEquiv(t, "Admin.SetTenantLogin", inputs,
		func(in tenantLoginIn) any { // old shape (a copy of tenants.go)
			return map[string]any{
				"tenant": in.Tenant, "allowed_providers": in.AllowedProviders,
				"auto_join_domains": in.AutoJoinDomains, "allowed_domains": in.AllowedDomains,
				"hidden_providers": in.HiddenProviders,
			}
		},
		func(in tenantLoginIn) any {
			return tenantLoginWire{
				Tenant: in.Tenant, AllowedProviders: in.AllowedProviders,
				AutoJoinDomains: in.AutoJoinDomains, AllowedDomains: in.AllowedDomains,
				HiddenProviders: in.HiddenProviders,
			}
		})
	t.Logf("comparison mode: %s", got)
}
