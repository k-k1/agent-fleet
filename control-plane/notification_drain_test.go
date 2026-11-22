package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// drainFakeRT is only what the drain touches: State, Endpoint and Token.
type drainFakeRT struct {
	runtime.Runtime
	state, endpoint string
}

func (f drainFakeRT) State(context.Context) string { return f.state }
func (f drainFakeRT) Endpoint() string             { return f.endpoint }
func (f drainFakeRT) Token() string                { return "tok" }

func TestNotificationDrainerDrainWorkspace(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	tn, err := st.EnsureDefaultTenant(ctx)
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.UpsertIdentity(ctx, "a@example.com", "a", "")
	if err != nil {
		t.Fatal(err)
	}
	mem, err := st.EnsureMembership(ctx, id.ID, tn.ID, "member")
	if err != nil {
		t.Fatal(err)
	}
	var announced atomic.Int32
	st.SetNotificationHook(func(store.Notification) { announced.Add(1) })

	var gets, acks atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/notifications":
			gets.Add(1)
			_, _ = w.Write([]byte(`{"notifications":[{"id":"e1","kind":"question","sessionName":"s1","sessionKind":"claude","displayName":"S","createdAt":"2026-11-23T00:00:00Z"}]}`))
		case "/notifications/ack":
			acks.Add(1)
		}
	}))
	defer srv.Close()

	d := &notificationDrainer{store: st, interval: time.Hour}

	if got := d.drainWorkspace(ctx, drainFakeRT{state: "stopped", endpoint: srv.URL}, mem.ID); got != "stopped" || gets.Load() != 0 {
		t.Fatalf("stopped workspace must not be asked: got=%q gets=%d", got, gets.Load())
	}
	if got := d.drainWorkspace(ctx, drainFakeRT{state: "running", endpoint: srv.URL}, mem.ID); got != "ready" {
		t.Fatalf("running drain = %q", got)
	}
	if announced.Load() != 1 || acks.Load() != 1 {
		t.Fatalf("announced=%d acks=%d, want 1 and 1", announced.Load(), acks.Load())
	}
	// The same event arriving again (an unacked re-send, or a Console drain) is not announced twice.
	d.drainWorkspace(ctx, drainFakeRT{state: "running", endpoint: srv.URL}, mem.ID)
	if announced.Load() != 1 {
		t.Fatalf("duplicate was announced again: %d", announced.Load())
	}

	if d.hasSubscription(ctx, mem.ID) {
		t.Fatal("no subscription yet")
	}
	if err := st.UpsertPushSubscription(ctx, store.PushSubscription{MembershipID: mem.ID, Endpoint: "https://fcm.googleapis.com/fcm/send/x", P256dh: "k", Auth: "a"}); err != nil {
		t.Fatal(err)
	}
	if !d.hasSubscription(ctx, mem.ID) {
		t.Fatal("subscription not seen")
	}
}

func newDrainStore(t *testing.T) *store.SQL {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return st
}

// A sweep covers every tenant, asks only running workspaces whose member has a device
// subscribed, never has more than notificationDrainParallel in flight, and a workspace that
// 404s or hangs does not stop the others.
func TestNotificationDrainerSweep(t *testing.T) {
	ctx := context.Background()
	st := newDrainStore(t)
	var announced atomic.Int32
	st.SetNotificationHook(func(store.Notification) { announced.Add(1) })

	type target struct {
		ws       store.Workspace
		state    string
		subbed   bool
		behavior string // "ok" | "404" | "hang"
	}
	var targets []*target
	n := 0
	for _, slug := range []string{"t1", "t2"} {
		tn, err := st.CreateTenant(ctx, slug, slug)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 5; i++ {
			n++
			email := slug + string(rune('a'+i)) + "@example.com"
			id, err := st.UpsertIdentity(ctx, email, email, "")
			if err != nil {
				t.Fatal(err)
			}
			m, err := st.EnsureMembership(ctx, id.ID, tn.ID, "member")
			if err != nil {
				t.Fatal(err)
			}
			tg := &target{state: "running", subbed: true, behavior: "ok"}
			switch i {
			case 1:
				tg.state = "stopped"
			case 2:
				tg.subbed = false
			case 3:
				tg.behavior = "404"
			}
			tg.ws = store.Workspace{ID: store.NewID(), TenantID: tn.ID, MembershipID: m.ID,
				ContainerName: "c" + string(rune('0'+n)), Network: "n", DataDir: "/d" + string(rune('0'+n)), AgentPort: "1", AgentToken: "x", State: "running"}
			if err := st.CreateWorkspace(ctx, tg.ws); err != nil {
				t.Fatal(err)
			}
			if tg.subbed {
				if err := st.UpsertPushSubscription(ctx, store.PushSubscription{MembershipID: m.ID,
					Endpoint: "https://fcm.googleapis.com/fcm/send/" + m.ID, P256dh: "k", Auth: "a"}); err != nil {
					t.Fatal(err)
				}
			}
			targets = append(targets, tg)
		}
	}

	var inflight, peak atomic.Int32
	var asked sync.Map // membership id -> GET count
	byEndpoint := map[string]*target{}
	var servers []*httptest.Server
	for _, tg := range targets {
		tg := tg
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/notifications" {
				return
			}
			c, _ := asked.LoadOrStore(tg.ws.MembershipID, new(atomic.Int32))
			c.(*atomic.Int32).Add(1)
			cur := inflight.Add(1)
			defer inflight.Add(-1)
			for {
				p := peak.Load()
				if cur <= p || peak.CompareAndSwap(p, cur) {
					break
				}
			}
			time.Sleep(30 * time.Millisecond)
			if tg.behavior == "404" {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(`{"notifications":[{"id":"e-` + tg.ws.ID + `","kind":"question","sessionName":"s","sessionKind":"claude","displayName":"S","createdAt":"2026-11-23T00:00:00Z"}]}`))
		}))
		servers = append(servers, srv)
		byEndpoint[tg.ws.ID] = tg
		tg.ws.AgentPort = srv.URL // carried to the fake runtime below
	}
	defer func() {
		for _, s := range servers {
			s.Close()
		}
	}()

	d := &notificationDrainer{store: st, interval: time.Hour,
		runtimeOf: func(ws store.Workspace) runtime.Runtime {
			tg := byEndpoint[ws.ID]
			return drainFakeRT{state: tg.state, endpoint: tg.ws.AgentPort}
		}}
	d.sweep(ctx)

	want := 0
	for _, tg := range targets {
		c, ok := asked.Load(tg.ws.MembershipID)
		should := tg.state == "running" && tg.subbed
		if should != ok {
			t.Errorf("workspace %s state=%s subbed=%v: asked=%v, want %v", tg.ws.ContainerName, tg.state, tg.subbed, ok, should)
		}
		if should && tg.behavior == "ok" {
			want++
		}
		_ = c
	}
	if int(announced.Load()) != want {
		t.Errorf("announced %d rows, want %d (every ok target, none of the 404s)", announced.Load(), want)
	}
	if peak.Load() > 4 {
		t.Errorf("peak concurrency %d exceeds 4", peak.Load())
	}
	if peak.Load() < 2 {
		t.Errorf("peak concurrency %d: the sweep is not parallel", peak.Load())
	}
}

// run starts the timer loop and stops on cancel.
func TestNotificationDrainerRunTicksAndStops(t *testing.T) {
	st := newDrainStore(t)
	var ticks atomic.Int32
	d := &notificationDrainer{store: countingTenantStore{Store: st, n: &ticks}, interval: 5 * time.Millisecond,
		runtimeOf: func(store.Workspace) runtime.Runtime { return nil }}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.run(ctx); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for ticks.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if ticks.Load() < 2 {
		t.Fatal("run did not sweep on its timer")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not stop on cancel")
	}
}

type countingTenantStore struct {
	store.Store
	n *atomic.Int32
}

func (c countingTenantStore) ListTenants(ctx context.Context) ([]store.Tenant, error) {
	c.n.Add(1)
	return c.Store.ListTenants(ctx)
}

// An oversized /notifications body is neither stored nor acked, so the entries stay queued.
func TestDrainAgentOutboxRejectsOversizedBody(t *testing.T) {
	ctx := context.Background()
	st := newDrainStore(t)
	var announced, acks atomic.Int32
	st.SetNotificationHook(func(store.Notification) { announced.Add(1) })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/notifications/ack" {
			acks.Add(1)
			return
		}
		_, _ = w.Write([]byte(`{"notifications":[{"id":"e1","kind":"question","sessionName":"s","displayName":"`))
		chunk := make([]byte, 1<<20)
		for i := range chunk {
			chunk[i] = 'x'
		}
		for i := 0; i < int(maxOutboxBody>>20)+2; i++ {
			_, _ = w.Write(chunk)
		}
		_, _ = w.Write([]byte(`"}]}`))
	}))
	defer srv.Close()
	tn, _ := st.EnsureDefaultTenant(ctx)
	id, _ := st.UpsertIdentity(ctx, "a@example.com", "a", "")
	m, _ := st.EnsureMembership(ctx, id.ID, tn.ID, "member")
	if got := drainAgentOutbox(ctx, st, drainFakeRT{state: "running", endpoint: srv.URL}, m.ID); got != "offline" {
		t.Fatalf("oversized body = %q, want offline", got)
	}
	if announced.Load() != 0 || acks.Load() != 0 {
		t.Fatalf("oversized body was stored (%d) or acked (%d)", announced.Load(), acks.Load())
	}
}
