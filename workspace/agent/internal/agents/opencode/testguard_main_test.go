package opencode

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/testguard"
)

// TestMain keeps every test of this package off the workspace's live tmux server, HOME and
// Agent (testguard). It also answers the first-start migration gate (migrate.go) with an import
// that is already over: the fake opencode 2 binaries and daemons in these tests have nothing to
// migrate, and the real throwaway server would wait out the whole migration timeout for them.
// The gate's own tests (migrate_test.go) install their own.
// realStartMigrationServe is the production starter, for the contract test that needs it.
var realStartMigrationServe = startMigrationServe

func TestMain(m *testing.M) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"completed"}`))
	}))
	defer srv.Close()
	startMigrationServe = func(LaunchSpec) (string, string, <-chan struct{}, func(), error) {
		return srv.URL, "", nil, func() {}, nil
	}
	pinNoHostOpencode()
	code := testguard.Run(m, nil)
	srv.Close()
	os.Exit(code)
}
