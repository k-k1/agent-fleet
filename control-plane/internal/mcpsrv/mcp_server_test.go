package mcpsrv

// Tenant-distributed MCP servers (docs/log/48 P4 + ADR0031).
//
// The tests worth having here are the ones a code review cannot guarantee:
//   - a tenant definition can never become a stdio one (決定 2)
//   - a masked header round-trips without destroying the stored credential, and
//     user_secret actually DISCARDS values rather than only hiding them
//   - the distribution face is scoped by the token's tenant, and never ships a row it
//     could not decrypt
//   - the store scopes every statement by tenant, so a leaked id is not a cross-tenant read

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

func newMCPServerAPITest(t *testing.T, withKey bool) (ServerAPI, context.Context) {
	t.Helper()
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	cp := testCP{store: st}
	if withKey {
		cp.master32 = []byte("0123456789abcdef0123456789abcdef")
		cp.custodian = newTestCustodian(cp.master32)
	}
	return NewServerAPI(cp), ctx
}

// --- validation --------------------------------------------------------------------

func TestValidateMCPBodyRefusesStdio(t *testing.T) {
	// ADR0031 決定 2. The table has no command columns either, so this is the second of
	// three refusals (the third is the agent re-validating what it receives).
	aerr := validateMCPBody(mcpServerBody{Name: "x", Transport: "stdio", URL: "https://x.example/mcp"})
	if aerr == nil || aerr.Code != codeMCPTenantStdio {
		t.Fatalf("stdio must be refused with %s, got %+v", codeMCPTenantStdio, aerr)
	}
}

func TestValidateMCPBodyRules(t *testing.T) {
	ok := mcpServerBody{Name: "corp-wiki", URL: "https://wiki.corp.example/mcp"}
	if aerr := validateMCPBody(ok); aerr != nil {
		t.Fatalf("valid body refused: %+v", aerr)
	}
	cases := []struct {
		name string
		body mcpServerBody
		code string
	}{
		{"bad name", mcpServerBody{Name: "bad name", URL: "https://x/y"}, codeMCPNameInvalid},
		{"reserved name", mcpServerBody{Name: "af", URL: "https://x/y"}, codeMCPNameReserved},
		{"no url", mcpServerBody{Name: "x"}, codeMCPURLRequired},
		{"bad scheme", mcpServerBody{Name: "x", URL: "ftp://x/y"}, codeMCPURLScheme},
		// Credentials in the URL would land in every member's materialized config in plain
		// sight, where the masking contract cannot reach them.
		{"userinfo", mcpServerBody{Name: "x", URL: "https://u:p@x/y"}, codeMCPURLCredentials},
		{"header newline", mcpServerBody{Name: "x", URL: "https://x/y", Headers: map[string]string{"A": "b\nc"}}, codeMCPHeaderValue},
		{"header colon", mcpServerBody{Name: "x", URL: "https://x/y", Headers: map[string]string{"A:B": "c"}}, codeMCPHeaderName},
		{"unknown kind", mcpServerBody{Name: "x", URL: "https://x/y", Kinds: []string{"emacs"}}, codeMCPKindUnknown},
		{"timeout", mcpServerBody{Name: "x", URL: "https://x/y", TimeoutMS: 10}, codeMCPTimeoutRange},
		{"transport", mcpServerBody{Name: "x", Transport: "sse", URL: "https://x/y"}, codeMCPTransport},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			aerr := validateMCPBody(c.body)
			if aerr == nil || aerr.Code != c.code {
				t.Fatalf("want %s, got %+v", c.code, aerr)
			}
		})
	}
}

// --- secret handling ---------------------------------------------------------------

func TestMergeHeadersRoundTrip(t *testing.T) {
	stored := map[string]string{"Authorization": "Bearer real", "X-Team": "sre"}
	got := mergeHeaders(map[string]string{"Authorization": MaskedValue, "X-Team": "platform"}, stored)
	if got["Authorization"] != "Bearer real" {
		t.Fatalf("masked value must keep the stored credential, got %q", got["Authorization"])
	}
	if got["X-Team"] != "platform" {
		t.Fatalf("a typed value must replace the stored one, got %q", got["X-Team"])
	}
	// A key absent from the incoming map is a deletion.
	if _, ok := mergeHeaders(map[string]string{"X-Team": "sre"}, stored)["Authorization"]; ok {
		t.Fatal("an omitted header must be deleted")
	}
	// Masked with nothing behind it must be DROPPED, never stored: the literal "***"
	// would otherwise be sent to the MCP server as if it were a credential.
	if _, ok := mergeHeaders(map[string]string{"New": MaskedValue}, stored)["New"]; ok {
		t.Fatal("a masked value with no stored counterpart must be dropped")
	}
}

func TestStripValuesAndMask(t *testing.T) {
	in := map[string]string{"Authorization": "Bearer real"}
	if v := stripValues(in)["Authorization"]; v != "" {
		t.Fatalf("user_secret must keep the NAME and drop the value, got %q", v)
	}
	if v := maskHeaders(in)["Authorization"]; v != MaskedValue {
		t.Fatalf("a stored value must be masked, got %q", v)
	}
	// An empty value stays empty: it is not a secret being withheld, it is one nobody has
	// entered. Masking it would tell the admin a credential is stored when none is.
	if v := maskHeaders(map[string]string{"Authorization": ""})["Authorization"]; v != "" {
		t.Fatalf("an unset value must not be masked, got %q", v)
	}
}

func TestSealHeadersRoundTrip(t *testing.T) {
	a, ctx := newMCPServerAPITest(t, true)
	in := map[string]string{"Authorization": "Bearer real"}
	enc, keyRef, err := a.sealHeaders(ctx, "tenant-a", in)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if keyRef != "tenant-a" {
		t.Fatalf("keyRef must name the tenant key, got %q", keyRef)
	}
	if enc == "" || enc == `{"Authorization":"Bearer real"}` {
		t.Fatalf("headers must not be stored in the clear when a master key exists: %q", enc)
	}
	out, err := a.openHeaders(ctx, enc, keyRef)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if out["Authorization"] != "Bearer real" {
		t.Fatalf("round trip lost the value: %+v", out)
	}
	// The keyRef is the AEAD's AAD, so a row cannot be read under another tenant's key.
	if _, err := a.openHeaders(ctx, enc, "tenant-b"); err == nil {
		t.Fatal("a row sealed for tenant-a must not open under tenant-b")
	}
}

func TestSealHeadersPlaintextWithoutMasterKey(t *testing.T) {
	// Dev / no master key: the same degradation the Agent's secret store makes, rather
	// than refusing to work at all.
	a, ctx := newMCPServerAPITest(t, false)
	enc, keyRef, err := a.sealHeaders(ctx, "tenant-a", map[string]string{"A": "b"})
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if keyRef != "" {
		t.Fatalf("no custodian means no key ref, got %q", keyRef)
	}
	out, err := a.openHeaders(ctx, enc, keyRef)
	if err != nil || out["A"] != "b" {
		t.Fatalf("plaintext round trip failed: %+v %v", out, err)
	}
}

func TestOpenHeadersWithoutCustodianIsAnError(t *testing.T) {
	// A row sealed under a tenant key, read by a process whose master key is gone. Must
	// name the problem, not panic and not silently return an empty header set.
	a, ctx := newMCPServerAPITest(t, false)
	if _, err := a.openHeaders(ctx, "some-ciphertext", "tenant-a"); err == nil {
		t.Fatal("a sealed row with no custodian must be an error")
	}
}

// --- targets / kinds encoding ------------------------------------------------------

func TestTargetsAndKindsEncoding(t *testing.T) {
	if got := joinTargets(mcpTargets{Assistant: true, Session: true}); got != "assistant,session" {
		t.Fatalf("joinTargets: %q", got)
	}
	if got := splitTargets("session"); got.Session != true || got.Assistant != false {
		t.Fatalf("splitTargets: %+v", got)
	}
	// Both off is a legal staging state (stored, distributed to nothing).
	if got := splitTargets(""); got.Assistant || got.Session {
		t.Fatalf("empty targets must be all-off: %+v", got)
	}
	if got := splitKinds("claude, codex"); len(got) != 2 || got[0] != "claude" || got[1] != "codex" {
		t.Fatalf("splitKinds: %+v", got)
	}
	if got := splitKinds(""); got != nil {
		t.Fatalf("no kinds must mean nil (= every kind), got %+v", got)
	}
}

// --- store scoping -----------------------------------------------------------------

func TestMCPServerStoreIsTenantScoped(t *testing.T) {
	a, ctx := newMCPServerAPITest(t, false)
	row := store.MCPServerRow{
		ID: "s1", TenantID: "tenant-a", Name: "wiki", Transport: "http",
		URL: "https://wiki/mcp", Targets: "assistant,session", Enabled: true,
		CreatedAt: store.NowTS(), UpdatedAt: store.NowTS(),
	}
	if err := a.store.CreateMCPServer(ctx, row); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Another tenant holding the id must see nothing and must not be able to change or
	// delete it. The id travels to the Console and into every member's cache, so it is
	// not a secret — tenant scoping is what actually isolates the row.
	if _, ok, err := a.store.GetMCPServer(ctx, "tenant-b", "s1"); err != nil || ok {
		t.Fatalf("cross-tenant get must miss: ok=%v err=%v", ok, err)
	}
	if err := a.store.DeleteMCPServer(ctx, "tenant-b", "s1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok, _ := a.store.GetMCPServer(ctx, "tenant-a", "s1"); !ok {
		t.Fatal("a cross-tenant delete must not remove the row")
	}
	hijack := row
	hijack.TenantID, hijack.Name = "tenant-b", "hijacked"
	if err := a.store.UpdateMCPServer(ctx, hijack); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _, _ := a.store.GetMCPServer(ctx, "tenant-a", "s1")
	if got.Name != "wiki" {
		t.Fatalf("a cross-tenant update must not rewrite the row, got %q", got.Name)
	}

	rows, err := a.store.ListMCPServers(ctx, "tenant-b")
	if err != nil || len(rows) != 0 {
		t.Fatalf("cross-tenant list must be empty: %d %v", len(rows), err)
	}
}

func TestMCPServerStoreRoundTripsFlags(t *testing.T) {
	a, ctx := newMCPServerAPITest(t, false)
	row := store.MCPServerRow{
		ID: "s1", TenantID: "t", Name: "wiki", Transport: "http", URL: "https://wiki/mcp",
		Targets: "session", Kinds: "claude,codex", TimeoutMS: 30000,
		Enabled: false, UserSecret: true, CreatedBy: "admin@example.com",
		CreatedAt: store.NowTS(), UpdatedAt: store.NowTS(),
	}
	if err := a.store.CreateMCPServer(ctx, row); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, ok, err := a.store.GetMCPServer(ctx, "t", "s1")
	if err != nil || !ok {
		t.Fatalf("get: %v %v", ok, err)
	}
	// The 0/1 INTEGER columns do not coerce to Go bools on their own — that is what b2i
	// and the scan exist for, and getting either backwards silently inverts the flag.
	if got.Enabled || !got.UserSecret {
		t.Fatalf("bool columns round-tripped wrong: enabled=%v user_secret=%v", got.Enabled, got.UserSecret)
	}
	if got.TimeoutMS != 30000 || got.Kinds != "claude,codex" || got.CreatedBy != "admin@example.com" {
		t.Fatalf("row round-tripped wrong: %+v", got)
	}
}

// --- distribution face -------------------------------------------------------------

func TestDistributeScopesAndStrips(t *testing.T) {
	a, ctx := newMCPServerAPITest(t, true)
	seal := func(tenant string, h map[string]string) (string, string) {
		enc, ref, err := a.sealHeaders(ctx, tenant, h)
		if err != nil {
			t.Fatalf("seal: %v", err)
		}
		return enc, ref
	}
	encA, refA := seal("tenant-a", map[string]string{"Authorization": "Bearer tenant-a"})
	encSecret, refSecret := seal("tenant-a", map[string]string{"Authorization": ""})
	encB, refB := seal("tenant-b", map[string]string{"Authorization": "Bearer tenant-b"})

	rows := []store.MCPServerRow{
		{ID: "a1", TenantID: "tenant-a", Name: "wiki", Transport: "http", URL: "https://wiki/mcp",
			HeadersEnc: encA, KeyRef: refA, Targets: "assistant,session", Enabled: true},
		{ID: "a2", TenantID: "tenant-a", Name: "tickets", Transport: "http", URL: "https://tickets/mcp",
			HeadersEnc: encSecret, KeyRef: refSecret, Targets: "session", Enabled: true, UserSecret: true},
		{ID: "a3", TenantID: "tenant-a", Name: "off", Transport: "http", URL: "https://off/mcp",
			Targets: "session", Enabled: false},
		// Corrupt ciphertext (a key rotation, a bad restore). Must be counted, not shipped —
		// a server that authenticates with nothing sends the member off debugging the MCP
		// server instead of the key configuration.
		{ID: "a4", TenantID: "tenant-a", Name: "broken", Transport: "http", URL: "https://broken/mcp",
			HeadersEnc: "bm90LWEtdmFsaWQtY2lwaGVydGV4dA==", KeyRef: "tenant-a", Targets: "session", Enabled: true},
		{ID: "b1", TenantID: "tenant-b", Name: "other", Transport: "http", URL: "https://other/mcp",
			HeadersEnc: encB, KeyRef: refB, Targets: "session", Enabled: true},
	}
	for _, r := range rows {
		r.CreatedAt, r.UpdatedAt = store.NowTS(), store.NowTS()
		if err := a.store.CreateMCPServer(ctx, r); err != nil {
			t.Fatalf("create %s: %v", r.ID, err)
		}
	}

	rec := httptest.NewRecorder()
	a.Distribute(rec, httptest.NewRequest(http.MethodGet, "/internal/mcp-servers", nil),
		store.MembershipView{MembershipID: "m1", TenantID: "tenant-a"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Servers    []distDef `json:"servers"`
		Unreadable int       `json:"unreadable"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Unreadable != 1 {
		t.Fatalf("the undecryptable row must be counted, got %d", out.Unreadable)
	}
	byName := map[string]distDef{}
	for _, d := range out.Servers {
		byName[d.Name] = d
	}
	if len(byName) != 2 {
		t.Fatalf("want wiki + tickets only, got %+v", byName)
	}
	if _, ok := byName["off"]; ok {
		t.Fatal("a disabled row must not be distributed")
	}
	if _, ok := byName["other"]; ok {
		t.Fatal("another tenant's row must never be distributed")
	}
	if got := byName["wiki"]; got.Headers["Authorization"] != "Bearer tenant-a" || got.Origin != "tenant" {
		t.Fatalf("wiki: %+v", got)
	}
	if got := byName["tickets"]; !got.UserSecret || got.Headers["Authorization"] != "" {
		t.Fatalf("a user_secret row must carry the header NAME with no value: %+v", got)
	}
	// The agent decodes this straight into mcpreg.ServerDef, so enabled must be true —
	// the CP only distributes enabled rows and the local opt-out is applied in the agent.
	if !byName["wiki"].Enabled {
		t.Fatal("distributed rows must arrive enabled")
	}
}
