// mcp_seal_test.go — do the headers of a tenant-distributed MCP server reach the DB
// genuinely sealed?
//
// The feature itself lives in internal/mcpsrv, but the sealing is done by the CP's
// localCustodian (custodian.go, package main), which an mcpsrv-only test cannot build and
// therefore replaces with a fake. That fake upholds the seam's four claims (not
// plaintext / round-trips / keyRef is authenticated / a corrupt row errors) yet cannot
// tell "merely encoded" from "encrypted": a mutation storing base64(keyRef + NUL +
// plaintext JSON) without calling the custodian at all stays green over there. What is
// being protected is the tenant's `Authorization: Bearer …`, so the gap puts credentials
// into the DB in effectively plaintext. Hence one test on this side, wired to the real
// custodian through cpDeps.
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

	"github.com/k-k1/agent-fleet/control-plane/internal/mcpsrv"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

const mcpSealSecret = "Bearer super-secret-tenant-credential"

// upsertMCPServer drives the real admin face (POST /api/admin/mcp-servers), so the
// row under test travels the same sealHeaders path a deployment uses.
func upsertMCPServer(t *testing.T, api mcpsrv.ServerAPI, slug, name string) {
	t.Helper()
	body := `{"tenant_slug":"` + slug + `","name":"` + name + `","url":"https://wiki.example/mcp",` +
		`"headers":{"Authorization":"` + mcpSealSecret + `"},"targets":{"session":true},"enabled":true}`
	rec := httptest.NewRecorder()
	api.AdminUpsert(rec, httptest.NewRequest(http.MethodPost, "/api/admin/mcp-servers", strings.NewReader(body)))
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

	// Under AUTH=dev the fixed user is a super_admin, so tenantAdminFor lets this through.
	master := sha256.Sum256([]byte("test-master"))
	m := &manager{store: st, authMode: "dev", devUser: "admin"}
	m.master32 = master[:]
	m.custodian = newLocalCustodian(m.master32)
	api := mcpsrv.NewServerAPI(cpDeps{m})

	upsertMCPServer(t, api, "acme", "wiki")
	rows, err := st.ListMCPServers(ctx, tn.ID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("list: %d %v", len(rows), err)
	}
	enc := rows[0].HeadersEnc
	if rows[0].KeyRef != tn.ID {
		t.Fatalf("the row must be sealed under the tenant key, got %q", rows[0].KeyRef)
	}

	// 1. No naive decoding of the stored value may reveal the plaintext. Checking the raw
	//    substring alone misses plaintext wrapped in base64.
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

	// 2. Sealing the same headers twice must give different bytes. An AEAD draws a fresh
	//    nonce each time, so every deterministic encoding fails here — including one
	//    contrived to slip past check 1.
	upsertMCPServer(t, api, "acme", "wiki2")
	rows, err = st.ListMCPServers(ctx, tn.ID)
	if err != nil || len(rows) != 2 {
		t.Fatalf("list 2: %d %v", len(rows), err)
	}
	if rows[0].HeadersEnc == rows[1].HeadersEnc {
		t.Fatal("two seals of the same header map are byte-identical: this is a deterministic encoding, not an AEAD")
	}

	// 3. And it still round-trips, so checks 1 and 2 cannot pass merely because the value
	//    is unreadable garbage.
	rec := httptest.NewRecorder()
	api.Distribute(rec, httptest.NewRequest(http.MethodGet, "/internal/mcp-servers", nil),
		store.MembershipView{MembershipID: "m1", TenantID: tn.ID})
	if rec.Code != http.StatusOK {
		t.Fatalf("distribute: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), mcpSealSecret) {
		t.Fatalf("the sealed header must come back out for the member's agent: %s", rec.Body.String())
	}
}
