// events_test.go — contract tests for the unified /api/events push channel (traffic
// reduction P3). They pin that the first tick emits a snapshot of every stream, that an
// unchanged tick emits nothing (diff suppression), that only a changed stream is re-sent,
// and that a quiet stream still shows it is alive via ping. The shape of a payload is not
// tested here: the composing functions shared with the existing REST endpoints
// (workspacePayload / sessionsPayload / listPayload) own that.
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// eventsStub holds the knobs that drive the stub Agent from outside, plus how many polls
// have reached it. The count exists so a test's window closes on progress rather than on
// elapsed time (see runStream).
type eventsStub struct {
	body  atomic.Value // string — the /sessions response body
	delay atomic.Value // time.Duration — slows /sessions from the second poll on
	polls atomic.Int64 // how many times /sessions was called
}

func newEventsStub(body string) *eventsStub {
	s := &eventsStub{}
	s.body.Store(body)
	return s
}

func (s *eventsStub) setBody(b string)         { s.body.Store(b) }
func (s *eventsStub) setDelay(d time.Duration) { s.delay.Store(d) }

// waitPolls blocks until the stub has served n polls, or the deadline passes.
func (s *eventsStub) waitPolls(n int64, deadline time.Time) {
	for s.polls.Load() < n && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
}

// eventsTestEnv builds a sqlite-backed eventsAPI plus a stub Agent serving
// /sessions and /notifications, and the resolved record pointing at it.
func eventsTestEnv(t *testing.T, stub *eventsStub) (eventsAPI, *resolved) {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	tenant, _ := st.EnsureDefaultTenant(context.Background())
	id, _ := st.UpsertIdentity(context.Background(), "events@example.com", "events", "")
	m, _ := st.EnsureMembership(context.Background(), id.ID, tenant.ID, "member")
	ws := store.Workspace{ID: store.NewID(), TenantID: tenant.ID, MembershipID: m.ID,
		ContainerName: "af-ws-events", Network: "af-net-events", DataDir: t.TempDir(),
		AgentPort: "7700", AgentToken: "tok", State: "running", CreatedAt: store.NowTS()}
	if err := st.CreateWorkspace(context.Background(), ws); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sessions":
			// The delay applies from the second poll on; the first tick (the
			// snapshot) always passes through unimpeded.
			n := stub.polls.Add(1)
			if d, ok := stub.delay.Load().(time.Duration); ok && d > 0 && n > 1 {
				// Return at once when the other end (the CP's poll) goes away. A
				// plain Sleep would make httptest.Server.Close() wait it out and
				// slow the test down.
				select {
				case <-time.After(d):
				case <-r.Context().Done():
				}
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(stub.body.Load().(string)))
		case "/notifications":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"notifications":[]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	// Cleanup order is everything here. t.TempDir()'s RemoveAll is pushed onto
	// t.Cleanup at the moment TempDir is called, and cleanups run LIFO. So any
	// shutdown that must happen before RemoveAll has to be registered after the
	// TempDir call — register it earlier and it runs after the delete, too late.
	// The same trap and fix appear around tmux kill-server in
	// workspace/agent/tui_mirror_contract_test.go.
	t.Cleanup(func() { waitWorkItemFetchIdle(t, m.ID) })

	mgr := &manager{store: st}
	a := eventsAPI{memberAuth{mgr}, newWorkspaceAPI(mgr, false), notificationAPI{memberAuth{mgr}, st},
		workItemsAPI{memberAuth{mgr}, st},
		5 * time.Millisecond, time.Hour /* ping effectively off; only the ping test overrides it */}
	res := &resolved{rt: stubRuntime{endpoint: srv.URL, token: "tok"}, ws: ws,
		mv: store.MembershipView{MembershipID: m.ID}}
	return a, res
}

// waitWorkItemFetchIdle blocks until the detached work-item fetch for this
// membership has finished.
//
// When state=="running", tickAll fires refreshAsync through workItemsPayload. That
// goroutine queries the DB on a context.Background() deliberately detached from the
// request ctx (refreshAsync in workitems.go), so it is still touching sqlite after
// a.stream returns and the test function has returned. sqlite is in WAL mode, so that
// one SELECT recreates -wal/-shm in the middle of RemoveAll and the run fails with
// "TempDir RemoveAll cleanup: … directory not empty" even though the test itself
// passed. Measured: 5 failures in 30 runs locally with -count=30.
//
// refreshAsync deletes the key at the very end of the goroutine, after refreshNow has
// returned and its ctx is cancelled, so a missing key really does mean the DB is no
// longer being touched.
func waitWorkItemFetchIdle(t *testing.T, membershipID string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, busy := workItemFetchInFlight.Load(membershipID); !busy {
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("work-item fetch still in flight after 10s (membership %s)", membershipID)
			return
		}
		time.Sleep(time.Millisecond)
	}
}

// runStream drives a.stream until the stub Agent has served `polls` polls, then
// cancels the request (= the subscriber went away), and returns the decoded
// frames plus the raw body (for ping assertions).
//
// Closing the window on progress rather than on elapsed time is the whole point of this
// helper. A fixed deadline bets on how many ticks the runner can turn in that time, and
// on hosted CI the window shut before the first tick finished — 0 frames, with the
// snapshot under test never emitted. The absolute deadline stays as insurance against a
// hang; it must never be what decides the assertion.
func runStream(t *testing.T, a eventsAPI, res *resolved, stub *eventsStub, polls int64) (map[string][]json.RawMessage, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	go func() {
		stub.waitPolls(polls, time.Now().Add(25*time.Second))
		cancel()
	}()
	r := httptest.NewRequest("GET", "/api/events", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	a.stream(w, r, res)
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q", ct)
	}
	frames := map[string][]json.RawMessage{}
	for _, part := range strings.Split(w.Body.String(), "\n\n") {
		part = strings.TrimSpace(part)
		if !strings.HasPrefix(part, "data: ") {
			continue
		}
		var f struct {
			Stream string          `json:"stream"`
			Data   json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(part, "data: ")), &f); err != nil {
			t.Fatalf("bad frame %q: %v", part, err)
		}
		frames[f.Stream] = append(frames[f.Stream], f.Data)
	}
	return frames, w.Body.String()
}

// TestEventsStreamSnapshotThenSilence: the first tick emits one snapshot for each of the
// four streams, and later unchanged ticks re-send nothing. That diff suppression is the
// entire traffic saving P3 is after.
func TestEventsStreamSnapshotThenSilence(t *testing.T) {
	stub := newEventsStub(`{"sessions":[{"name":"s1","kind":"claude","alive":true}]}`)
	a, res := eventsTestEnv(t, stub)
	// One snapshot plus four unchanged ticks.
	frames, _ := runStream(t, a, res, stub, 5)

	for _, stream := range []string{"workspace", "stats", "sessions", "notifications"} {
		if len(frames[stream]) != 1 {
			t.Errorf("%s frames = %d, want 1 (snapshot once, then suppressed)", stream, len(frames[stream]))
		}
	}
	var wsp map[string]any
	_ = json.Unmarshal(frames["workspace"][0], &wsp)
	if wsp["state"] != "running" || wsp["name"] != "stub" {
		t.Errorf("workspace payload = %v", wsp)
	}
	var sess struct {
		Sessions []map[string]any `json:"sessions"`
	}
	_ = json.Unmarshal(frames["sessions"][0], &sess)
	if len(sess.Sessions) != 1 || sess.Sessions[0]["name"] != "s1" {
		t.Errorf("sessions payload = %v", sess)
	}
	var notif map[string]any
	_ = json.Unmarshal(frames["notifications"][0], &notif)
	if notif["sourceState"] != "ready" {
		t.Errorf("notifications payload = %v", notif)
	}
}

// TestEventsStreamPushesChange: when the Agent's sessions change, that stream and only
// that stream emits one more frame.
func TestEventsStreamPushesChange(t *testing.T) {
	stub := newEventsStub(`{"sessions":[{"name":"s1","kind":"claude","alive":true}]}`)
	a, res := eventsTestEnv(t, stub)

	// Apply the change only after the snapshot has gone out — counted in polls, not
	// milliseconds.
	go func() {
		stub.waitPolls(2, time.Now().Add(25*time.Second))
		stub.setBody(`{"sessions":[{"name":"s1","kind":"claude","alive":false}]}`)
	}()
	frames, _ := runStream(t, a, res, stub, 6)

	if got := len(frames["sessions"]); got != 2 {
		t.Fatalf("sessions frames = %d, want 2 (snapshot + change)", got)
	}
	if got := len(frames["workspace"]); got != 1 {
		t.Errorf("workspace frames = %d, want 1 (unchanged stream stays silent)", got)
	}
	var sess struct {
		Sessions []map[string]any `json:"sessions"`
	}
	_ = json.Unmarshal(frames["sessions"][1], &sess)
	if sess.Sessions[0]["alive"] != false {
		t.Errorf("second sessions frame = %v, want alive=false", sess)
	}
}

// TestEventsStreamNoFrameWhenCancelledMidPoll: the tick that was running when the
// subscriber went away must not add a single frame.
//
// This is exactly what made the two tests above fail on hosted CI. The sessions payload
// falls back to the DB mirror — a different shape — when the HTTP call to the Agent
// fails, so a request-ctx cancellation landing mid-poll looks like a change and emits a
// spurious frame. Locally a poll finishes in under 1ms and never lands in that window,
// so only slow runners hit it; here the Agent is slowed down on purpose to reproduce it.
func TestEventsStreamNoFrameWhenCancelledMidPoll(t *testing.T) {
	stub := newEventsStub(`{"sessions":[{"name":"s1","kind":"claude","alive":true}]}`)
	// The second /sessions sleeps for 5s. runStream cuts the stream off as soon as
	// that poll starts, so the cancellation always lands mid-poll.
	stub.setDelay(5 * time.Second)
	a, res := eventsTestEnv(t, stub)
	frames, _ := runStream(t, a, res, stub, 2)

	if got := len(frames["sessions"]); got != 1 {
		t.Fatalf("sessions frames = %d, want 1 (a cancelled poll is not a change): %s",
			got, frames["sessions"])
	}
}

// TestRoundStats: raw cgroup values jitter by the byte on every read, so mem_used is
// floored to 8MiB and cpu_pct rounded to an integer for diff suppression to work at all.
// Nothing is lost: the WS-bar chip displays a rounded percent and 0.1GiB anyway. Every
// other key must come through untouched.
func TestRoundStats(t *testing.T) {
	got := roundStats(map[string]any{
		"running": true, "mem_used": uint64(8<<20 + 12345), "mem_max": uint64(1 << 30),
		"cpu_pct": 12.7, "oom_kill_total": uint64(2),
	})
	if got["mem_used"] != uint64(8<<20) {
		t.Errorf("mem_used = %v, want %v", got["mem_used"], uint64(8<<20))
	}
	if got["cpu_pct"] != 13.0 {
		t.Errorf("cpu_pct = %v, want 13", got["cpu_pct"])
	}
	if got["mem_max"] != uint64(1<<30) || got["oom_kill_total"] != uint64(2) || got["running"] != true {
		t.Errorf("untouched keys changed: %v", got)
	}
	// Two samples differing only by jitter must serialize to identical JSON, which is
	// what makes diff suppression fire.
	a, _ := json.Marshal(roundStats(map[string]any{"mem_used": uint64(100<<20 + 1), "cpu_pct": 3.2}))
	b, _ := json.Marshal(roundStats(map[string]any{"mem_used": uint64(100<<20 + 999999), "cpu_pct": 2.8}))
	if string(a) != string(b) {
		t.Errorf("jittered samples differ after rounding: %s vs %s", a, b)
	}
}

// TestEventsStreamPing: once nothing has been sent for longer than the ping interval, a
// comment ping goes out — for the client watchdog and to keep intermediate proxies from
// dropping the connection.
func TestEventsStreamPing(t *testing.T) {
	stub := newEventsStub(`{"sessions":[]}`)
	a, res := eventsTestEnv(t, stub)
	a.ping = time.Millisecond
	_, raw := runStream(t, a, res, stub, 3)
	if !strings.Contains(raw, ": ping") {
		t.Errorf("no ping in quiet stream; body=%q", raw)
	}
}
