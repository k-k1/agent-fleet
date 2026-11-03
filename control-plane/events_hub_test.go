package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// syncRecorder is a ResponseWriter whose body can be read while stream is still writing it.
type syncRecorder struct {
	mu  sync.Mutex
	h   http.Header
	buf bytes.Buffer
}

func (s *syncRecorder) Header() http.Header {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.h == nil {
		s.h = http.Header{}
	}
	return s.h
}
func (s *syncRecorder) WriteHeader(int) {}
func (s *syncRecorder) Flush()          {}
func (s *syncRecorder) Write(b []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(b)
}

func (s *syncRecorder) body() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// waitFrames blocks until n frames of stream have been written (or 25s pass).
func (s *syncRecorder) waitFrames(stream string, n int) {
	for deadline := time.Now().Add(25 * time.Second); len(s.frames()[stream]) < n && time.Now().Before(deadline); {
		time.Sleep(time.Millisecond)
	}
}

// frames decodes the data frames written so far, per stream.
func (s *syncRecorder) frames() map[string][]json.RawMessage {
	s.mu.Lock()
	body := s.buf.String()
	s.mu.Unlock()
	out := map[string][]json.RawMessage{}
	for _, part := range strings.Split(body, "\n\n") {
		part = strings.TrimSpace(part)
		if !strings.HasPrefix(part, "data: ") {
			continue
		}
		var f struct {
			Stream string          `json:"stream"`
			Data   json.RawMessage `json:"data"`
		}
		if json.Unmarshal([]byte(strings.TrimPrefix(part, "data: ")), &f) == nil {
			out[f.Stream] = append(out[f.Stream], f.Data)
		}
	}
	return out
}

// hubAgent serves /sessions with a list that differs on every read (so every tick is a change
// frame) and counts those reads; other paths answer like eventsTestEnv's stub.
func hubAgent(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/sessions":
			k := n.Add(1)
			fmt.Fprintf(w, `{"sessions":[{"name":"s","kind":"claude","alive":true,"label":"read-%d"}]}`, k)
		case "/notifications":
			_, _ = w.Write([]byte(`{"notifications":[]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

type hubSub struct {
	rec    *syncRecorder
	cancel context.CancelFunc
	done   chan struct{}
}

func startSub(t *testing.T, a eventsAPI, res *resolved) *hubSub {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	s := &hubSub{rec: &syncRecorder{}, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(s.done)
		a.stream(s.rec, httptest.NewRequest("GET", "/api/events", nil).WithContext(ctx), res)
	}()
	t.Cleanup(func() { cancel(); <-s.done })
	return s
}

func (s *hubSub) waitSessionFrames(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for len(s.rec.frames()["sessions"]) < n {
		if time.Now().After(deadline) {
			t.Fatalf("only %d sessions frames, want %d", len(s.rec.frames()["sessions"]), n)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func hubEnv(t *testing.T) (eventsAPI, *resolved, *atomic.Int64) {
	t.Helper()
	a, res := eventsTestEnv(t, newEventsStub(`{"sessions":[]}`))
	srv, n := hubAgent(t)
	res.rt = stubRuntime{endpoint: srv.URL, token: "tok"}
	a.tick = 40 * time.Millisecond
	return a, res, n
}

// With N subscribers the Agent sees one read per hub tick, not N. The shared-read TTL is off
// (eventsTestEnv), so only the hub can be what collapses them: before it, polls ≈ N × frames.
func TestEventsHubOneAgentReadPerTick(t *testing.T) {
	a, res, polls := hubEnv(t)
	const N = 10
	subs := make([]*hubSub, N)
	start := time.Now()
	for i := range subs {
		subs[i] = startSub(t, a, res)
	}
	for _, s := range subs {
		s.waitSessionFrames(t, 5)
	}
	for _, s := range subs {
		s.cancel()
		<-s.done
	}
	// The hub builds at most once per tick; +3 for the join-time builds. Without it every
	// subscriber polls on its own clock and the count is about N times this.
	ticks := int64(time.Since(start)/a.tick) + 3
	if got := polls.Load(); got > ticks {
		t.Fatalf("Agent saw %d reads for %d subscribers in %d tick periods, want at most one per tick", got, N, ticks)
	}
}

// Two members of one workspace share the list but each gets its own inbox origin.
func TestEventsHubKeepsMemberPartsApart(t *testing.T) {
	a, res, _ := hubEnv(t)
	ctx := context.Background()
	st := a.mgr.store
	tenant, _ := st.EnsureDefaultTenant(ctx)
	id2, _ := st.UpsertIdentity(ctx, "second@example.com", "second", "")
	m2, err := st.EnsureMembership(ctx, id2.ID, tenant.ID, "member")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RecordInboxSessionOrigin(ctx, res.mv.MembershipID, "s", "item-A", "sender-A"); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordInboxSessionOrigin(ctx, m2.ID, "s", "item-B", "sender-B"); err != nil {
		t.Fatal(err)
	}
	res2 := *res
	res2.mv = store.MembershipView{MembershipID: m2.ID}

	sa, sb := startSub(t, a, res), startSub(t, a, &res2)
	sa.waitSessionFrames(t, 3)
	sb.waitSessionFrames(t, 3)
	for _, c := range []struct {
		s    *hubSub
		want string
	}{{sa, "item-A"}, {sb, "item-B"}} {
		for _, f := range c.s.rec.frames()["sessions"] {
			if !strings.Contains(string(f), c.want) {
				t.Fatalf("frame %s lacks %q", f, c.want)
			}
			if strings.Contains(string(f), "item-A") == (c.want == "item-B") {
				t.Fatalf("frame %s leaks the other member's origin", f)
			}
		}
	}
}

// The last subscriber to leave stops the ticker goroutine and drops the hub.
func TestEventsHubStopsWithLastSubscriber(t *testing.T) {
	a, res, polls := hubEnv(t)
	s1, s2 := startSub(t, a, res), startSub(t, a, res)
	s1.waitSessionFrames(t, 2)
	s2.waitSessionFrames(t, 2)
	hubDone := a.mgr.eventsHub.Done(res.ws.ID)
	if hubDone == nil {
		t.Fatal("no hub while subscribed")
	}
	s1.cancel()
	<-s1.done
	select {
	case <-hubDone:
		t.Fatal("hub stopped with a subscriber left")
	case <-time.After(100 * time.Millisecond):
	}
	s2.cancel()
	<-s2.done
	select {
	case <-hubDone:
	case <-time.After(5 * time.Second):
		t.Fatal("hub goroutine still running after the last subscriber left")
	}
	if left := a.mgr.eventsHub.Active(); left != 0 {
		t.Fatalf("%d hubs registered after everyone left", left)
	}
	// A detached shared read that was already under way may still land; wait it out.
	time.Sleep(100 * time.Millisecond)
	before := polls.Load()
	time.Sleep(150 * time.Millisecond)
	if polls.Load() != before {
		t.Fatal("Agent still polled after the last subscriber left")
	}
}

// One subscriber going away (mid-read included) neither errors nor stalls the others.
func TestEventsHubCancelledSubscriberDoesNotHurtOthers(t *testing.T) {
	a, res, _ := hubEnv(t)
	gone, stay := startSub(t, a, res), startSub(t, a, res)
	gone.waitSessionFrames(t, 1)
	stay.waitSessionFrames(t, 1)
	gone.cancel()
	<-gone.done
	// The first joiner started the hub; its leaving must not take the hub down with it.
	if a.mgr.eventsHub.Done(res.ws.ID) == nil {
		t.Fatal("hub gone while a subscriber is still connected")
	}
	n := len(stay.rec.frames()["sessions"])
	stay.waitSessionFrames(t, n+3)
	for _, f := range stay.rec.frames()["sessions"] {
		var p struct{ Sessions []sessionWire }
		if err := json.Unmarshal(f, &p); err != nil || len(p.Sessions) != 1 || !p.Sessions[0].Alive {
			t.Fatalf("remaining subscriber got a degraded frame %s (%v)", f, err)
		}
	}
}
