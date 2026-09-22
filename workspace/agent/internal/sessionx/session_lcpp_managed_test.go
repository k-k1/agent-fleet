package sessionx

// End-to-end coverage, through the real HTTP handlers and the REAL lcpp driver (not a fake),
// for the two routes a prior lane flagged as unreachable while lcpp had no managed driver:
// fork and recreate (session_handlers.go's HandleForkSession / HandleRecreateSession). Both
// go through driverOf/mcpx.StartManagedSession exactly like every other managed kind, so this
// mainly pins that internal/agents/lcpp's driver.Resume (and its own fork materialization,
// ensureForked) actually gets called on these paths now that it exists.

import (
	"net/http"
	"net/http/httptest"
	"os/exec"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/lcpp"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/harness"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func lcppTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /sessions", HandleCreateSession)
	mux.HandleFunc("POST /sessions/{name}/fork", HandleForkSession)
	mux.HandleFunc("POST /sessions/{name}/recreate", HandleRecreateSession)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestLcppForkThroughRealDriverCopiesStore is the end-to-end positive control for the fork
// route reaching the real lcpp driver: create a session, seed its store directly (no engine
// needed — decision 3's store predates any turn), fork it over HTTP, and confirm the new
// session's own store holds a copy.
func TestLcppForkThroughRealDriverCopiesStore(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	isolateAgentState(t)
	srv := lcppTestServer(t)

	var created session.Session
	do(t, srv, "POST", "/sessions", map[string]any{"dir": t.TempDir(), "kind": "lcpp", "model": "test-model"}, http.StatusCreated, &created)

	srcMeta, ok := session.ReadMeta(created.Name)
	if !ok {
		t.Fatal("meta not persisted")
	}
	sid := session.UUID(srcMeta.Dir, srcMeta.Name)
	st := lcpp.Open(sid)
	if _, err := st.AppendUser("hello"); err != nil {
		t.Fatalf("AppendUser: %v", err)
	}
	if _, err := st.AppendMessage(harness.Message{Role: harness.RoleAssistant, Content: "hi there"}); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	var forked session.Session
	do(t, srv, "POST", "/sessions/"+created.Name+"/fork", map[string]any{}, http.StatusCreated, &forked)

	dstMeta, ok := session.ReadMeta(forked.Name)
	if !ok {
		t.Fatal("forked meta not persisted")
	}
	if dstMeta.DriverKind() != session.DriverManaged {
		t.Fatalf("forked driver = %q, want managed", dstMeta.Driver)
	}
	dstSid := session.UUID(dstMeta.Dir, dstMeta.Name)
	recs, _, err := lcpp.Open(dstSid).Records()
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	if len(recs) != 2 || recs[0].Content != "hello" || recs[1].Content != "hi there" {
		t.Fatalf("forked records = %+v, want a copy of the source conversation", recs)
	}
}

// TestLcppRecreateThroughRealDriverStartsEmpty is the end-to-end positive control for
// recreate reaching the real lcpp driver: the new slot must be a fresh, empty conversation
// (recreate carries no ForkFrom — session_handlers.go's own comment on HandleRecreateSession),
// never a copy of the old one.
func TestLcppRecreateThroughRealDriverStartsEmpty(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	isolateAgentState(t)
	srv := lcppTestServer(t)

	var created session.Session
	do(t, srv, "POST", "/sessions", map[string]any{"dir": t.TempDir(), "kind": "lcpp", "model": "test-model"}, http.StatusCreated, &created)
	origMeta, _ := session.ReadMeta(created.Name)
	origSid := session.UUID(origMeta.Dir, origMeta.Name)
	if _, err := lcpp.Open(origSid).AppendUser("do not carry me over"); err != nil {
		t.Fatalf("AppendUser: %v", err)
	}

	var recreated session.Session
	do(t, srv, "POST", "/sessions/"+created.Name+"/recreate", nil, http.StatusOK, &recreated)
	if recreated.Name == created.Name {
		t.Fatal("recreate kept the same session name")
	}
	newMeta, ok := session.ReadMeta(recreated.Name)
	if !ok {
		t.Fatal("recreated meta not persisted")
	}
	if newMeta.DriverKind() != session.DriverManaged {
		t.Fatalf("recreated driver = %q, want managed", newMeta.Driver)
	}
	newSid := session.UUID(newMeta.Dir, newMeta.Name)
	recs, _, err := lcpp.Open(newSid).Records()
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	if len(recs) != 0 {
		t.Fatalf("recreated records = %+v, want an empty conversation", recs)
	}
}
