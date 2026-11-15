package opencode

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/routepolicy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// A key whose every route the policy forbids is not stored; one with a route allowed is, and so
// is any key while nothing is restricted.
func TestPutConnRefusesAKeyThePolicyForbids(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SECRET_KEY", "")
	s := routepolicy.Open(filepath.Join(t.TempDir(), "rp.json"), "")
	if _, err := s.Accept(routepolicy.Policy{Revision: 2, Restricted: true, Allowed: []string{routes.RouteOpencodeGo}}); err != nil {
		t.Fatal(err)
	}
	old := routepolicy.Active
	routepolicy.Active = func() *routepolicy.Store { return s }
	t.Cleanup(func() { routepolicy.Active = old })
	put := func(env string) int {
		rec := httptest.NewRecorder()
		HandlePutConn(rec, httptest.NewRequest(http.MethodPut, "/connections/opencode", strings.NewReader(`{"env":"`+env+`","key":"k-1"}`)))
		return rec.Code
	}
	if code := put("ANTHROPIC_API_KEY"); code != http.StatusForbidden {
		t.Errorf("a forbidden provider key: %d", code)
	}
	if code := put("MY_CUSTOM_KEY"); code != http.StatusForbidden {
		t.Errorf("a custom key (custom-api) under a restriction: %d", code)
	}
	if code := put(opencodeKeyEnv); code != http.StatusOK {
		t.Errorf("the opencode.ai key, Go allowed: %d", code)
	}
	st, err := secrets.Load()
	if err != nil {
		t.Fatal(err)
	}
	if st.Opencode["ANTHROPIC_API_KEY"] != "" || st.Opencode["MY_CUSTOM_KEY"] != "" || st.Opencode[opencodeKeyEnv] == "" {
		t.Errorf("stored = %v", st.Opencode)
	}
}
