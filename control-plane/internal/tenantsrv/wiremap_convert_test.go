// wiremap_convert_test.go — tenantsrv で変換した map サイトの等価証明（CONTRACT-MAP / 脚③）。
//
// 🔴 wire 型は非公開なので、等価はこのパッケージの中でしか測れない。
// ハーネスは internal/wiretest（テストからしか import されない共有機構）。
package tenantsrv

import (
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/wiretest"
)

// --- Admin.TenantNetwork（Console: NetworkView）---

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
		// 🔴 your_ip は旧コードでは **まず "" を必ず入れてから** info.OK なら上書きする形。
		// つまり**常に出るキー**であって条件付きではない（走査の cond 印は過大評価）。
		// 上書きが起きない側＝"" のままを測る。
		{Tenant: "acme", AllowedCIDRs: "", ProxyHops: 0,
			YourIP: "", Editable: false, Reason: "proxy_not_configured"},
	}
	got := wiretest.AssertEquiv(t, "Admin.TenantNetwork", inputs,
		func(in tenantNetworkIn) any { // 旧（tenant_network.go の out の写し）
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
	t.Logf("突き合わせ方式: %s", got)
}

// --- Admin.SetTenantNetwork（PUT の応答。GET とはキー集合が違う）---

func TestWireEquivTenantNetworkSaved(t *testing.T) {
	type in struct{ Tenant, Stored string }
	got := wiretest.AssertEquiv(t, "Admin.SetTenantNetwork",
		[]in{{Tenant: "acme", Stored: "192.0.2.0/24"}, {Tenant: "acme", Stored: ""}},
		func(v in) any { // 旧（tenant_network.go:143 の写し）
			return map[string]any{"tenant": v.Tenant, "allowed_cidrs": v.Stored}
		},
		func(v in) any {
			return tenantNetworkSavedWire{Tenant: v.Tenant, AllowedCIDRs: v.Stored}
		})
	t.Logf("突き合わせ方式: %s", got)
}

// --- Admin.TenantSlotClass（Console: MachineView）---

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
		// 🔴 production は make(…, 0, n) 済みで nil にならない＝`[]` が出る。
		// slot_class は "" を取りうる（配備既定に従う）が、キーは出続ける。
		{Tenant: "acme", SlotClass: "", Classes: []runtime.WorkspaceSlotClass{},
			DefaultSlotClass: "", Editable: false},
	}
	got := wiretest.AssertEquiv(t, "Admin.TenantSlotClass", inputs,
		func(in tenantSlotClassIn) any { // 旧（tenant_slot_class.go の写し）
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
	t.Logf("突き合わせ方式: %s", got)
}

// --- Admin.SetTenantLogin（Console: TenantLoginFields）---

type tenantLoginIn struct {
	Tenant, AllowedProviders, AutoJoinDomains, AllowedDomains, HiddenProviders string
}

func TestWireEquivTenantLogin(t *testing.T) {
	inputs := []tenantLoginIn{
		{Tenant: "acme", AllowedProviders: "google,github", AutoJoinDomains: "acme.test",
			AllowedDomains: "acme.test", HiddenProviders: "github"},
		// 🔴 CSV は空文字を取りうる（＝制限なし）。omitempty を付けると
		// 「制限なし」と「未設定」が区別できなくなるので、キーは出続ける。
		{Tenant: "acme"},
	}
	got := wiretest.AssertEquiv(t, "Admin.SetTenantLogin", inputs,
		func(in tenantLoginIn) any { // 旧（tenants.go の写し）
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
	t.Logf("突き合わせ方式: %s", got)
}
