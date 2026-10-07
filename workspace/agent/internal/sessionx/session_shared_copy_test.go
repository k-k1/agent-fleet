package sessionx

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// sharedCopyEnv stands up a live parent in repo and a live unrelated session in other, and
// captures every delivered first prompt.
func sharedCopyEnv(t *testing.T) (env *spawnEnv, repo, other string, prompts chan string) {
	t.Helper()
	env = spawnServer(t)
	repo = filepath.Join(env.home, "repos", "app")
	other = filepath.Join(env.home, "repos", "other")
	for _, d := range []string{filepath.Join(repo, "console"), other} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	env.fixture(session.Meta{Name: "parent1", Kind: session.KindClaude, Dir: repo,
		Origin: session.OriginUser})
	env.fixture(session.Meta{Name: "stranger", Kind: session.KindClaude, Dir: other,
		Origin: session.OriginUser})
	aliveOrig := sessionAliveFn
	sessionAliveFn = func(m session.Meta) bool { return m.Name == "parent1" || m.Name == "stranger" }
	t.Cleanup(func() { sessionAliveFn = aliveOrig })
	prompts = make(chan string, 8)
	deliverOrig := deliverInitialPromptFn
	deliverInitialPromptFn = func(_, p string) { prompts <- p }
	t.Cleanup(func() { deliverInitialPromptFn = deliverOrig })
	return env, repo, other, prompts
}

func TestSharedWorkingCopyOptIn(t *testing.T) {
	env, repo, other, prompts := sharedCopyEnv(t)
	link := filepath.Join(env.home, "app-link")
	if err := os.Symlink(repo, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	body := func(extra map[string]any) map[string]any {
		b := map[string]any{"dir": repo, "worktree": false, "initial_prompt": "review the diff"}
		for k, v := range extra {
			b[k] = v
		}
		return spawnBody(b)
	}
	busy := func(code int, raw []byte) bool {
		return code == http.StatusConflict && strings.Contains(string(raw), "spawn_working_copy_busy")
	}

	// Without the flag, and with it explicitly false, the default refusal stands, and it names the
	// option.
	for _, extra := range []map[string]any{nil, {"allow_shared_working_copy": false}} {
		code, raw := env.create(body(extra))
		if !busy(code, raw) || !strings.Contains(string(raw), "allow_shared_working_copy") {
			t.Fatalf("flag %v: = %d %s, want 409 spawn_working_copy_busy naming the option", extra, code, raw)
		}
	}
	// A foreign directory stays refused even with the flag.
	if code, raw := env.create(body(map[string]any{"dir": other, "allow_shared_working_copy": true})); !busy(code, raw) {
		t.Fatalf("foreign dir with flag = %d %s, want 409 spawn_working_copy_busy", code, raw)
	}
	// The flag changes nothing for a worktree launch: same answer as without it.
	c1, _ := env.create(body(map[string]any{"worktree": true, "idempotency_key": "wt-a"}))
	c2, _ := env.create(body(map[string]any{"worktree": true, "allow_shared_working_copy": true, "idempotency_key": "wt-b"}))
	if c1 != c2 || c2 == http.StatusCreated {
		t.Fatalf("worktree=true: without flag %d, with flag %d; want equal and not 201 (not a git repo)", c1, c2)
	}
	// Depth stays in force: a child cannot spawn, flag or not.
	env.fixture(session.Meta{Name: "kid", Kind: session.KindClaude, Dir: repo,
		Origin: session.OriginSession, OriginSession: "stranger"})
	if code, raw := env.create(body(map[string]any{"origin_session": "kid", "allow_shared_working_copy": true})); code < 400 ||
		!strings.Contains(string(raw), "spawn_depth") {
		t.Fatalf("child spawning with flag = %d %s, want spawn_depth", code, raw)
	}
	// Shells stay refused.
	if code, raw := env.create(body(map[string]any{"kind": "shell", "allow_shared_working_copy": true})); code < 400 ||
		!strings.Contains(string(raw), "spawn_kind_refused") {
		t.Fatalf("shell with flag = %d %s, want spawn_kind_refused", code, raw)
	}
	for len(prompts) > 0 {
		<-prompts
	}

	// The opt-in on the parent's own copy starts a second live session in it, through a
	// symlink and with a subdir too, and Meta.Dir records the canonical path.
	realRepo, _ := filepath.EvalSymlinks(repo)
	for i, extra := range []map[string]any{
		{"allow_shared_working_copy": true, "idempotency_key": "ok-1"},
		{"allow_shared_working_copy": true, "dir": link, "idempotency_key": "ok-2"},
		{"allow_shared_working_copy": true, "subdir": "console", "idempotency_key": "ok-3"},
	} {
		code, raw := env.create(body(extra))
		if code != http.StatusCreated {
			t.Fatalf("case %d: = %d %s, want 201", i, code, raw)
		}
		var created session.Session
		if err := json.Unmarshal(raw, &created); err != nil {
			t.Fatal(err)
		}
		m, _ := session.ReadMeta(created.Name)
		if m.Dir != realRepo {
			t.Fatalf("case %d: Meta.Dir = %q, want the canonical %q", i, m.Dir, realRepo)
		}
		p := <-prompts
		if !strings.HasPrefix(p, "[agent-fleet:spawn from=parent1] review the diff") ||
			!strings.Contains(p, "[agent-fleet:shared-working-copy]") ||
			!strings.Contains(p, "explicitly by path") {
			t.Fatalf("case %d: first prompt lacks the envelope or the shared-copy warning: %q", i, p)
		}
	}
}

// Meta.Dir keeps the spelling the launch used; the guards that stop a delete or a branch switch
// under a live session must see it through an alias.
func TestSessionsInDirSeesAliasPaths(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "app")
	if err := os.MkdirAll(filepath.Join(repo, "console"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(repo, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	metas := []session.Meta{
		{Name: "a", Dir: link, Title: "via-link"},
		{Name: "b", Dir: filepath.Join(repo, "console", ".."), Title: "via-dotdot"},
		{Name: "c", Dir: filepath.Join(link, "console"), Title: "sub-via-link"},
		{Name: "d", Dir: root, Title: "parent", Archived: false},
	}
	all := func(m session.Meta) bool { return m.Name != "d" }
	got := sessionsInDir(metas, all, repo)
	if len(got) != 3 {
		t.Fatalf("sessionsInDir(real path) = %v, want the three alias launches", got)
	}
	if got := sessionsInDir(metas, all, link); len(got) != 3 {
		t.Fatalf("sessionsInDir(alias path) = %v, want the three alias launches", got)
	}
	locked := []session.Meta{{Name: "a", Dir: link, Title: "x", Locked: true}}
	if got := LockedSessionsInDir(locked, repo); len(got) != 1 {
		t.Fatalf("LockedSessionsInDir(real path) = %v, want the alias-launched one", got)
	}
}
