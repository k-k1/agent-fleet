package egressagent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestRunEndToEnd(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("log files are identified by an xattr; the agent runs on Linux only")
	}
	dir := t.TempDir()
	policy := &fakeCP{allowlist: []string{".github.com"}}
	posted := make(chan struct{}, 1)
	ingest := &ingestCP{onPost: func() {
		select {
		case posted <- struct{}{}:
		default:
		}
	}}
	mux := http.NewServeMux()
	mux.Handle("/internal/egress/policy", policy)
	mux.Handle("/internal/egress", ingest)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cfg := Config{
		PolicyURL: srv.URL + "/internal/egress/policy", IngestURL: srv.URL + "/internal/egress", Token: "tok",
		AllowFile: filepath.Join(dir, "allow.txt"), ModeFile: filepath.Join(dir, "mode.conf"),
		LogPath: filepath.Join(dir, "af-egress.log"), StatePath: filepath.Join(dir, "state", "state.json"),
		PolicyEvery: 20 * time.Millisecond, ShipEvery: 20 * time.Millisecond,
	}
	appendFile(t, cfg.LogPath, line(1, "10.0.1.5", "api.github.com")+line(2, "10.0.1.5", "evil.example"))

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- Run(ctx, cfg) }()
	// Wait on the ingest handler's own signal instead of polling a wall clock: the signal fires
	// per request, ids() takes the handler's lock so it observes the request's events once the
	// handler returns. The timeout only bounds a real hang; it is not the pass condition.
	timeout := time.After(60 * time.Second)
	for {
		if _, n := ingest.ids(); n >= 2 {
			break
		}
		select {
		case <-posted:
		case <-timeout:
			cancel()
			t.Fatal("events were not shipped")
		}
	}
	cancel()
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	ingest.mu.Lock()
	defer ingest.mu.Unlock()
	if !ingest.got[0].Allowed || ingest.got[1].Allowed {
		t.Fatalf("verdicts: %+v", ingest.got)
	}
	if b, _ := os.ReadFile(cfg.AllowFile); string(b) != generatedHeader+".github.com\n" {
		t.Fatalf("allow file = %q", b)
	}
}

func TestRunRefusesUnusableConfig(t *testing.T) {
	if err := Run(context.Background(), Config{}); err == nil {
		t.Fatal("missing URLs and token must be refused")
	}
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := Config{PolicyURL: "http://x", IngestURL: "http://x", Token: "t", StatePath: filepath.Join(blocker, "state.json")}
	if err := Run(context.Background(), cfg); err == nil {
		t.Fatal("a state path that cannot be written must be refused at start")
	}
}
