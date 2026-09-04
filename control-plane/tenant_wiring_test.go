package main

// `cpTenant` in tenant_wiring.go (the implementation of `tenantsrv.CP`) is 33 adapters that
// each delegate to the real thing in a single line. Neither the type checker nor
// `var _ tenantsrv.CP = cpTenant{}` sees more than "it exists", so swapping two methods of
// the same type sets nothing off.
//
// Measured: all three of these mutations passed with the whole CP suite green.
//
//   - swap the bodies of `EvictMembershipCache` and `EvictTenantCache` (both string → ())
//   - make `InvalidateTenantLogin` a no-op (a tenant's login settings stay stale)
//   - swap `StopWorkspaceByMembership` and `CleanHomeByMembership` — pressing "stop" wipes
//     the home, the most expensive mix-up of the lot
//
// Caught by the same two checks as the sibling family (workspace/agent's git_wiring_test.go
// / mcp_wiring_test.go), with one difference: there the members are function fields on
// `Deps`, so function-pointer identity is observable, whereas these are struct methods that
// cannot be compared as pointers. In place of identity, check which real thing each one
// delegates to, by name:
//
//  1. TestCPTenantAdaptersDelegateToMatchingTarget — the AST of each of the 33 bodies must
//     mention the expected real name (a swap changes the referenced name, hence red).
//  2. TestCPTenantWiringCheckCoversInterface — matches `tenantsrv.CP`'s method set against
//     the table used by 1 (a new method without a new entry is red).
//
// The expected values in 1 hold the real spellings as strings. Apply mutation testing to
// the implementation (tenant_wiring.go) ONLY: rewriting the literals in the check as well
// repairs the implementation and the check condition together, and both sides go green
// (README §4, pitfall 3).

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/tenantsrv"
)

// cpTenantDelegates maps each cpTenant method to the name of the real thing it must
// reach. Same-signature neighbours are the ones a swap can hide in, so they are
// listed adjacently on purpose: reading the two columns side by side is the review.
var cpTenantDelegates = map[string]string{
	"Store":                        "store",
	"KnownProviderIDs":             "knownProviderIDs",
	"EvictMembershipCache":         "evictMembershipCache", // ↕ same type, swappable
	"EvictTenantCache":             "evictTenantCache",     // ↕
	"InvalidateTenantLogin":        "invalidate",
	"IdleForecastFor":              "idleForecastFor",
	"WorkspaceSizing":              "workspaceSizing",
	"IsSystemTenantSlug":           "isSystemTenantSlug",
	"SanitizeUser":                 "sanitizeUser",
	"SplitCSVLower":                "splitCSVLower",  // ↕ same type (string → []string)
	"SplitDomainCSV":               "splitDomainCSV", // ↕
	"JoinCSV":                      "joinCSV",
	"TrustedProxyHops":             "trustedProxyHops",
	"IPInAny":                      "ipInAny",
	"DomainMatches":                "domainMatches",
	"WorkspaceLifecycleLeaseError": "workspaceLifecycleLeaseError",
	"MembershipsFor":               "membershipsFor",
	"CountRunningInTenant":         "countRunningInTenant",
	"WorkspaceStateByMembership":   "workspaceStateByMembership",
	"StopWorkspaceByMembership":    "stopWorkspaceByMembership",    // ↕ same type; swap = wiped home
	"CleanHomeByMembership":        "cleanHomeByMembership",        // ↕
	"DestroyWorkspaceByMembership": "destroyWorkspaceByMembership", // ↕
	"ResolveWorkspaceSize":         "resolveWorkspaceSize",
	"ResolveSlotClass":             "resolveSlotClass",
	"PoolBudget":                   "poolBudget",
	"PoolStatus":                   "poolStatus",
	"TenantAdminFor":               "tenantAdminFor",
	"ResolveMember":                "resolveMember",
	"ClientIPFrom":                 "clientIPFrom",
	"ParseCIDRList":                "parseCIDRList",
	"ParseLimits":                  "parseLimits",
	"LimitsFor":                    "GetTenant",
	"StoreTenantLimits":            "SetTenantLimits",
}

// bodyNames returns every identifier and selector-field name mentioned in a body.
// Fields (`d.m.store`) and package-level variables (`trustedProxyHops`) are reached
// without a call, so collecting names rather than call targets keeps all 33 uniform.
func bodyNames(fd *ast.FuncDecl) map[string]bool {
	names := map[string]bool{}
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.Ident:
			names[n.Name] = true
		case *ast.SelectorExpr:
			names[n.Sel.Name] = true
		}
		return true
	})
	return names
}

// cpTenantMethods parses tenant_wiring.go and returns its cpTenant methods.
func cpTenantMethods(t *testing.T) map[string]*ast.FuncDecl {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "tenant_wiring.go", nil, 0)
	if err != nil {
		t.Fatalf("tenant_wiring.go を読めない: %v", err)
	}
	out := map[string]*ast.FuncDecl{}
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Recv == nil || len(fd.Recv.List) != 1 || fd.Body == nil {
			continue
		}
		id, ok := fd.Recv.List[0].Type.(*ast.Ident)
		if !ok || id.Name != "cpTenant" {
			continue
		}
		out[fd.Name.Name] = fd
	}
	return out
}

func TestCPTenantAdaptersDelegateToMatchingTarget(t *testing.T) {
	methods := cpTenantMethods(t)
	// Close off the "found none, therefore checked nothing" shape first.
	if len(methods) == 0 {
		t.Fatal("tenant_wiring.go に cpTenant のメソッドを 1 本も見つけられなかった＝この検査が無言化している")
	}
	for name, want := range cpTenantDelegates {
		fd, ok := methods[name]
		if !ok {
			t.Errorf("cpTenant.%s が tenant_wiring.go に無い（改名したなら表も直すこと）", name)
			continue
		}
		if !bodyNames(fd)[want] {
			t.Errorf("cpTenant.%s が %q を参照していない＝別の本物へ繋がっている。"+
				"同じ型の隣と入れ替わっていないか見ること（実際に参照しているのは %s）",
				name, want, strings.Join(sortedNames(bodyNames(fd)), " "))
		}
	}
}

func TestCPTenantWiringCheckCoversInterface(t *testing.T) {
	iface := reflect.TypeOf((*tenantsrv.CP)(nil)).Elem()
	if iface.NumMethod() == 0 {
		t.Fatal("tenantsrv.CP のメソッドを 1 本も読めなかった＝この検査が無言化している")
	}
	var missing []string
	inIface := map[string]bool{}
	for i := 0; i < iface.NumMethod(); i++ {
		n := iface.Method(i).Name
		inIface[n] = true
		if _, ok := cpTenantDelegates[n]; !ok {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("tenantsrv.CP に %v が増えたのに cpTenantDelegates に無い。"+
			"アダプタを足したら、どの本物へ繋ぐのかをここに書くこと", missing)
	}
	var stale []string
	for n := range cpTenantDelegates {
		if !inIface[n] {
			stale = append(stale, n)
		}
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		t.Errorf("cpTenantDelegates の %v は tenantsrv.CP に無い（消えたなら表からも消すこと）", stale)
	}
}

func sortedNames(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
