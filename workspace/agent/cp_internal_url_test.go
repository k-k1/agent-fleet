package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/awsx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/branchrule"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/bridge"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// With AF_CP_INTERNAL_URL injected (ADR 0106 decision 8), every request the Agent sends to
// the CP goes to the workspace listener, and the public base — unreachable here, as it is
// from inside a cluster — is used only for links a person opens.
func TestCPRequestsGoToTheInternalURL(t *testing.T) {
	withAgentHome(t)
	var mu sync.Mutex
	hits := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[r.Method+" "+r.URL.Path] = true
		mu.Unlock()
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	t.Setenv("AF_CP_BASE_URL", "http://127.0.0.1:1")
	t.Setenv("AF_CP_INTERNAL_URL", srv.URL+"/")
	for _, k := range []string{"AF_DOCS_TOKEN", "AF_MCP_TOKEN", "AF_BRANCH_RULES_TOKEN", "AF_AWS_PROFILES_TOKEN",
		"AF_SCHEDULE_TOKEN", "AF_ENGINE_ISSUE_TOKEN", "AF_GIT_OAUTH_TOKEN"} {
		t.Setenv(k, "tok")
	}

	_, _ = fetchWorkspaceDocs(t.TempDir())
	_, _ = mcpreg.FetchTenant()
	_, _ = branchrule.FetchTenant(context.Background(), filepath.Join(t.TempDir(), "rules.json"))
	_, _, _ = awsx.Fetch()
	_, _ = mcpx.CPScheduleDo(http.MethodGet, "/internal/schedules", nil)
	engineCPCall(context.Background(), http.MethodGet, "/internal/engine/catalog", nil, &struct{}{})
	for _, want := range []string{
		"GET /internal/docs", "GET /internal/mcp-servers", "GET /internal/branch-rules",
		"GET /internal/aws-profiles", "GET /internal/schedules", "GET /internal/engine/catalog",
	} {
		if !hits[want] {
			t.Errorf("%s did not reach the internal URL (hits: %v)", want, hits)
		}
	}

	// The cred helper's refresh bridge is seeded with the request base.
	seedGitOAuthBridge()
	s, err := secrets.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if s.GitOAuthBridge == nil || s.GitOAuthBridge.BaseURL != srv.URL {
		t.Errorf("git OAuth bridge = %+v, want base %q", s.GitOAuthBridge, srv.URL)
	}

	// A link for a person keeps the public base.
	msg := bridge.Message{Kind: "exit", SessionName: "s1"}
	if txt := msg.Text("en"); !strings.Contains(txt, "http://127.0.0.1:1/?session=s1") || strings.Contains(txt, srv.URL) {
		t.Errorf("notification link = %q, want the public base only", txt)
	}
}
