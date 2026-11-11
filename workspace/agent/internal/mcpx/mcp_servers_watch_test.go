package mcpx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// GET /mcp-servers must put the tenant switch on the wire the Console reads: a definite
// true/false once a tenant snapshot exists, no key before that.
func TestServersGetCarriesWatchSources(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SECRET_KEY", "")
	get := func() map[string]json.RawMessage {
		t.Helper()
		w := httptest.NewRecorder()
		HandleServersGet(w, httptest.NewRequest(http.MethodGet, "/mcp-servers", nil))
		if w.Code != 200 {
			t.Fatalf("status %d", w.Code)
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	if _, ok := get()["watchSources"]; ok {
		t.Fatal("watchSources present before any tenant snapshot")
	}
	write := func(body string) {
		t.Helper()
		p := filepath.Join(paths.AgentConfigDir(), "mcp-tenant.json")
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for body, want := range map[string]string{
		`{"fetchedAt":5,"servers":[],"watchSources":true}`: "true",
		`{"fetchedAt":5,"servers":[]}`:                     "false",
	} {
		write(body)
		if got := string(get()["watchSources"]); got != want {
			t.Errorf("%s: watchSources = %q, want %q", body, got, want)
		}
	}
}
