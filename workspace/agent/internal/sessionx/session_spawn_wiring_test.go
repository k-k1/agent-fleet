package sessionx

import (
	"encoding/json"
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

// spawnEnv is one test's isolated create endpoint plus the list of sessions IT started.
type spawnEnv struct {
	srv     *httptest.Server
	home    string
	t       *testing.T
	started []string // tmux sessions this test launched, and the only ones it may kill
}

// spawnServer stands the create endpoint up with a stub agent CLI on PATH, so a launch really
// completes without depending on a real claude.
//
// ⚠️ The tmux server is SHARED with every other session in this workspace. The session store is
// per-test (AF_SESSIONS_DIR), but tmux names are not namespaced, so cleanup may only touch what
// this test actually launched — never "every meta in the store", which includes hand-written
// fixtures whose names (`busy`, `kid1`, …) could belong to somebody's real session. Kills are
// also pinned with `=`: without it tmux resolves a target by prefix and fnmatch, so
// `claude_kid1` would happily match a stranger's `claude_kid10`.
func spawnServer(t *testing.T) *spawnEnv {
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
	env := &spawnEnv{srv: httptest.NewServer(mux), home: home, t: t}
	t.Cleanup(func() {
		env.srv.Close()
		for _, name := range env.started {
			_ = exec.Command("tmux", "kill-session", "-t", "="+session.TmuxName(name)).Run()
		}
	})
	return env
}

// create POSTs one create and remembers the session it launched, so cleanup can kill exactly
// that and nothing else. Returns the status and the raw body; the caller asserts on them.
func (e *spawnEnv) create(body map[string]any) (int, []byte) {
	e.t.Helper()
	code, raw := roundtrip(e.t, e.srv, "POST", "/sessions", body)
	if code == http.StatusCreated || code == http.StatusOK {
		var s session.Session
		if json.Unmarshal(raw, &s) == nil && s.Name != "" {
			e.started = append(e.started, s.Name)
		}
	}
	return code, raw
}

// spawnBody is the common create request, with per-test overrides.
func spawnBody(extra map[string]any) map[string]any {
	body := map[string]any{"kind": "claude", "origin": "session", "origin_session": "parent1"}
	for k, v := range extra {
		body[k] = v
	}
	return body
}

// The whole chain for one spawned child: provenance on the meta, the envelope on the delivered
// task, and the injection record that gives the mirror its badge — written BEFORE delivery.
func TestCreateSessionSpawnWiring(t *testing.T) {
	env := spawnServer(t)
	repo := filepath.Join(env.home, "repos", "app")
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

	code, raw := env.create(spawnBody(map[string]any{"dir": repo, "initial_prompt": "rebase onto develop"}))
	if code != http.StatusCreated {
		t.Fatalf("create = %d %s", code, raw)
	}
	var created session.Session
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatal(err)
	}
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
	env := spawnServer(t)
	repo := filepath.Join(env.home, "repos", "app")
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

	third := spawnBody(map[string]any{
		"dir": repo, "initial_prompt": "the third task", "idempotency_key": "cs_third",
	})
	code, raw := env.create(third)
	if code != http.StatusCreated {
		t.Fatalf("third child = %d %s", code, raw)
	}
	var created session.Session
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatal(err)
	}

	// No slot is left held after the create. This catches a release that never happens; that it
	// happens at the META WRITE rather than at the handler's return is pinned by
	// TestSpawnSlotIsHandedOverToTheMeta, because the difference between the two is only
	// visible from inside the launch window and the handler waits on nothing a test can hold.
	if got := spawnInflight.n["parent1"]; got != 0 {
		t.Fatalf("inflight after a completed create = %d, want 0 (leaked slot)", got)
	}

	// Four is refused, and the refusal names the ceiling.
	code, raw = env.create(spawnBody(map[string]any{"dir": repo, "initial_prompt": "one too many"}))
	if code != http.StatusConflict || !strings.Contains(string(raw), "spawn_budget") {
		t.Fatalf("fourth child = %d %s, want 409 spawn_budget", code, raw)
	}

	// The retry of the create that MADE the third child must be replayed, not refused. A client
	// that timed out mid-launch re-sends the same request, and by then its own child is on disk
	// and counted: check the budget before the idempotency ledger and the caller is told it
	// already has three, for a session it is still waiting to hear about.
	code, raw = env.create(third)
	if code != http.StatusOK {
		t.Fatalf("retry = %d %s, want 200 (a replay of the first)", code, raw)
	}
	var replay session.Session
	if err := json.Unmarshal(raw, &replay); err != nil {
		t.Fatal(err)
	}
	if replay.Name != created.Name {
		t.Fatalf("retry produced %q, want the first session %q", replay.Name, created.Name)
	}
}

// Two agents in one working copy, checked against the dir the session will REALLY run in.
func TestCreateSessionSpawnWorkingCopyGuard(t *testing.T) {
	env := spawnServer(t)
	home := env.home
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

	spawn := func(extra map[string]any) (int, []byte) { return env.create(spawnBody(extra)) }

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
	env := spawnServer(t)
	repo := filepath.Join(env.home, "repos", "app")
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
			body := spawnBody(map[string]any{"dir": repo, "worktree": true})
			for k, v := range tc.body {
				body[k] = v
			}
			code, raw := env.create(body)
			if code < 400 || !strings.Contains(string(raw), tc.code) {
				t.Fatalf("= %d %s, want %s", code, raw, tc.code)
			}
		})
	}
}
