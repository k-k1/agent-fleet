package codex

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/routepolicy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
)

// codex's API key opens openai-api: refused while the policy forbids it, before anything is
// written; a device login on a forbidden ChatGPT route is not started either.
func TestCodexCredentialEntryUnderThePolicy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := routepolicy.Open(filepath.Join(t.TempDir(), "rp.json"), "")
	if _, err := s.Accept(routepolicy.Policy{Revision: 2, Restricted: true, Allowed: []string{routes.RouteAnthropicAPI}}); err != nil {
		t.Fatal(err)
	}
	old := routepolicy.Active
	routepolicy.Active = func() *routepolicy.Store { return s }
	t.Cleanup(func() { routepolicy.Active = old })
	rec := httptest.NewRecorder()
	HandleAPIKey(rec, httptest.NewRequest(http.MethodPost, "/connections/codex/api-key", strings.NewReader(`{"key":"sk-test"}`)))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), routes.CodePolicyForbidden) {
		t.Errorf("api key: %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	HandleDeviceStart(rec, httptest.NewRequest(http.MethodPost, "/connections/codex/device/start", nil))
	if rec.Code != http.StatusForbidden {
		t.Errorf("device login: %d %s", rec.Code, rec.Body.String())
	}
}
