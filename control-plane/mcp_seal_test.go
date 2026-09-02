// mcp_seal_test.go — テナント配布 MCP サーバのヘッダが**本当に封をされて**保存されるか。
//
// 🔥 この検査が package main に居る理由（レビュー B-1・2026-09-03）。実体は
// internal/mcpsrv にあるが、封をするのは CP の localCustodian（custodian.go・package
// main）で、mcpsrv 単体のテストはそれを構築できず作り物を挿している。作り物は seam の
// 4 つの主張（平文でない / 往復する / keyRef が認証される / 壊れた行はエラー）を守るが、
// **「符号化しただけ」と「暗号化した」を区別できない**——custodian を一切呼ばずに
// base64(keyRef + NUL + 平文 JSON) を保存する変異が、mcpsrv 側では緑のまま通る。
// 守っているのはテナントの `Authorization: Bearer …` なので、抜けると資格情報が
// 平文相当で DB に入る方向になる。
//
// そこで **cpDeps 経由で実 custodian を配線した 1 本**をこちら側に置く。(B) で
// TestMCPTokenRoundTrip を main に残したのと同じ理由・同じ手。
package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

const mcpSealSecret = "Bearer super-secret-tenant-credential"

// upsertMCPServer drives the real admin face (POST /api/admin/mcp-servers), so the
// row under test travels the same sealHeaders path a deployment uses.
func upsertMCPServer(t *testing.T, api mcpServerAPI, slug, name string) {
	t.Helper()
	body := `{"tenant_slug":"` + slug + `","name":"` + name + `","url":"https://wiki.example/mcp",` +
		`"headers":{"Authorization":"` + mcpSealSecret + `"},"targets":{"session":true},"enabled":true}`
	rec := httptest.NewRecorder()
	api.adminUpsert(rec, httptest.NewRequest(http.MethodPost, "/api/admin/mcp-servers", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("upsert %s: %d %s", name, rec.Code, rec.Body.String())
	}
}

func TestMCPServerHeadersAreSealedNotMerelyEncoded(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	tn, err := st.CreateTenant(ctx, "acme", "Acme")
	if err != nil {
		t.Fatalf("tenant: %v", err)
	}

	// AUTH=dev の固定利用者は super_admin なので tenantAdminFor を通る。
	master := sha256.Sum256([]byte("test-master"))
	m := &manager{store: st, authMode: "dev", devUser: "admin"}
	m.master32 = master[:]
	m.custodian = newLocalCustodian(m.master32)
	api := newMCPServerAPI(m)

	upsertMCPServer(t, api, "acme", "wiki")
	rows, err := st.ListMCPServers(ctx, tn.ID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("list: %d %v", len(rows), err)
	}
	enc := rows[0].HeadersEnc
	if rows[0].KeyRef != tn.ID {
		t.Fatalf("the row must be sealed under the tenant key, got %q", rows[0].KeyRef)
	}

	// ① 保存値からは、どの素朴な復号でも平文が出てこないこと。生の部分文字列だけを見ると
	//    「base64 で包んだ平文」を見逃す——実際そういう変異が作り物側では緑になる。
	probes := map[string][]byte{"raw": []byte(enc)}
	for name, dec := range map[string]func(string) ([]byte, error){
		"base64.Std":    base64.StdEncoding.DecodeString,
		"base64.RawStd": base64.RawStdEncoding.DecodeString,
		"base64.URL":    base64.URLEncoding.DecodeString,
		"base64.RawURL": base64.RawURLEncoding.DecodeString,
	} {
		if b, err := dec(enc); err == nil {
			probes[name] = b
		}
	}
	for name, b := range probes {
		if strings.Contains(string(b), mcpSealSecret) {
			t.Fatalf("the stored value reveals the credential under %s: the headers are encoded, not encrypted", name)
		}
	}

	// ② 同じヘッダを 2 回封じたら違う値になること。AEAD は毎回新しい nonce を引くので、
	//    **決定的な符号化はすべてここで落ちる**（①をすり抜ける細工も含めて）。
	upsertMCPServer(t, api, "acme", "wiki2")
	rows, err = st.ListMCPServers(ctx, tn.ID)
	if err != nil || len(rows) != 2 {
		t.Fatalf("list 2: %d %v", len(rows), err)
	}
	if rows[0].HeadersEnc == rows[1].HeadersEnc {
		t.Fatal("two seals of the same header map are byte-identical: this is a deterministic encoding, not an AEAD")
	}

	// ③ それでも往復すること（①②が「壊れているから読めない」で通ってしまわないように）。
	rec := httptest.NewRecorder()
	api.distribute(rec, httptest.NewRequest(http.MethodGet, "/internal/mcp-servers", nil),
		store.MembershipView{MembershipID: "m1", TenantID: tn.ID})
	if rec.Code != http.StatusOK {
		t.Fatalf("distribute: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), mcpSealSecret) {
		t.Fatalf("the sealed header must come back out for the member's agent: %s", rec.Body.String())
	}
}
