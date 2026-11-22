package opencode

import (
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/routepolicy"
)

// A policy accepted while the shared serve is starting finds no daemon up and restarts nothing, so
// the start itself must not publish a daemon built for the old policy: the daemon comes healthy
// after the change, and Ensure refuses it (ErrServePolicyMoved) instead of returning it.
func TestServeStartedOnAMovedPolicyIsNotPublished(t *testing.T) {
	home := isolatedHome(t)
	t.Setenv("AF_SECRET_KEY", "")
	useMajor(t, 2)
	stubUsagePref(t, UsageZen)
	t.Setenv(standaloneEnv, "")
	storeKeys(t, map[string]string{"ANTHROPIC_API_KEY": "a", "OPENROUTER_API_KEY": "o"})

	store := routepolicy.Open(filepath.Join(t.TempDir(), "rp.json"), "")
	if _, err := store.Accept(anthropicOnly); err != nil {
		t.Fatal(err)
	}
	routepolicy.WaitListeners()
	old := routepolicy.Active
	routepolicy.Active = func() *routepolicy.Store { return store }
	t.Cleanup(func() {
		routepolicy.WaitListeners()
		reconcileInflight.Wait()
		routepolicy.Active = old
	})

	bin := t.TempDir()
	started := filepath.Join(home, "spawned")
	script := "#!/bin/sh\n: > " + started + "\nexec sleep 30\n"
	if err := os.WriteFile(filepath.Join(bin, "opencode"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	port := freePortForTest(t)
	t.Setenv(serveAddrEnv, "http://127.0.0.1:"+port)

	// Once the daemon is spawned: the policy moves, then the daemon answers its health probe.
	// The worker is joined before the shared policy state is put back (cleanups run last-in
	// first-out, so this one runs before the restore above) and reports its failures here.
	done := make(chan struct{})
	var workerErr error
	t.Cleanup(func() {
		<-done
		if workerErr != nil {
			t.Error(workerErr)
		}
	})
	go func() {
		defer close(done)
		spawned := false
		for i := 0; i < 400 && !spawned; i++ {
			_, err := os.Stat(started)
			spawned = err == nil
			if !spawned {
				time.Sleep(10 * time.Millisecond)
			}
		}
		if !spawned {
			workerErr = errors.New("the fake opencode never started")
			return
		}
		if _, err := store.Accept(openRouterOnly); err != nil {
			workerErr = err
			return
		}
		l, err := net.Listen("tcp", "127.0.0.1:"+port)
		if err != nil {
			workerErr = err
			return
		}
		srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"version":"2.0.28"}`))
		})}
		go func() { _ = srv.Serve(l) }()
		time.Sleep(3 * time.Second)
		_ = srv.Close()
	}()

	s := &Supervisor{}
	t.Cleanup(s.Shutdown)
	addr, _, err := s.Ensure()
	if !errors.Is(err, ErrServePolicyMoved) {
		t.Errorf("Ensure = %q, %v; want ErrServePolicyMoved", addr, err)
	}
	if s.up {
		t.Error("the stale daemon was published")
	}
}
