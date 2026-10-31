package opencode

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// A restart drains first: a Terminal turn running on the daemon is waited for and, at the drain
// timeout, aborted before the daemon goes. An adopted daemon (no process handle) takes the same
// drain even though it cannot be replaced.
func TestRestartDrainsBusyTerminals(t *testing.T) {
	isolatedHome(t)
	t.Setenv("AF_OPENCODE_DRAIN_TIMEOUT_SEC", "1")
	var mu sync.Mutex
	aborted := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/info":
			_, _ = w.Write([]byte(`{"version":"2.0.28"}`))
		case r.URL.Path == "/api/session/active":
			_, _ = w.Write([]byte(`{"data":{"ses_busy":{"type":"running"}}}`))
		case strings.HasSuffix(r.URL.Path, "/interrupt"):
			mu.Lock()
			aborted = r.URL.Path
			mu.Unlock()
			_, _ = w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv(serveAddrEnv, srv.URL)
	detectedMajor.Store(int32(apiV2))
	t.Cleanup(func() { detectedMajor.Store(int32(apiV1)) })

	m := session.Meta{Name: "oc-busy", Dir: t.TempDir(), Kind: session.KindOpencode, Driver: session.DriverTUI}
	sid := session.UUID(m.Dir, m.Name)
	markSlotMode(sid, modeShared)
	sids.Write(sid, "ses_busy")
	t.Cleanup(func() { slotModes.Remove(sid); sids.Remove(sid) })
	stubTerminalSlots(t, func() []session.Meta { return []session.Meta{m} })

	s := &Supervisor{up: true} // adopted: up, no process handle
	if s.Restart("test") {
		t.Fatal("an adopted daemon reported as replaced")
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(aborted, "ses_busy") {
		t.Fatalf("the busy Terminal turn was neither waited for nor aborted by the restart (abort %q)", aborted)
	}
}

// A daemon started for opencode 2 Terminal sessions carries the marker that tells the plugins
// they run on af's shared server (agent-fleet-shared.js); without it they act as standalone.
func TestEnsureMarksTheSharedServeInItsEnvironment(t *testing.T) {
	home := isolatedHome(t)
	useMajor(t, 2)
	stubUsagePref(t, UsageFree)
	t.Setenv(standaloneEnv, "")
	bin := t.TempDir()
	envLog := filepath.Join(home, "child-env")
	if err := os.WriteFile(filepath.Join(bin, "opencode"), []byte("#!/bin/sh\nenv > "+envLog+"\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(serveAddrEnv, "http://127.0.0.1:"+freePortForTest(t))
	oldTimeout := serveStartTimeout
	serveStartTimeout = 1500 * time.Millisecond
	t.Cleanup(func() { serveStartTimeout = oldTimeout })

	s := &Supervisor{}
	t.Cleanup(s.Shutdown)
	_, _, _ = s.Ensure() // the fake never answers; only the environment it was started with matters
	b, err := os.ReadFile(envLog)
	if err != nil {
		t.Fatalf("the daemon was never started: %v", err)
	}
	if !strings.Contains(string(b), sharedServeEnv+"=1") {
		t.Fatalf("the shared daemon was started without %s=1:\n%s", sharedServeEnv, b)
	}
}
