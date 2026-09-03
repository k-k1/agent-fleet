package main

// alias_tenant.go の `cpTenant`（`tenantsrv.CP` の実装）は、33 本とも「1 行で本物へ
// 委譲するだけ」のアダプタである。**型の守りも `var _ tenantsrv.CP = cpTenant{}` も
// 「在ること」しか見ない**ので、**同じ型の 2 本を入れ替えても何も鳴らない。**
//
// 🔥 実測（#333 のレビュー）: 次の 3 変異は **CP の全テストが緑のまま通った**。
//
//   - `EvictMembershipCache` と `EvictTenantCache` の中身を入れ替える（どちらも string → ()）
//   - `InvalidateTenantLogin` を no-op にする（テナントのログイン設定が古いまま残る）
//   - `StopWorkspaceByMembership` と `CleanHomeByMembership` を入れ替える
//     （**「停止」を押すと home が消える**——取り違えの中でいちばん高い代償）
//
// 姉妹家系（workspace/agent の git_wiring_test.go / mcp_wiring_test.go）と同じ 2 本立てで
// 止める。ただし**あちらは `Deps` の関数フィールドなので関数ポインタの同一性で見られる**のに対し、
// こちらは**構造体のメソッド**でポインタ比較ができないので、同一性の代わりに
// **「どの本物へ委譲しているか」を名前の対応で**見る:
//
//	① TestCPTenantAdaptersDelegateToMatchingTarget — 33 本それぞれの本体が、
//	   期待した本物の名前を参照していることを AST で見る（入れ替えれば参照名が変わる＝赤）
//	② TestCPTenantWiringCheckCoversInterface       — `tenantsrv.CP` のメソッド集合と
//	   ①の表を突き合わせる（メソッドが増えたのに検査を足さなければ赤）
//
// 🔴 **①の期待値は「本物の綴り」を文字列で握っている。**変異試験を当てるときは
// **実装（alias_tenant.go）だけに当てること**——検査の中のリテラルまで一緒に書き換えると、
// 実装と検査条件が同時に直って両側緑になる（README §4 の落とし穴 3）。

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
	"EvictMembershipCache":         "evictMembershipCache", // ↕ 同じ型・入れ替え可能
	"EvictTenantCache":             "evictTenantCache",     // ↕
	"InvalidateTenantLogin":        "invalidate",
	"IdleForecastFor":              "idleForecastFor",
	"WorkspaceSizing":              "workspaceSizing",
	"IsSystemTenantSlug":           "isSystemTenantSlug",
	"SanitizeUser":                 "sanitizeUser",
	"SplitCSVLower":                "splitCSVLower",  // ↕ 同じ型（string → []string）
	"SplitDomainCSV":               "splitDomainCSV", // ↕
	"JoinCSV":                      "joinCSV",
	"TrustedProxyHops":             "trustedProxyHops",
	"IPInAny":                      "ipInAny",
	"DomainMatches":                "domainMatches",
	"WorkspaceLifecycleLeaseError": "workspaceLifecycleLeaseError",
	"MembershipsFor":               "membershipsFor",
	"CountRunningInTenant":         "countRunningInTenant",
	"WorkspaceStateByMembership":   "workspaceStateByMembership",
	"StopWorkspaceByMembership":    "stopWorkspaceByMembership",    // ↕ 同じ型・#333 の 3 番目
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

// cpTenantMethods parses alias_tenant.go and returns its cpTenant methods.
func cpTenantMethods(t *testing.T) map[string]*ast.FuncDecl {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "alias_tenant.go", nil, 0)
	if err != nil {
		t.Fatalf("alias_tenant.go を読めない: %v", err)
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
	// 🔥 「1 本も見つからなければ何も検査しない」形を先に塞ぐ（#320）。
	if len(methods) == 0 {
		t.Fatal("alias_tenant.go に cpTenant のメソッドを 1 本も見つけられなかった＝この検査が無言化している")
	}
	for name, want := range cpTenantDelegates {
		fd, ok := methods[name]
		if !ok {
			t.Errorf("cpTenant.%s が alias_tenant.go に無い（改名したなら表も直すこと）", name)
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
