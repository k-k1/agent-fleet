package mcpx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// The envelope carries the issuer when a refusal concerns one: the Console needs it to ask for
// a client id, and the Control Plane's callback redirect to name the registration path.
func TestWriteOAuthErrCarriesIssuerAndRegistration(t *testing.T) {
	w := httptest.NewRecorder()
	writeOAuthErr(w, &mcpreg.OAuthError{
		Status: http.StatusConflict, Code: mcpreg.OAuthErrClientIDNeeded, Message: "enter one",
		Issuer: "https://as.example.com", Registration: "dcr",
	})
	var out struct{ Error map[string]string }
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if w.Code != 409 || out.Error["code"] != "client_id_needed" || out.Error["issuer"] != "https://as.example.com" || out.Error["registration"] != "dcr" {
		t.Errorf("%d %s", w.Code, w.Body.String())
	}
}

func TestOAuthRoutesAnswerForUnknownServers(t *testing.T) {
	for name, h := range map[string]http.HandlerFunc{
		"status": HandleOAuthStatus, "disconnect": HandleOAuthDisconnect,
	} {
		r := httptest.NewRequest(http.MethodGet, "/mcp-servers/mcp-nope/oauth", nil)
		r.SetPathValue("id", "mcp-nope")
		w := httptest.NewRecorder()
		h(w, r)
		if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "mcp_not_found") {
			t.Errorf("%s: %d %s", name, w.Code, w.Body.String())
		}
	}
	r := httptest.NewRequest(http.MethodPost, "/mcp-servers/mcp-nope/oauth/start", strings.NewReader(`{"state":"state-0123456789abcdef","redirectUri":"https://af.example.com/oauth2/mcp/callback"}`))
	r.SetPathValue("id", "mcp-nope")
	w := httptest.NewRecorder()
	HandleOAuthStart(w, r)
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "mcp_not_found") {
		t.Errorf("start: %d %s", w.Code, w.Body.String())
	}
}

// Leaving OAuth (or changing the URL) ends the sign-in: the grant is a credential for that
// one server.
func TestUpdateAwayFromOAuthDropsTheGrant(t *testing.T) {
	d, err := mcpreg.Create(mcpreg.ServerDef{Name: "hosted", Transport: mcpreg.TransportHTTP, URL: "https://m.example.com/mcp", Auth: mcpreg.AuthOAuth, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mcpreg.Delete(d.ID) })
	plant := func() {
		if err := secrets.Update(func(s *secrets.Data) error {
			s.MCPOAuth = append(s.MCPOAuth, secrets.MCPOAuthGrant{ServerID: d.ID, Issuer: "https://as.example.com", AccessToken: "x"})
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	plant()
	put := func(body string) int {
		r := httptest.NewRequest(http.MethodPut, "/mcp-servers/"+d.ID, strings.NewReader(body))
		r.SetPathValue("id", d.ID)
		w := httptest.NewRecorder()
		HandleServerUpdate(w, r)
		return w.Code
	}
	grants := func() int {
		s, _ := secrets.Load()
		n := 0
		for _, g := range s.MCPOAuth {
			if g.ServerID == d.ID {
				n++
			}
		}
		return n
	}
	if c := put(`{"name":"hosted","transport":"http","url":"https://m.example.com/mcp","auth":"oauth","enabled":true}`); c != 200 || grants() != 1 {
		t.Fatalf("an unchanged OAuth server keeps its grant: %d, grants=%d", c, grants())
	}
	if c := put(`{"name":"hosted","transport":"http","url":"https://m.example.com/other","auth":"oauth","enabled":true}`); c != 200 || grants() != 0 {
		t.Fatalf("a new URL must drop the grant, OAuth or not: %d, grants=%d", c, grants())
	}
	plant()
	if c := put(`{"name":"hosted","transport":"http","url":"https://m.example.com/other","enabled":true}`); c != 200 || grants() != 0 {
		t.Fatalf("leaving OAuth must drop the grant: %d, grants=%d", c, grants())
	}
	// A grant left on a server that is not OAuth is dropped by a URL change too.
	plant()
	if c := put(`{"name":"hosted","transport":"http","url":"https://m.example.com/third","enabled":true}`); c != 200 || grants() != 0 {
		t.Fatalf("a URL change must drop a stray grant: %d, grants=%d", c, grants())
	}
}

// A sign-in's end is what takes an OAuth server out of every kind's config, so the disconnect
// route must re-materialize. The CLI's own config is what the member's next session reads.
func TestDisconnectRemovesTheServerFromTheKindConfigs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude-cfg"))
	stop, err := mcpreg.StartRelay()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	d, err := mcpreg.Create(mcpreg.ServerDef{Name: "hosted", Transport: mcpreg.TransportHTTP, URL: "https://m.example.com/mcp",
		Auth: mcpreg.AuthOAuth, Enabled: true, Targets: mcpreg.Targets{Session: true}})
	if err != nil {
		t.Fatal(err)
	}
	if err := secrets.Update(func(s *secrets.Data) error {
		s.MCPOAuth = append(s.MCPOAuth, secrets.MCPOAuthGrant{ServerID: d.ID, Issuer: "https://as.example.com", AccessToken: "x"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(home, "claude-cfg", ".claude.json")
	MaterializeAll()
	if b, _ := os.ReadFile(cfg); !strings.Contains(string(b), "/mcp/"+d.ID) {
		t.Fatalf("a signed-in server is not in claude's config:\n%s", b)
	}
	r := httptest.NewRequest(http.MethodDelete, "/mcp-servers/"+d.ID+"/oauth", nil)
	r.SetPathValue("id", d.ID)
	w := httptest.NewRecorder()
	HandleOAuthDisconnect(w, r)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if b, _ := os.ReadFile(cfg); strings.Contains(string(b), d.ID) {
		t.Fatalf("a disconnected server is still in claude's config:\n%s", b)
	}
}
