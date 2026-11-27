package mcpsrv

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

func TestWorkspaceMCPPresetBundles(t *testing.T) {
	wantURLs := map[string]string{
		"gmail":     "https://gmailmcp.googleapis.com/mcp/v1self",
		"drive":     "https://drivemcp.googleapis.com/mcp/v1self",
		"docs":      "https://docsmcp.googleapis.com/mcp/v1self",
		"sheets":    "https://sheetsmcp.googleapis.com/mcp/v1self",
		"slides":    "https://slidesmcp.googleapis.com/mcp/v1self",
		"calendar":  "https://calendarmcp.googleapis.com/mcp/v1self",
		"chat":      "https://chatmcp.googleapis.com/mcp/v1self",
		"tasks":     "https://tasksmcp.googleapis.com/mcp/v1self",
		"people":    "https://people.googleapis.com/mcp/v1",
		"classroom": "https://classroommcp.googleapis.com/mcp/v1",
		"search":    "https://workspacemcp.googleapis.com/mcp/v1self",
	}
	all := make([]string, 0, len(wantURLs))
	for id := range wantURLs {
		all = append(all, id)
	}
	preset := workspacePreset("https://af.example.com/")
	if preset.RedirectURI != "https://af.example.com/oauth2/mcp/callback" || len(preset.Services) != len(wantURLs) {
		t.Fatalf("unexpected preset: redirect=%q, services=%d", preset.RedirectURI, len(preset.Services))
	}

	for _, bundle := range []string{"read_only", "standard"} {
		req := workspaceMCPRequest{ClientID: "tenant-agent", ClientSecret: "fake-client-secret", Services: all, Bundle: bundle}
		defs, aerr := req.definitions()
		if aerr != nil || len(defs) != len(wantURLs) {
			t.Fatalf("bundle %s: %d definitions, error %+v", bundle, len(defs), aerr)
		}
		var union []string
		for i, d := range defs {
			s := preset.Services[i]
			if d.Name != s.Name || d.URL != wantURLs[s.ID] || !strings.HasPrefix(d.URL, "https://"+s.Host+"/") {
				t.Errorf("service %s has unexpected definition name or URL: %s %s", s.ID, d.Name, d.URL)
			}
			if aerr := validateMCPBody(d); aerr != nil || len(d.Scopes) == 0 {
				t.Errorf("%s: invalid definition (empty scopes: %v): %+v", s.ID, len(d.Scopes) == 0, aerr)
			}
			for _, scope := range d.Scopes {
				if bundle == "read_only" && !strings.HasSuffix(scope, ".readonly") {
					t.Errorf("%s: read-only requests %s", d.Name, scope)
				}
				switch scope {
				case "https://mail.google.com/", "https://www.googleapis.com/auth/gmail.send", "https://www.googleapis.com/auth/drive":
					t.Errorf("%s: bundle grants %s", d.Name, scope)
				}
				if !slices.Contains(union, scope) {
					union = append(union, scope)
				}
			}
		}
		if !validDefScopes(union) {
			t.Errorf("bundle %s exceeds the shared scope limit: %d bytes", bundle, len(strings.Join(union, " ")))
		}
		const compose = "https://www.googleapis.com/auth/gmail.compose"
		if slices.Contains(union, compose) != (bundle == "standard") {
			t.Errorf("bundle %s has unexpected draft permission", bundle)
		}
	}
}

func TestAdminWorkspaceMCPPresetCreatesOrdinaryDefinitions(t *testing.T) {
	a, ctx := newMCPServerAPITest(t, true)
	tenant := store.Tenant{ID: "tenant-a", Slug: "a"}
	cp := a.cp.(testCP)
	cp.admin = &tenant
	a = NewServerAPI(cp)
	const secret = "fake-client-secret"

	post := func(bundle, clientSecret string, services ...string) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(workspaceMCPRequest{
			TenantSlug: tenant.Slug, ClientID: "tenant-agent", ClientSecret: clientSecret, Services: services, Bundle: bundle,
		})
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		a.AdminCreateWorkspacePreset(w, httptest.NewRequest(http.MethodPost, "/api/admin/mcp-servers/google-workspace", strings.NewReader(string(body))))
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("preset response included the client secret")
		}
		return w
	}
	for _, reply := range []*httptest.ResponseRecorder{
		post("standard", secret),
		post("standard", secret, "gmail", "bad-selection"),
		post("standard", secret, "gmail", "gmail"),
		post("unknown", secret, "gmail"),
		post("standard", MaskedValue, "gmail"),
		post("standard", "", "gmail"),
	} {
		if reply.Code != http.StatusBadRequest {
			t.Fatalf("expected a bad request, got %d: %s", reply.Code, reply.Body)
		}
	}
	if w := post("standard", secret, "gmail", "people"); w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	if w := post("standard", secret, "drive", "people"); w.Code != http.StatusConflict {
		t.Fatalf("repeat: %d %s", w.Code, w.Body)
	}

	rows, err := a.store.ListMCPServers(ctx, tenant.ID)
	if err != nil || len(rows) != 2 {
		t.Fatalf("expected two rows after rejected saves, got %d (%v)", len(rows), err)
	}
	for _, row := range rows {
		if row.OAuthIssuer != googleWorkspaceIssuer || row.OAuthClientID != "tenant-agent" ||
			row.OAuthKeyRef != tenant.ID || row.OAuthSecretEnc == "" || strings.Contains(row.OAuthSecretEnc, secret) {
			t.Fatal("row is missing its sealed Google client")
		}
		opened, err := a.openHeaders(ctx, row.OAuthSecretEnc, row.OAuthKeyRef)
		if err != nil || opened[oauthSecretKey] != secret {
			t.Fatal("sealed client could not be opened")
		}
	}

	list := httptest.NewRecorder()
	a.AdminList(list, httptest.NewRequest(http.MethodGet, "/api/admin/mcp-servers?tenant=a", nil))
	if list.Code != http.StatusOK || strings.Contains(list.Body.String(), secret) ||
		!strings.Contains(list.Body.String(), `"client_secret":"***"`) ||
		!strings.Contains(list.Body.String(), `"google_workspace":`) {
		t.Fatal("list did not expose the catalog and masked definitions")
	}
	dist := httptest.NewRecorder()
	a.Distribute(dist, httptest.NewRequest(http.MethodGet, "/internal/mcp-servers", nil),
		store.MembershipView{MembershipID: "m1", TenantID: tenant.ID})
	var payload struct {
		Servers []distDef `json:"servers"`
	}
	if err := json.Unmarshal(dist.Body.Bytes(), &payload); err != nil ||
		dist.Code != http.StatusOK || len(payload.Servers) != 2 ||
		strings.Contains(dist.Body.String(), secret) || strings.Contains(dist.Body.String(), "client_secret") {
		t.Fatalf("distribution code=%d, rows=%d, decode=%v", dist.Code, len(payload.Servers), err)
	}
	for _, def := range payload.Servers {
		if def.OAuthClient == nil || !def.OAuthClient.Confidential || def.OAuthClient.ClientID != "tenant-agent" ||
			def.Auth != "oauth" || len(def.Scopes) == 0 || !def.Enabled || !def.Targets.Session || !def.Targets.Assistant {
			t.Errorf("definition %s did not carry the preset's OAuth settings", def.Name)
		}
	}
}
