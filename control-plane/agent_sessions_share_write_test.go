package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// TestInboxLaunchInvalidatesSharedSessions: the inbox launch is registered through withMembership,
// which used to skip the invalidation, so the next list read served the list from before the
// launch (#3256).
func TestInboxLaunchInvalidatesSharedSessions(t *testing.T) {
	e := newInboxEnv(t)
	var reads atomic.Int64
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/sessions":
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"name":"s-1","dir":"/home/dev/repos/app","repo":"app"}`)
		case r.Method == "GET" && r.URL.Path == "/sessions":
			reads.Add(1)
			_, _ = io.WriteString(w, sharedOneSession)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(agent.Close)
	ws := store.Workspace{ID: "ws-b", TenantID: e.t1.ID, MembershipID: e.members["b"], ContainerName: "b",
		Network: "n", DataDir: "/d", AgentPort: "1", AgentToken: "tok", State: "running", CreatedAt: store.NowTS()}
	if err := e.st.CreateWorkspace(context.Background(), ws); err != nil {
		t.Fatal(err)
	}
	rt := stubRuntime{endpoint: agent.URL, token: "tok"}
	e.mgr.rts[e.members["b"]] = cachedRT{rt: rt, ws: ws}

	h := e.mgr.invalidatesSessionsOnWrite(e.mux)
	id := e.send("a", "request", "b", "c")
	_ = e.do("b", "POST", "/api/inbox/items/"+id+"/claim", nil)

	read := func() { _, _, _ = e.mgr.agentSessionsShared(context.Background(), rt) }
	read()
	read()
	if reads.Load() != 1 {
		t.Fatalf("reads = %d before the write, want 1 (shared)", reads.Load())
	}
	// A GET through the wrapper must not drop the cache.
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/inbox/items/"+id, nil))
	read()
	if reads.Load() != 1 {
		t.Fatalf("reads = %d after a GET, want 1", reads.Load())
	}
	req := httptest.NewRequest("POST", "/api/inbox/items/"+id+"/launch",
		strings.NewReader(`{"dir":"/home/dev/repos/app","kind":"claude"}`))
	req.Header.Set(inboxTestEmailHeader, "b@example.com")
	req.Header.Set("X-AF-Tenant", e.t1.Slug)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("launch: %d %s", rec.Code, rec.Body.String())
	}
	read()
	if reads.Load() != 2 {
		t.Fatalf("reads = %d after the launch, want 2 (cache dropped)", reads.Load())
	}
}
