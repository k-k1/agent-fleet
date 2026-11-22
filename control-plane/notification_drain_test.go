package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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

	d := newNotificationDrainer(&manager{store: st}, time.Hour)

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
