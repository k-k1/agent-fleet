package mcpx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpreg"
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
