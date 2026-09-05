// runtime_health_test.go — regression: "the Agent has not answered yet" must never be
// reported as a failed start.
//
// The symptom: some local docker users hit a red `agent did not become healthy within 15s`
// toast on every Workspace start, and the workspace was usable seconds later. The start had
// succeeded all along; only the CP's rule that /healthz must answer 200 within the budget
// turned it into a failure — and the budget widens to 300s only when the self-update opt-in
// is ON, which is why it looked like it struck just some people.
package runtime

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// unreadyAgent keeps answering /healthz with 503, i.e. an Agent still in boot-install.
func unreadyAgent(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func readyAgent(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestWaitAgentHealthyTimeoutIsTypedAndKeepsItsWording — a caller has to be able to tell
// "it has not arrived" apart from a failure. The wording stays byte-for-byte at the same
// time: scheduled-run history (error:wake: …) and operational greps match on that string.
func TestWaitAgentHealthyTimeoutIsTypedAndKeepsItsWording(t *testing.T) {
	srv := unreadyAgent(t)
	err := WaitAgentHealthy(context.Background(), srv.URL, 400*time.Millisecond)
	if err == nil {
		t.Fatal("unready agent: want an error")
	}
	var notReady agentNotReadyError
	if !errors.As(err, &notReady) {
		t.Fatalf("timeout must be typed as agentNotReadyError, got %T (%v)", err, err)
	}
	if want := "agent did not become healthy within 400ms"; err.Error() != want {
		t.Fatalf("message drifted: %q, want %q", err.Error(), want)
	}
	// A cancellation is a different thing (the caller simply left). Confusing the two reads
	// an aborted start as "not here yet" and carries on.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cerr := WaitAgentHealthy(ctx, srv.URL, time.Second)
	if errors.As(cerr, &notReady) {
		t.Fatalf("canceled wait must not look like a readiness overrun: %v", cerr)
	}
}

// TestAgentStartingMarkerIsSelfHealing — the mark is what justifies claiming "still
// booting", but left behind it yields a starting that never converges: a box the Console
// can neither stop nor recreate (docs/log/70 §70.14.6). Pins both ways out: the Agent came
// up, or the deadline passed.
func TestAgentStartingMarkerIsSelfHealing(t *testing.T) {
	t.Run("印が無ければ starting ではない", func(t *testing.T) {
		m := agentStartingMarkerIn(t.TempDir())
		if m.active(context.Background(), unreadyAgent(t).URL) {
			t.Fatal("no marker: want not starting")
		}
	})

	t.Run("dataDir を持たない Runtime では常に無効", func(t *testing.T) {
		m := agentStartingMarkerIn("")
		m.arm(time.Now().Add(time.Hour)) // nowhere to write it: nothing happens
		if m.active(context.Background(), unreadyAgent(t).URL) {
			t.Fatal("marker without a dataDir must never claim starting")
		}
	})

	t.Run("応答が無い間は starting", func(t *testing.T) {
		dir := t.TempDir()
		m := agentStartingMarkerIn(dir)
		m.arm(time.Now().Add(time.Hour))
		if !m.active(context.Background(), unreadyAgent(t).URL) {
			t.Fatal("armed marker + unready agent: want starting")
		}
		if _, err := os.Stat(filepath.Join(dir, ".agent-starting")); err != nil {
			t.Fatalf("marker must survive the probe while still booting: %v", err)
		}
	})

	t.Run("Agent が上がったら印を落として running へ戻る", func(t *testing.T) {
		dir := t.TempDir()
		m := agentStartingMarkerIn(dir)
		m.arm(time.Now().Add(time.Hour))
		if m.active(context.Background(), readyAgent(t).URL) {
			t.Fatal("healthy agent: want not starting")
		}
		if _, err := os.Stat(filepath.Join(dir, ".agent-starting")); !os.IsNotExist(err) {
			t.Fatalf("marker survived a healthy probe: %v", err)
		}
	})

	t.Run("期限切れは running へ落ちる（永遠の starting を作らない）", func(t *testing.T) {
		dir := t.TempDir()
		m := agentStartingMarkerIn(dir)
		m.arm(time.Now().Add(-time.Second))
		if m.active(context.Background(), unreadyAgent(t).URL) {
			t.Fatal("expired marker: want not starting")
		}
		if _, err := os.Stat(filepath.Join(dir, ".agent-starting")); !os.IsNotExist(err) {
			t.Fatalf("expired marker was not cleaned up: %v", err)
		}
	})

	t.Run("壊れた印も starting にしない", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".agent-starting"), []byte("nonsense"), 0o644); err != nil {
			t.Fatal(err)
		}
		if agentStartingMarkerIn(dir).active(context.Background(), unreadyAgent(t).URL) {
			t.Fatal("garbled marker: want not starting")
		}
	})
}

// hostPort splits an httptest URL into the host/port pair the adapters keep.
func hostPort(t *testing.T, raw string) (string, string) {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Hostname(), u.Port()
}

// TestDockerStateReportsStartingWhileTheAgentBoots — "the container is running" is not
// "usable", and the state has to say so. Left as running, terminals and file fetches
// connect to a still-booting Workspace and fail against a socket nobody listens on.
func TestDockerStateReportsStartingWhileTheAgentBoots(t *testing.T) {
	dir := t.TempDir()
	agent := unreadyAgent(t)
	host, port := hostPort(t, agent.URL)
	status := "running"
	d := &dockerRuntime{name: "af-ws-x", dataDir: dir, agentHost: host, agentPort: port}
	d.inspect = func(_ context.Context, typ, _, _ string) string {
		if typ != "container" {
			t.Errorf("State inspected a %q, not the container", typ)
		}
		return status
	}
	ctx := context.Background()

	if got := d.State(ctx); got != "running" {
		t.Fatalf("no marker: State = %q, want running", got)
	}
	d.startingMarker().arm(time.Now().Add(time.Hour))
	if got := d.State(ctx); got != "starting" {
		t.Fatalf("booting agent: State = %q, want starting", got)
	}
	// Marker or no marker, a dead container is not starting — it is not even up.
	status = "exited"
	if got := d.State(ctx); got != "stopped" {
		t.Fatalf("exited container: State = %q, want stopped", got)
	}
	status = ""
	if got := d.State(ctx); got != "none" {
		t.Fatalf("absent container: State = %q, want none", got)
	}
}

// TestNativeStateReportsStartingWhileTheAgentBoots — the same for native. While a live pid
// meant running, the first rootfs start (minutes of boot-install) read as "running"
// throughout.
func TestNativeStateReportsStartingWhileTheAgentBoots(t *testing.T) {
	dir := t.TempDir()
	agent := unreadyAgent(t)
	_, port := hostPort(t, agent.URL)
	n := &nativeRuntime{name: "af-ws-x", dataDir: dir, agentBin: os.Args[0], agentPort: port}
	// Use this test process as the live agent process (pidAlive compares the argv0 basename).
	if err := os.WriteFile(n.pidFile(), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if got := n.State(ctx); got != "running" {
		t.Fatalf("no marker: State = %q, want running", got)
	}
	n.startingMarker().arm(time.Now().Add(time.Hour))
	if got := n.State(ctx); got != "starting" {
		t.Fatalf("booting agent: State = %q, want starting", got)
	}
}

// TestWorkspaceAliveCoversStarting — deciding to delete a home (recreate / clean-home) asks
// "is it alive", not "is it running". A booting container can still write under the
// bind-mount, so a running-only check would go and delete a live home.
func TestWorkspaceAliveCoversStarting(t *testing.T) {
	for state, want := range map[string]bool{"running": true, "starting": true, "stopped": false, "none": false} {
		if got := WorkspaceAlive(state); got != want {
			t.Errorf("WorkspaceAlive(%q) = %v, want %v", state, got, want)
		}
	}
}
