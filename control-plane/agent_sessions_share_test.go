package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// countingAgent plays GET /sessions and counts the reads that reach it.
func countingAgent(t *testing.T, body func(n int64) (int, string)) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		code, b := body(n.Add(1))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = w.Write([]byte(b))
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

const sharedOneSession = `{"sessions":[{"name":"s","kind":"claude","alive":true}]}`

func TestSharedSessionsCollapsesSubscribers(t *testing.T) {
	srv, n := countingAgent(t, func(int64) (int, string) { time.Sleep(50 * time.Millisecond); return 200, sharedOneSession })
	m := &manager{}
	rt := stubRuntime{endpoint: srv.URL, token: "tok"}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			env, _, err := m.agentSessionsShared(context.Background(), rt)
			if err != nil || len(env.Sessions) != 1 {
				t.Errorf("env=%+v err=%v", env, err)
			}
		}()
	}
	wg.Wait()
	// A later subscriber inside the TTL reuses the result too.
	if _, _, err := m.agentSessionsShared(context.Background(), rt); err != nil {
		t.Fatal(err)
	}
	if got := n.Load(); got != 1 {
		t.Fatalf("Agent saw %d reads for 9 callers, want 1", got)
	}
}

func TestSharedSessionsTTLExpiryAndKey(t *testing.T) {
	srv, n := countingAgent(t, func(int64) (int, string) { return 200, sharedOneSession })
	other, n2 := countingAgent(t, func(int64) (int, string) { return 200, `{"sessions":[]}` })
	m := &manager{}
	now := time.Now()
	m.sessionsShare.now = func() time.Time { return now }
	rt := stubRuntime{endpoint: srv.URL, token: "tok"}
	read := func(rt stubRuntime) int {
		env, _, err := m.agentSessionsShared(context.Background(), rt)
		if err != nil {
			t.Fatal(err)
		}
		return len(env.Sessions)
	}
	read(rt)
	read(rt)
	if n.Load() != 1 {
		t.Fatalf("reads = %d inside the TTL, want 1", n.Load())
	}
	// The same workspace re-pointed at another Agent must not see the old Agent's list.
	if got := read(stubRuntime{endpoint: other.URL, token: "tok"}); got != 0 || n2.Load() != 1 {
		t.Fatalf("re-pointed: sessions=%d agent2 reads=%d, want 0 and 1", got, n2.Load())
	}
	now = now.Add(sharedSessionsTTL + time.Millisecond)
	read(rt)
	if n.Load() != 2 {
		t.Fatalf("reads = %d after the TTL, want 2", n.Load())
	}
	// Expired entries are dropped on the next insert, so the map does not grow with gone workspaces.
	now = now.Add(time.Hour)
	read(rt)
	m.sessionsShare.mu.Lock()
	size := len(m.sessionsShare.entries)
	m.sessionsShare.mu.Unlock()
	if size != 1 {
		t.Fatalf("entries = %d, want 1 (the other Agent's expired entry must be gone)", size)
	}
}

func TestSharedSessionsFailureIsNotCached(t *testing.T) {
	srv, n := countingAgent(t, func(n int64) (int, string) {
		if n == 1 {
			return 500, `boom`
		}
		return 200, sharedOneSession
	})
	m := &manager{}
	rt := stubRuntime{endpoint: srv.URL}
	if _, _, err := m.agentSessionsShared(context.Background(), rt); err == nil {
		t.Fatal("first read: want the Agent's error")
	}
	env, _, err := m.agentSessionsShared(context.Background(), rt)
	if err != nil || len(env.Sessions) != 1 || n.Load() != 2 {
		t.Fatalf("second read: env=%+v err=%v reads=%d, want a fresh successful read", env, err, n.Load())
	}
}

func TestSharedSessionsCancelledCallerDoesNotFailOthers(t *testing.T) {
	release := make(chan struct{})
	srv, n := countingAgent(t, func(int64) (int, string) { <-release; return 200, sharedOneSession })
	m := &manager{}
	rt := stubRuntime{endpoint: srv.URL}

	ctxA, cancelA := context.WithCancel(context.Background())
	errA := make(chan error, 1)
	go func() { _, _, err := m.agentSessionsShared(ctxA, rt); errA <- err }()
	for n.Load() == 0 { // A's read is in flight
		time.Sleep(time.Millisecond)
	}
	type result struct {
		env agentSessionsEnvelope
		err error
	}
	resB := make(chan result, 1)
	go func() { env, _, err := m.agentSessionsShared(context.Background(), rt); resB <- result{env, err} }()
	time.Sleep(20 * time.Millisecond)
	cancelA()
	if err := <-errA; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled caller: err = %v, want context.Canceled", err)
	}
	close(release)
	r := <-resB
	if r.err != nil || len(r.env.Sessions) != 1 {
		t.Fatalf("surviving caller: env=%+v err=%v, want the shared list", r.env, r.err)
	}
	if n.Load() != 1 {
		t.Fatalf("reads = %d, want 1", n.Load())
	}
}

func TestSharedSessionsInvalidateDropsCacheAndInflight(t *testing.T) {
	var gate atomic.Bool
	release := make(chan struct{})
	srv, n := countingAgent(t, func(int64) (int, string) {
		if gate.Load() {
			<-release
		}
		return 200, sharedOneSession
	})
	m := &manager{}
	rt := stubRuntime{endpoint: srv.URL}
	_, _, _ = m.agentSessionsShared(context.Background(), rt)
	m.sessionsShare.invalidate()
	_, _, _ = m.agentSessionsShared(context.Background(), rt)
	if n.Load() != 2 {
		t.Fatalf("reads = %d after a write, want 2 (cache dropped)", n.Load())
	}
	// A read that began before the write must not be stored once it lands.
	m.sessionsShare.invalidate()
	gate.Store(true)
	done := make(chan struct{})
	go func() { _, _, _ = m.agentSessionsShared(context.Background(), rt); close(done) }()
	for n.Load() < 3 {
		time.Sleep(time.Millisecond)
	}
	m.sessionsShare.invalidate()
	close(release)
	<-done
	gate.Store(false)
	_, _, _ = m.agentSessionsShared(context.Background(), rt)
	if n.Load() != 4 {
		t.Fatalf("reads = %d, want 4: the pre-write read must not have been cached", n.Load())
	}
}

// Two members of one workspace share the Agent read but each gets only their own inbox origin,
// and N subscribers on one tick cost one Agent read.
func TestSessionsPayloadPerMemberContentWithSharedRead(t *testing.T) {
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
	mk := func(email string) store.Membership {
		ident, _ := st.UpsertIdentity(ctx, email, email, "")
		mb, err := st.EnsureMembership(ctx, ident.ID, tenant.ID, "member")
		if err != nil {
			t.Fatal(err)
		}
		return mb
	}
	a, b := mk("a@example.com"), mk("b@example.com")
	ws := store.Workspace{ID: "ws-share", TenantID: tenant.ID, MembershipID: a.ID,
		ContainerName: "c", Network: "test", DataDir: "/d", AgentPort: "1", AgentToken: "tok",
		State: "running", CreatedAt: store.NowTS()}
	if err := st.CreateWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordInboxSessionOrigin(ctx, a.ID, "s", "item-a", "sender-a"); err != nil {
		t.Fatal(err)
	}
	srv, n := countingAgent(t, func(int64) (int, string) { time.Sleep(30 * time.Millisecond); return 200, sharedOneSession })
	api := workspaceAPI{memberAuth: memberAuth{mgr: &manager{store: st}}}
	rt := stubRuntime{endpoint: srv.URL, token: "tok"}

	origin := func(mv store.MembershipView) string {
		res := &resolved{rt: rt, ws: ws, mv: mv}
		raw, _ := json.Marshal(api.sessionsPayload(ctx, res))
		var body struct {
			Sessions []struct {
				InboxOrigin json.RawMessage `json:"inboxOrigin"`
			} `json:"sessions"`
		}
		if err := json.Unmarshal(raw, &body); err != nil || len(body.Sessions) != 1 {
			t.Errorf("payload %s err=%v", raw, err)
			return "?"
		}
		return string(body.Sessions[0].InboxOrigin)
	}
	got := make([]string, 6)
	var wg sync.WaitGroup
	for i := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mv := store.MembershipView{MembershipID: a.ID}
			if i%2 == 1 {
				mv = store.MembershipView{MembershipID: b.ID}
			}
			got[i] = origin(mv)
		}()
	}
	wg.Wait()
	for i, g := range got {
		if i%2 == 0 && g == "" {
			t.Errorf("member a #%d lost their inbox origin", i)
		}
		if i%2 == 1 && g != "" && g != "null" {
			t.Errorf("member b #%d got another member's origin: %s", i, g)
		}
	}
	if n.Load() != 1 {
		t.Fatalf("Agent saw %d reads for 6 subscribers, want 1", n.Load())
	}
}
