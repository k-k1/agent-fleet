package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// #1151: the Agent holds these routes open with a heartbeat while a model answers, and the
// buffered REST relay would sit on those bytes until the ingress idle timeout cut the call. So
// the heartbeat has to reach the browser while the Agent is still working: the fake Agent below
// only answers after the test has read its keepalive through the CP.
func TestHeldRoutesRelayTheHeartbeatBeforeTheAnswer(t *testing.T) {
	release := make(chan struct{})
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, ": keepalive\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_, _ = io.WriteString(w, `data: {"status":200,"body":{}}`+"\n\n")
	}))
	defer agent.Close()

	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	tenant, _ := st.EnsureDefaultTenant(ctx)
	ident, _ := st.UpsertIdentity(ctx, "", "held-user", "")
	membership, _ := st.EnsureMembership(ctx, ident.ID, tenant.ID, "member")
	workspace := store.Workspace{ID: "ws-held", TenantID: tenant.ID, MembershipID: membership.ID,
		ContainerName: "held", Network: "n", DataDir: "d", AgentPort: "1", AgentToken: "t", State: "running", CreatedAt: store.NowTS()}
	if err := st.CreateWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	mgr := &manager{
		rts:             map[string]cachedRT{membership.ID: {rt: stubRuntime{endpoint: agent.URL, token: "tok"}, ws: workspace}},
		store:           st,
		authMode:        "dev",
		devUser:         "held-user",
		provisionMode:   "auto",
		defaultTenantID: tenant.ID,
		conns:           newConnRegistry(),
	}
	cp := httptest.NewServer(buildMux(config{consoleDir: t.TempDir(), mgr: mgr, egressDedup: &egressAuditDedup{}}))
	defer cp.Close()

	paths := []string{
		"/api/chat/conversations/c1/compact",
		"/api/chat/conversations/c1/plan/refresh",
		"/api/chat/ask",
		"/api/fs/suggest-edit",
	}
	for _, path := range paths {
		t.Run(strings.TrimPrefix(path, "/api/"), func(t *testing.T) {
			reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			req, _ := http.NewRequestWithContext(reqCtx, http.MethodPost, cp.URL+path, strings.NewReader(`{}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "text/event-stream")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			buf := make([]byte, 64)
			n, err := resp.Body.Read(buf)
			if err != nil || string(buf[:n]) != ": keepalive\n\n" {
				t.Fatalf("status %d: first bytes %q, %v; want the keepalive while the Agent still works",
					resp.StatusCode, buf[:n], err)
			}
		})
	}
	close(release)
}
