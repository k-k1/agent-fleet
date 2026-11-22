package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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
	var ackBody atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/notifications":
			gets.Add(1)
			_, _ = w.Write([]byte(`{"notifications":[{"id":"e1","kind":"question","sessionName":"s1","sessionKind":"claude","displayName":"S","createdAt":"2026-11-23T00:00:00Z"}]}`))
		case "/notifications/ack":
			acks.Add(1)
			b, _ := io.ReadAll(r.Body)
			ackBody.Store(string(b))
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
	if got, _ := ackBody.Load().(string); got != `{"ids":["e1"]}` {
		t.Fatalf("ack body = %q", got)
	}
	rows, err := st.ListNotifications(ctx, mem.ID, "2000-01-01T00:00:00Z", 10)
	if err != nil || len(rows) != 1 || rows[0].MembershipID != mem.ID || rows[0].EventID != "agent:e1" {
		t.Fatalf("stored rows for the member = %+v, %v", rows, err)
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

// seedSubscribed makes n running, subscribed workspaces in one tenant.
func seedSubscribed(t *testing.T, st *store.SQL, n int) []store.Workspace {
	t.Helper()
	ctx := context.Background()
	tn, err := st.EnsureDefaultTenant(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var out []store.Workspace
	for i := 0; i < n; i++ {
		email := "u" + string(rune('a'+i)) + "@example.com"
		id, err := st.UpsertIdentity(ctx, email, email, "")
		if err != nil {
			t.Fatal(err)
		}
		m, err := st.EnsureMembership(ctx, id.ID, tn.ID, "member")
		if err != nil {
			t.Fatal(err)
		}
		ws := store.Workspace{ID: store.NewID(), TenantID: tn.ID, MembershipID: m.ID,
			ContainerName: "c" + string(rune('a'+i)), Network: "n", DataDir: "/d" + string(rune('a'+i)), AgentPort: "1", AgentToken: "x", State: "running"}
		if err := st.CreateWorkspace(ctx, ws); err != nil {
			t.Fatal(err)
		}
		if err := st.UpsertPushSubscription(ctx, store.PushSubscription{MembershipID: m.ID,
			Endpoint: "https://fcm.googleapis.com/fcm/send/" + m.ID, P256dh: "k", Auth: "a"}); err != nil {
			t.Fatal(err)
		}
		out = append(out, ws)
	}
	return out
}

// With every slot held by an Agent that does not answer, exactly notificationDrainParallel
// workspaces are asked and the rest wait; releasing the gate lets all of them through.
func TestNotificationDrainerHoldsAtMostParallelSlots(t *testing.T) {
	st := newDrainStore(t)
	wss := seedSubscribed(t, st, notificationDrainParallel+3)
	gate := make(chan struct{})
	var inflight, total atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/notifications" {
			return
		}
		inflight.Add(1)
		total.Add(1)
		<-gate
		inflight.Add(-1)
		_, _ = w.Write([]byte(`{"notifications":[]}`))
	}))
	defer srv.Close()
	d := &notificationDrainer{store: st, interval: time.Hour, drainTimeout: 20 * time.Second,
		runtimeOf: func(store.Workspace) runtime.Runtime { return drainFakeRT{state: "running", endpoint: srv.URL} }}
	done := make(chan struct{})
	go func() { d.sweep(context.Background()); close(done) }()
	waitFor(t, "all slots held", func() bool { return inflight.Load() == notificationDrainParallel })
	time.Sleep(150 * time.Millisecond) // a fifth request would have arrived by now
	if got := total.Load(); got != notificationDrainParallel {
		t.Fatalf("%d workspaces asked while all slots were held, want %d", got, notificationDrainParallel)
	}
	close(gate)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("sweep did not finish")
	}
	if got := total.Load(); int(got) != len(wss) {
		t.Fatalf("%d workspaces asked in the end, want %d", got, len(wss))
	}
}

// An Agent that never answers is cut off at the drain timeout and the other workspaces are
// still drained.
func TestNotificationDrainerHungAgentDoesNotBlockOthers(t *testing.T) {
	st := newDrainStore(t)
	wss := seedSubscribed(t, st, 3)
	var announced atomic.Int32
	st.SetNotificationHook(func(store.Notification) { announced.Add(1) })
	hung := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer hung.Close()
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/notifications" {
			_, _ = w.Write([]byte(`{"notifications":[{"id":"` + r.Header.Get("X-Test") + `","kind":"question","sessionName":"s","displayName":"S"}]}`))
		}
	}))
	defer ok.Close()
	d := &notificationDrainer{store: st, interval: time.Hour, drainTimeout: 200 * time.Millisecond,
		runtimeOf: func(ws store.Workspace) runtime.Runtime {
			if ws.ID == wss[0].ID {
				return drainFakeRT{state: "running", endpoint: hung.URL}
			}
			return drainFakeRT{state: "running", endpoint: ok.URL}
		}}
	start := time.Now()
	d.sweep(context.Background())
	if time.Since(start) > 5*time.Second {
		t.Fatalf("sweep took %s: the hung Agent was not cut off", time.Since(start))
	}
	if announced.Load() != 2 {
		t.Fatalf("announced %d, want 2 (the two healthy workspaces)", announced.Load())
	}
}

// The timer loop never starts a sweep before the previous one has returned.
func TestNotificationDrainerRunDoesNotOverlapSweeps(t *testing.T) {
	st := newDrainStore(t)
	release := make(chan struct{})
	var calls atomic.Int32
	d := &notificationDrainer{store: blockingTenantStore{Store: st, n: &calls, release: release}, interval: 5 * time.Millisecond,
		runtimeOf: func(store.Workspace) runtime.Runtime { return nil }}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.run(ctx); close(done) }()
	waitFor(t, "first sweep started", func() bool { return calls.Load() == 1 })
	time.Sleep(100 * time.Millisecond) // twenty ticks while the first sweep is stuck
	if got := calls.Load(); got != 1 {
		t.Fatalf("%d sweeps started while the first had not returned", got)
	}
	close(release)
	waitFor(t, "second sweep started", func() bool { return calls.Load() >= 2 })
	cancel()
	<-done
}

type blockingTenantStore struct {
	store.Store
	n       *atomic.Int32
	release chan struct{}
}

func (b blockingTenantStore) ListTenants(ctx context.Context) ([]store.Tenant, error) {
	if b.n.Add(1) == 1 {
		<-b.release
	}
	return b.Store.ListTenants(ctx)
}

// A stalled read gives up at the query timeout instead of freezing the sweep.
func TestNotificationDrainerStalledQueryTimesOut(t *testing.T) {
	st := newDrainStore(t)
	d := &notificationDrainer{store: stalledTenantStore{Store: st}, queryTimeout: 100 * time.Millisecond}
	start := time.Now()
	d.sweep(context.Background())
	if time.Since(start) > 5*time.Second {
		t.Fatalf("sweep took %s", time.Since(start))
	}
}

type stalledTenantStore struct{ store.Store }

func (stalledTenantStore) ListTenants(ctx context.Context) ([]store.Tenant, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

type pruneStore struct {
	store.Store
	cutoffs []string
	fail    bool
}

func (p *pruneStore) SweepNotifications(_ context.Context, cutoff string) error {
	p.cutoffs = append(p.cutoffs, cutoff)
	if p.fail {
		return context.DeadlineExceeded
	}
	return nil
}

// Pruning uses the notification retention cutoff, runs at most hourly after a success, and is
// retried soon after a failure.
func TestNotificationDrainerPrune(t *testing.T) {
	ps := &pruneStore{Store: newDrainStore(t), fail: true}
	d := &notificationDrainer{store: ps}
	d.sweep(context.Background())
	d.sweep(context.Background())
	if len(ps.cutoffs) != 1 {
		t.Fatalf("a failed prune was retried at once: %d calls", len(ps.cutoffs))
	}
	if d.nextPrune.After(time.Now().Add(2 * notificationPruneRetry)) {
		t.Fatalf("after a failure the next prune is %s away, want about %s", time.Until(d.nextPrune), notificationPruneRetry)
	}
	ps.fail = false
	d.nextPrune = time.Time{}
	d.sweep(context.Background())
	d.sweep(context.Background())
	if len(ps.cutoffs) != 2 {
		t.Fatalf("a successful prune ran again within the hour: %d calls", len(ps.cutoffs))
	}
	if c := ps.cutoffs[1]; c > notificationCutoff() || c < time.Now().Add(-30*24*time.Hour).UTC().Format(time.RFC3339) {
		t.Fatalf("prune cutoff %q is not the retention cutoff", c)
	}
}

func TestStartNotificationDrainNeedsASender(t *testing.T) {
	if startNotificationDrain(context.Background(), &manager{}, time.Hour) {
		t.Fatal("the drain started without a push sender")
	}
}

// Entries are folded up one at a time under a byte cap: those read whole are stored and acked
// and the rest stays queued for the next drain.
func TestDrainAgentOutboxTakesWhatFitsUnderTheCap(t *testing.T) {
	ctx := context.Background()
	st := newDrainStore(t)
	var acked atomic.Value
	big := strings.Repeat("x", 1<<20)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/notifications/ack" {
			b, _ := io.ReadAll(r.Body)
			acked.Store(string(b))
			return
		}
		_, _ = w.Write([]byte(`{"notifications":[`))
		for i := 0; i < 12; i++ { // 12 MiB in all, over maxOutboxBody
			if i > 0 {
				_, _ = w.Write([]byte(","))
			}
			_, _ = w.Write([]byte(`{"id":"e` + string(rune('a'+i)) + `","kind":"question","sessionName":"s","displayName":"` + big + `"}`))
		}
		_, _ = w.Write([]byte(`]}`))
	}))
	defer srv.Close()
	tn, _ := st.EnsureDefaultTenant(ctx)
	id, _ := st.UpsertIdentity(ctx, "a@example.com", "a", "")
	m, _ := st.EnsureMembership(ctx, id.ID, tn.ID, "member")
	if got := drainAgentOutbox(ctx, st, drainFakeRT{state: "running", endpoint: srv.URL}, m.ID); got != "ready" {
		t.Fatalf("drain = %q", got)
	}
	rows, _ := st.ListNotifications(ctx, m.ID, "2000-01-01T00:00:00Z", 50)
	if len(rows) == 0 || len(rows) >= 12 {
		t.Fatalf("stored %d entries, want some but not all 12", len(rows))
	}
	ids, _ := acked.Load().(string)
	if strings.Count(ids, `"e`) != len(rows) {
		t.Fatalf("acked %q for %d stored rows", ids, len(rows))
	}
}
