// wiremap_convert_test.go — mcpx で変換した map サイトの等価証明（CONTRACT-MAP / 脚③）。
//
// 🔴 wire 型は非公開なので、等価はこのパッケージの中でしか測れない。
// ハーネスは internal/wiretest（テストからしか import されない共有機構）。
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
		// 🔴 Shadowed は nil を取りうる（`null`）。空スライスへ正規化すると `[]` になり別物。
		// ゼロ値ケース（全部 nil）はハーネスが先頭に足すので、ここでは
		// 「nil と空が混在する形」を測る。
		{Servers: []mcpServerWire{}, TenantFetchedAt: 0, Shadowed: nil},
		{Servers: nil, TenantFetchedAt: 0, Shadowed: []string{}},
	}
	got := wiretest.AssertEquiv(t, "HandleServersGet", inputs,
		func(in mcpRegistryIn) any { // 旧（mcp_servers.go の map リテラルの写し）
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
	t.Logf("突き合わせ方式: %s", got)
}
