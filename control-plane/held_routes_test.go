package main

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// #1151: the Agent holds these routes open with a heartbeat while a model answers, and the
// buffered REST relay would sit on those bytes until the ingress idle timeout cut the call. So
// the heartbeat has to reach the browser while the Agent is still working: the fake Agent below
// only answers after the test has read its keepalive through the CP.
func TestHeldRoutesRelayTheHeartbeatBeforeTheAnswer(t *testing.T) {
	// One release per request: the Agent answers only once the test has seen the keepalive.
	release := make(chan struct{}, 1)
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
		_, _ = io.WriteString(w, `data: {"status":200,"body":{"path":"`+r.URL.Path+`"}}`+"\n\n")
	}))
	defer agent.Close()
	cp := heldTestCP(t, agent.URL)

	paths := []string{
		"/api/chat/conversations/c1/compact",
		"/api/chat/conversations/c1/plan/refresh",
		"/api/chat/ask",
		"/api/fs/suggest-edit",
	}
	for _, path := range paths {
		t.Run(strings.TrimPrefix(path, "/api/"), func(t *testing.T) {
			resp := postHeld(t, cp.URL+path)
			defer resp.Body.Close()
			buf := make([]byte, 64)
			n, err := resp.Body.Read(buf)
			if err != nil || string(buf[:n]) != ": keepalive\n\n" {
				t.Fatalf("status %d: first bytes %q, %v; want the keepalive while the Agent still works",
					resp.StatusCode, buf[:n], err)
			}
			release <- struct{}{}
			rest, err := io.ReadAll(resp.Body)
			want := `data: {"status":200,"body":{"path":"` + strings.TrimPrefix(path, "/api") + `"}}` + "\n\n"
			if err != nil || string(rest) != want {
				t.Fatalf("after the keepalive: %q, %v; want the final frame %q", rest, err, want)
			}
		})
	}
}

// A stream the Agent cuts after the status went out is a 200 in the access log, so the relay
// has to say why the final frame never came.
func TestHeldRouteLogsAnAgentStreamCutShort(t *testing.T) {
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, ": keepalive\n\n")
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	}))
	defer agent.Close()
	cp := heldTestCP(t, agent.URL)

	var logs strings.Builder
	var mu sync.Mutex
	prev := log.Writer()
	log.SetOutput(writerFunc(func(p []byte) (int, error) { mu.Lock(); defer mu.Unlock(); return logs.Write(p) }))
	t.Cleanup(func() { log.SetOutput(prev) })

	resp := postHeld(t, cp.URL+"/api/chat/ask")
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	deadline := time.Now().Add(3 * time.Second)
	for {
		mu.Lock()
		got := logs.String()
		mu.Unlock()
		if strings.Contains(got, "agent stream proxy: POST /api/chat/ask: body read:") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no log line for the cut stream; logs: %q", got)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func postHeld(t *testing.T, url string) *http.Response {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// heldTestCP is the real route table in front of one member whose workspace is the given Agent.
func heldTestCP(t *testing.T, agentURL string) *httptest.Server {
	t.Helper()
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
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
		rts:             map[string]cachedRT{membership.ID: {rt: stubRuntime{endpoint: agentURL, token: "tok"}, ws: workspace}},
		store:           st,
		authMode:        "dev",
		devUser:         "held-user",
		provisionMode:   "auto",
		defaultTenantID: tenant.ID,
		conns:           newConnRegistry(),
	}
	// Same isolation as smokeEnv: the caller's environment must not add routes or leave
	// auth exemptions behind for later tests.
	setRouteSwitches(t)
	restoreAuthExemptions(t)
	cp := httptest.NewServer(buildMux(config{consoleDir: t.TempDir(), mgr: mgr, egressDedup: &egressAuditDedup{}}))
	t.Cleanup(cp.Close)
	return cp
}
