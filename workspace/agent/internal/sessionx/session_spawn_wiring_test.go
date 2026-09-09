package sessionx

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// These go through the REAL create handler, which the unit tests around them do not.
//
// Every piece of the spawn regime is a function the handler has to remember to call: the
// refusals, the slot reservation, the envelope, the injection record. A unit test of any of them
// passes just as happily when nothing calls it — the failure mode stage 1 wrote
// TestGetSessionStatusTrimsOnlyForSessions for, one layer down.

// spawnServer stands the create endpoint up with a stub agent CLI on PATH, so a launch really
// completes without depending on a real claude.
func spawnServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\nsleep 120\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	mux := http.NewServeMux()
	mux.HandleFunc("POST /sessions", HandleCreateSession)
	srv := httptest.NewServer(mux)
	t.Cleanup(func() {
		srv.Close()
		// Whatever launched really is a tmux session on this host.
		for _, m := range session.ListMetas() {
			_ = exec.Command("tmux", "kill-session", "-t", session.TmuxName(m.Name)).Run()
		}
	})
	return srv, home
}

// The whole chain for one spawned child: provenance on the meta, the envelope on the delivered
// task, and the injection record that gives the mirror its badge — written BEFORE delivery.
func TestCreateSessionSpawnWiring(t *testing.T) {
	srv, home := spawnServer(t)
	repo := filepath.Join(home, "repos", "app")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	session.WriteMeta(session.Meta{Name: "parent1", Kind: session.KindClaude,
		Origin: session.OriginUser, CreatedAt: "2026-09-09T10:00:00+09:00"})

	// Be the delivery, so the ordering requirement is observable: what did the record say at
	// the moment the prompt went out?
	type delivered struct {
		prompt string
		source string
	}
	got := make(chan delivered, 4)
	orig := deliverInitialPromptFn
	deliverInitialPromptFn = func(name, prompt string) {
		got <- delivered{prompt: prompt, source: injectionSourceOf(name, prompt)}
	}
	t.Cleanup(func() { deliverInitialPromptFn = orig })

	var created session.Session
	do(t, srv, "POST", "/sessions", map[string]any{
		"dir": repo, "kind": "claude", "initial_prompt": "rebase onto develop",
		"origin": "session", "origin_session": "parent1",
	}, http.StatusCreated, &created)

	if created.Origin != session.OriginSession || created.OriginSession != "parent1" {
		t.Fatalf("wire origin = %q/%q, want session/parent1", created.Origin, created.OriginSession)
	}
	m, ok := session.ReadMeta(created.Name)
	if !ok || m.Origin != session.OriginSession || m.OriginSession != "parent1" {
		t.Fatalf("meta origin = %q/%q (ok=%v)", m.Origin, m.OriginSession, ok)
	}
	d := <-got
	// The envelope reaches the CHILD, which is the only place it can do its job: the agent
	// reads its own first prompt, not the mirror.
	if !strings.HasPrefix(d.prompt, "[agent-fleet:spawn from=parent1] ") {
		t.Fatalf("delivered task has no spawn envelope: %q", d.prompt)
	}
	if !strings.Contains(d.prompt, "rebase onto develop") {
		t.Fatalf("the task itself did not survive the envelope: %q", d.prompt)
	}
	// And the record was already there when the prompt went out. Recorded afterwards, a turn
	// that arrives quickly settles unbadged.
	if d.source != TurnSourceSpawn {
		t.Fatalf("injection source at delivery = %q, want %q (recorded after delivery?)", d.source, TurnSourceSpawn)
	}
}

// The budget is enforced by the handler, not merely computable by a helper — and it must not
// answer a RETRY, which is the whole point of running after the idempotency claim.
func TestCreateSessionSpawnBudgetAndRetry(t *testing.T) {
	srv, home := spawnServer(t)
	repo := filepath.Join(home, "repos", "app")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	session.WriteMeta(session.Meta{Name: "parent1", Kind: session.KindClaude,
		Origin: session.OriginUser, CreatedAt: "2026-09-09T10:00:00+09:00"})
	for _, n := range []string{"kid1", "kid2"} {
		session.WriteMeta(session.Meta{Name: n, Kind: session.KindClaude, Dir: repo,
			Origin: session.OriginSession, OriginSession: "parent1", CreatedAt: "2026-09-09T10:00:00+09:00"})
	}
	orig := deliverInitialPromptFn
	deliverInitialPromptFn = func(string, string) {}
	t.Cleanup(func() { deliverInitialPromptFn = orig })

	third := map[string]any{
		"dir": repo, "kind": "claude", "initial_prompt": "the third task",
		"origin": "session", "origin_session": "parent1", "idempotency_key": "cs_third",
	}
	var created session.Session
	do(t, srv, "POST", "/sessions", third, http.StatusCreated, &created)

	// Four is refused, and the refusal names the ceiling.
	code, raw := roundtrip(t, srv, "POST", "/sessions", map[string]any{
		"dir": repo, "kind": "claude", "initial_prompt": "one too many",
		"origin": "session", "origin_session": "parent1",
	})
	if code != http.StatusConflict || !strings.Contains(string(raw), "spawn_budget") {
		t.Fatalf("fourth child = %d %s, want 409 spawn_budget", code, raw)
	}

	// The retry of the create that MADE the third child must be replayed, not refused. A client
	// that timed out mid-launch re-sends the same request, and by then its own child is on disk
	// and counted: check the budget before the idempotency ledger and the caller is told it
	// already has three, for a session it is still waiting to hear about.
	var replay session.Session
	do(t, srv, "POST", "/sessions", third, http.StatusOK, &replay)
	if replay.Name != created.Name {
		t.Fatalf("retry produced %q, want the first session %q", replay.Name, created.Name)
	}
}

// Two agents in one working copy, checked against the dir the session will REALLY run in.
func TestCreateSessionSpawnWorkingCopyGuard(t *testing.T) {
	srv, home := spawnServer(t)
	repo := filepath.Join(home, "repos", "app")
	if err := os.MkdirAll(filepath.Join(repo, "console"), 0o755); err != nil {
		t.Fatal(err)
	}
	session.WriteMeta(session.Meta{Name: "parent1", Kind: session.KindClaude,
		Origin: session.OriginUser, CreatedAt: "2026-09-09T10:00:00+09:00"})
	// One session already working: in the repo, and one in home.
	session.WriteMeta(session.Meta{Name: "busy", Kind: session.KindShell, Dir: repo, Subdir: "console",
		Origin: session.OriginUser, CreatedAt: "2026-09-09T10:00:00+09:00"})
	session.WriteMeta(session.Meta{Name: "athome", Kind: session.KindShell, Dir: home,
		Origin: session.OriginUser, CreatedAt: "2026-09-09T10:00:00+09:00"})
	aliveOrig := sessionAliveFn
	sessionAliveFn = func(m session.Meta) bool { return m.Name == "busy" || m.Name == "athome" }
	t.Cleanup(func() { sessionAliveFn = aliveOrig })
	deliverOrig := deliverInitialPromptFn
	deliverInitialPromptFn = func(string, string) {}
	t.Cleanup(func() { deliverInitialPromptFn = deliverOrig })

	spawn := func(extra map[string]any) (int, []byte) {
		body := map[string]any{"kind": "claude", "origin": "session", "origin_session": "parent1"}
		for k, v := range extra {
			body[k] = v
		}
		return roundtrip(t, srv, "POST", "/sessions", body)
	}

	if code, raw := spawn(map[string]any{"dir": repo, "worktree": false}); code != http.StatusConflict ||
		!strings.Contains(string(raw), "spawn_working_copy_busy") {
		t.Fatalf("busy working copy = %d %s, want 409 spawn_working_copy_busy", code, raw)
	}
	// An empty dir becomes HOME inside the create. Checking the request as sent would compare
	// "" against home and wave this through, onto a session already running there.
	if code, raw := spawn(map[string]any{"worktree": false}); code != http.StatusConflict ||
		!strings.Contains(string(raw), "spawn_working_copy_busy") {
		t.Fatalf("empty dir (= home, busy) = %d %s, want 409 spawn_working_copy_busy", code, raw)
	}
	// Same for a relative dir, which the create joins onto home.
	if code, raw := spawn(map[string]any{"dir": "repos/app", "worktree": false}); code != http.StatusConflict ||
		!strings.Contains(string(raw), "spawn_working_copy_busy") {
		t.Fatalf("relative dir = %d %s, want 409 spawn_working_copy_busy", code, raw)
	}
	// A free working copy is fine.
	free := filepath.Join(home, "repos", "other")
	if err := os.MkdirAll(free, 0o755); err != nil {
		t.Fatal(err)
	}
	if code, raw := spawn(map[string]any{"dir": free, "worktree": false}); code != http.StatusCreated {
		t.Fatalf("free working copy = %d %s, want 201", code, raw)
	}
}

// The other refusals, over HTTP: a child cannot spawn, and no session can start a shell.
func TestCreateSessionSpawnDepthAndKindOverHTTP(t *testing.T) {
	srv, home := spawnServer(t)
	repo := filepath.Join(home, "repos", "app")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	session.WriteMeta(session.Meta{Name: "parent1", Kind: session.KindClaude,
		Origin: session.OriginUser, CreatedAt: "2026-09-09T10:00:00+09:00"})
	session.WriteMeta(session.Meta{Name: "kid", Kind: session.KindClaude, Dir: repo,
		Origin: session.OriginSession, OriginSession: "parent1", CreatedAt: "2026-09-09T10:00:00+09:00"})

	for _, tc := range []struct {
		name string
		body map[string]any
		code string
	}{
		{"a child may not spawn", map[string]any{"origin_session": "kid"}, "spawn_depth"},
		{"an unknown parent is not treated as a root", map[string]any{"origin_session": "ghost"}, "spawn_unknown_parent"},
		{"no shells", map[string]any{"origin_session": "parent1", "kind": "shell"}, "spawn_kind_refused"},
		{"no ssm either", map[string]any{"origin_session": "parent1", "kind": "ssm"}, "spawn_kind_refused"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := map[string]any{"dir": repo, "kind": "claude", "origin": "session", "worktree": true}
			for k, v := range tc.body {
				body[k] = v
			}
			code, raw := roundtrip(t, srv, "POST", "/sessions", body)
			if code < 400 || !strings.Contains(string(raw), tc.code) {
				t.Fatalf("= %d %s, want %s", code, raw, tc.code)
			}
		})
	}
}
