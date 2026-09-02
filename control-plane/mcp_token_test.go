// mcp_token_test.go — AF_MCP_TOKEN の往復検査。実体（MintMCPToken / VerifyMCPToken）は
// internal/mcpsrv にあるが、**このテストは package main に残す**（並列リファクタ ウェーブ C /
// track=CP-MCP）。
//
// 🔥 理由は bad リストの最後の 1 件: 「別ブリッジの資格情報を拒む」を、schedule ブリッジの
// **本物の mintScheduleToken** で作っている。mcpsrv からは package main のそれを呼べないので、
// 向こうへ持っていくと文字列リテラルに退化する——つまり「schedule 側が prefix と署名ドメインを
// MCP と衝突させた」という現実の壊れ方を検出できなくなる（memo ブリッジは既に同じ `afm_`
// prefix を使っており、両者を隔てているのは署名ドメイン文字列だけ）。テスト関数の本体は
// develop と 1 文字も変えていない。

package main

import (
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/mcpsrv"
)

func TestMCPTokenRoundTrip(t *testing.T) {
	key := mcpsrv.MCPSignKey([]byte("0123456789abcdef0123456789abcdef"))
	tok := mcpsrv.MintMCPToken(key, "membership-1")
	// Deterministic, so re-injection on every container start is idempotent.
	if tok != mcpsrv.MintMCPToken(key, "membership-1") {
		t.Fatal("the token must be deterministic per membership")
	}
	mid, ok := mcpsrv.VerifyMCPToken(key, tok)
	if !ok || mid != "membership-1" {
		t.Fatalf("verify: %q %v", mid, ok)
	}
	bad := []string{
		"", "nope", "afm_", "afm_bad.tag",
		mcpsrv.MintMCPToken(key, "membership-1") + "x",                                                 // tampered tag
		mintScheduleToken(scheduleSignKey([]byte("0123456789abcdef0123456789abcdef")), "membership-1"), // wrong credential
	}
	for _, b := range bad {
		if _, ok := mcpsrv.VerifyMCPToken(key, b); ok {
			t.Fatalf("must reject %q", b)
		}
	}
	// A different master key must not validate: the MCP token is its own credential, so a
	// memo/schedule token leak grants nothing here (and vice versa).
	if _, ok := mcpsrv.VerifyMCPToken(mcpsrv.MCPSignKey([]byte("ffffffffffffffffffffffffffffffff")), tok); ok {
		t.Fatal("a token from another deployment key must be rejected")
	}
}
