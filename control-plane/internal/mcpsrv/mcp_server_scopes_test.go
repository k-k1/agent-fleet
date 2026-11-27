package mcpsrv

// Definition scopes on tenant-distributed OAuth servers (ADR 0146 decision 2). The Agent drops a
// distributed definition whose scopes it refuses, and a dropped OAuth definition ends every
// member's sign-in to it on the next fetch, so the admin API must refuse exactly what the Agent
// would. Both rule and wire shape are held to the Agent through files in its testdata.

import (
	"bytes"
	"encoding/json"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

const (
	agentDefScopesCasesPath = "../../../workspace/agent/internal/mcpreg/testdata/def-scopes-cases.json"
	agentTenantDistPath     = "../../../workspace/agent/internal/mcpreg/testdata/cp-tenant-distribution.json"
)

var updateTenantDist = flag.Bool("update-tenant-dist-fixture", false,
	"rewrite the Agent's testdata/cp-tenant-distribution.json from Distribute")

func TestValidDefScopesMatchesTheAgent(t *testing.T) {
	raw, err := os.ReadFile(agentDefScopesCasesPath)
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Cases []struct {
			Name    string   `json:"name"`
			Scopes  []string `json:"scopes"`
			TokenOf int      `json:"tokenOf"`
			Valid   bool     `json:"valid"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &f); err != nil || len(f.Cases) == 0 {
		t.Fatalf("cases: %v (%d)", err, len(f.Cases))
	}
	for _, c := range f.Cases {
		list := append([]string{}, c.Scopes...)
		if c.TokenOf > 0 {
			list = append(list, strings.Repeat("a", c.TokenOf))
		}
		if got := validDefScopes(list); got != c.Valid {
			t.Errorf("%s: validDefScopes = %v, want %v", c.Name, got, c.Valid)
		}
		// Through the API too, so a list the Agent refuses can never be stored.
		b := mcpServerBody{Name: "x", URL: "https://x.example/mcp", Auth: mcpAuthOAuth, Scopes: list}
		if aerr := validateMCPBody(b); (aerr == nil) != c.Valid || (aerr != nil && aerr.Code != codeMCPOAuthScopes) {
			t.Errorf("%s: validateMCPBody = %+v, want valid=%v", c.Name, aerr, c.Valid)
		}
	}
}

func TestValidateMCPBodyRefusesScopesWithoutOAuth(t *testing.T) {
	b := mcpServerBody{Name: "x", URL: "https://x.example/mcp", Scopes: []string{"openid"}}
	if aerr := validateMCPBody(b); aerr == nil || aerr.Code != codeMCPOAuthScopes {
		t.Fatalf("scopes on a static-header server: %+v, want %s", aerr, codeMCPOAuthScopes)
	}
}

func TestValidateMCPBodyElicitation(t *testing.T) {
	for _, tc := range []struct {
		name, auth, elicitation, code string
	}{
		{name: "off by default", auth: mcpAuthOAuth},
		{name: "OAuth opt-in", auth: mcpAuthOAuth, elicitation: mcpElicitationRelay},
		{name: "static server", elicitation: mcpElicitationRelay, code: codeMCPElicitOAuth},
		{name: "unknown mode", auth: mcpAuthOAuth, elicitation: "other", code: codeMCPElicitUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := mcpServerBody{Name: "wiki", URL: "https://wiki.example/mcp", Auth: tc.auth, Elicitation: tc.elicitation}
			err := validateMCPBody(b)
			if tc.code == "" {
				if err != nil {
					t.Fatalf("validate = %+v, want success", err)
				}
			} else if err == nil || err.Status != http.StatusBadRequest || err.Code != tc.code {
				t.Fatalf("validate = %+v, want 400 %s", err, tc.code)
			}
		})
	}
}

// Scopes and tool confirmations round-trip through the admin API, store and distribution.
func TestAdminUpsertStoresAndDistributesOAuthOptions(t *testing.T) {
	a, ctx := newMCPServerAPITest(t, true)
	tn := store.Tenant{ID: "tenant-a", Slug: "a"}
	cp := a.cp.(testCP)
	cp.admin = &tn
	a = NewServerAPI(cp)
	upsert := func(method, id string, b mcpServerBody) *httptest.ResponseRecorder {
		b.TenantSlug = tn.Slug
		raw, _ := json.Marshal(b)
		r := httptest.NewRequest(method, "/api/admin/mcp-servers/"+id, strings.NewReader(string(raw)))
		r.SetPathValue("id", id)
		w := httptest.NewRecorder()
		a.AdminUpsert(w, r)
		return w
	}
	distribute := func() []distDef {
		rec := httptest.NewRecorder()
		a.Distribute(rec, httptest.NewRequest(http.MethodGet, "/internal/mcp-servers", nil),
			store.MembershipView{MembershipID: "m1", TenantID: tn.ID})
		var out struct {
			Servers []distDef `json:"servers"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("distribute: %v %s", err, rec.Body)
		}
		return out.Servers
	}
	scopes := []string{"https://www.googleapis.com/auth/gmail.readonly", "openid"}
	gmail := mcpServerBody{Name: "gmail", URL: "https://gmailmcp.googleapis.com/mcp/v1self", Auth: mcpAuthOAuth,
		Scopes: scopes, Elicitation: mcpElicitationRelay, Targets: mcpTargets{Session: true}, Enabled: true}
	w := upsert(http.MethodPost, "", gmail)
	if w.Code != http.StatusOK {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	var created mcpServerBody
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	if !reflect.DeepEqual(created.Scopes, scopes) || created.Elicitation != mcpElicitationRelay {
		t.Fatalf("upsert answered scopes %q and elicitation %q", created.Scopes, created.Elicitation)
	}
	row, ok, err := a.store.GetMCPServer(ctx, tn.ID, created.ID)
	if err != nil || !ok || row.OAuthScopes != strings.Join(scopes, " ") {
		t.Fatalf("stored %q (%v %v)", row.OAuthScopes, ok, err)
	}
	rec := httptest.NewRecorder()
	a.AdminList(rec, httptest.NewRequest(http.MethodGet, "/api/admin/mcp-servers?tenant=a", nil))
	if !strings.Contains(rec.Body.String(), `"scopes":["https://www.googleapis.com/auth/gmail.readonly","openid"]`) ||
		!strings.Contains(rec.Body.String(), `"elicitation":"relay"`) {
		t.Fatalf("list: %s", rec.Body)
	}
	if d := distribute(); len(d) != 1 || !reflect.DeepEqual(d[0].Scopes, scopes) || d[0].Elicitation != mcpElicitationRelay {
		t.Fatalf("distributed %+v", d)
	}

	// An edit to another non-empty list replaces it, order kept.
	other := []string{"openid", "https://www.googleapis.com/auth/drive.readonly", "email"}
	changed := gmail
	changed.Scopes = other
	if w := upsert(http.MethodPut, created.ID, changed); w.Code != http.StatusOK {
		t.Fatalf("change: %d %s", w.Code, w.Body)
	}
	if row, _, _ := a.store.GetMCPServer(ctx, tn.ID, created.ID); row.OAuthScopes != strings.Join(other, " ") {
		t.Fatalf("stored after change %q", row.OAuthScopes)
	}
	rec = httptest.NewRecorder()
	a.AdminList(rec, httptest.NewRequest(http.MethodGet, "/api/admin/mcp-servers?tenant=a", nil))
	if !strings.Contains(rec.Body.String(), `"scopes":["openid","https://www.googleapis.com/auth/drive.readonly","email"]`) {
		t.Fatalf("list after change: %s", rec.Body)
	}
	if d := distribute(); len(d) != 1 || !reflect.DeepEqual(d[0].Scopes, other) || d[0].Elicitation != mcpElicitationRelay {
		t.Fatalf("distributed after change %+v", d)
	}
	scopes = other
	gmail = changed

	// A refused list leaves the stored one alone.
	bad := gmail
	bad.Scopes = []string{"read,archive"}
	if w := upsert(http.MethodPut, created.ID, bad); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), codeMCPOAuthScopes) {
		t.Fatalf("bad scopes: %d %s", w.Code, w.Body)
	}
	if d := distribute(); len(d) != 1 || !reflect.DeepEqual(d[0].Scopes, scopes) {
		t.Fatalf("after a refused edit: %+v", d)
	}

	// Replacing the definition with these options omitted clears them while keeping OAuth.
	none := gmail
	none.Scopes = nil
	none.Elicitation = ""
	if w := upsert(http.MethodPut, created.ID, none); w.Code != http.StatusOK {
		t.Fatalf("clear: %d %s", w.Code, w.Body)
	}
	if d := distribute(); len(d) != 1 || d[0].Scopes != nil || d[0].Elicitation != "" || d[0].Auth != mcpAuthOAuth {
		t.Fatalf("after clearing: %+v", d)
	}

	// Taking OAuth off clears the stored scopes: a static-header body cannot carry any.
	if w := upsert(http.MethodPut, created.ID, gmail); w.Code != http.StatusOK {
		t.Fatalf("restore: %d %s", w.Code, w.Body)
	}
	if row, ok, err := a.store.GetMCPServer(ctx, tn.ID, created.ID); err != nil || !ok || row.OAuthScopes != strings.Join(scopes, " ") {
		t.Fatalf("after restoring scopes to an empty list: %q (%v %v)", row.OAuthScopes, ok, err)
	}
	static := gmail
	static.Auth, static.Scopes, static.Elicitation = "", nil, ""
	if w := upsert(http.MethodPut, created.ID, static); w.Code != http.StatusOK {
		t.Fatalf("to static: %d %s", w.Code, w.Body)
	}
	row, ok, err = a.store.GetMCPServer(ctx, tn.ID, created.ID)
	if err != nil || !ok || row.Name != "gmail" || row.OAuthScopes != "" || row.Auth != "" || row.Elicitation != "" {
		t.Fatalf("after leaving OAuth: auth=%q scopes=%q elicitation=%q (%v %v)", row.Auth, row.OAuthScopes, row.Elicitation, ok, err)
	}
}

// A row that holds scopes but is no longer OAuth (only a direct database edit makes one) is
// distributed and listed without them: the Agent would drop the whole definition, and an admin
// client sending the listed row back would be refused.
func TestDistributeSendsScopesOnlyForOAuth(t *testing.T) {
	a, ctx := newMCPServerAPITest(t, false)
	row := store.MCPServerRow{ID: "s1", TenantID: "t", Name: "wiki", Transport: "http", URL: "https://wiki.example/mcp",
		Targets: "session", Enabled: true, OAuthScopes: "openid", CreatedAt: store.NowTS(), UpdatedAt: store.NowTS()}
	if err := a.store.CreateMCPServer(ctx, row); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	a.Distribute(rec, httptest.NewRequest(http.MethodGet, "/internal/mcp-servers", nil), store.MembershipView{TenantID: "t"})
	var out struct {
		Servers []map[string]json.RawMessage `json:"servers"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
		t.Fatalf("distribute: %d %s", rec.Code, rec.Body)
	}
	if len(out.Servers) != 1 || string(out.Servers[0]["id"]) != `"s1"` {
		t.Fatalf("want s1 alone, got %s", rec.Body)
	}
	if _, ok := out.Servers[0]["scopes"]; ok {
		t.Fatalf("a static-header row was sent with scopes: %s", rec.Body)
	}
	if b := rowToBody(row, nil); b.Scopes != nil {
		t.Fatalf("a static-header row is listed with scopes %q", b.Scopes)
	}
}

// TestDistributeMatchesTheAgentFixture renders Distribute for an OAuth row next to a static-header
// row and compares it with the file the Agent's TestFetchTenantKeepsDistributedOAuthOptions feeds
// to FetchTenant. A field renamed or dropped on either
// side fails one of the two.
func TestDistributeMatchesTheAgentFixture(t *testing.T) {
	a, ctx := newMCPServerAPITest(t, false)
	secretEnc, _, err := a.sealHeaders(ctx, "t", map[string]string{oauthSecretKey: "s3cret"})
	if err != nil {
		t.Fatal(err)
	}
	headersEnc, _, err := a.sealHeaders(ctx, "t", map[string]string{"Authorization": "Bearer wiki"})
	if err != nil {
		t.Fatal(err)
	}
	rows := []store.MCPServerRow{
		{ID: "t-gmail", TenantID: "t", Name: "gmail", Label: "Gmail", Transport: "http",
			URL: "https://gmailmcp.googleapis.com/mcp/v1self", Targets: "assistant,session", Enabled: true,
			Auth: mcpAuthOAuth, OAuthIssuer: "https://accounts.google.com", OAuthClientID: "corp-agent",
			OAuthSecretEnc: secretEnc, OAuthScopes: "https://www.googleapis.com/auth/gmail.readonly openid", Elicitation: mcpElicitationRelay},
		{ID: "t-wiki", TenantID: "t", Name: "wiki", Transport: "http", URL: "https://wiki.corp.example/mcp",
			HeadersEnc: headersEnc, Targets: "session", Kinds: "claude,codex", TimeoutMS: 30000, Enabled: true},
	}
	for _, r := range rows {
		r.CreatedAt, r.UpdatedAt = store.NowTS(), store.NowTS()
		if err := a.store.CreateMCPServer(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	rec := httptest.NewRecorder()
	a.Distribute(rec, httptest.NewRequest(http.MethodGet, "/internal/mcp-servers", nil), store.MembershipView{TenantID: "t"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, rec.Body.Bytes(), "", "  "); err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(pretty.String()) + "\n"
	if *updateTenantDist {
		if err := os.WriteFile(agentTenantDistPath, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(agentTenantDistPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != got {
		t.Fatalf("Distribute no longer matches %s (rerun with -update-tenant-dist-fixture, then run the Agent's "+
			"TestFetchTenantKeepsDistributedOAuthOptions):\n got: %s\nwant: %s", agentTenantDistPath, got, want)
	}
}
