package sessionx

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
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

func sharedBody(repo string, extra map[string]any) map[string]any {
	b := map[string]any{"dir": repo, "worktree": false, "allow_shared_working_copy": true}
	for k, v := range extra {
		b[k] = v
	}
	return spawnBody(b)
}

// A shared child with NO task still hears about the shared copy, under the spawn envelope —
// otherwise its user later hands it work and it edits the checkout as if it were alone. Both
// launch paths.
func TestSharedWorkingCopyEmptyTaskStillWarns(t *testing.T) {
	env, repo, _, prompts := sharedCopyEnv(t)
	sends := useFakeManagedDriver(t, session.KindCodex)

	code, raw := env.create(sharedBody(repo, map[string]any{"idempotency_key": "empty-tui"}))
	if code != http.StatusCreated {
		t.Fatalf("tui create = %d %s", code, raw)
	}
	p := <-prompts
	if !strings.HasPrefix(p, "[agent-fleet:spawn from=parent1] ") || !strings.Contains(p, "[agent-fleet:shared-working-copy]") {
		t.Fatalf("tui: no envelope or warning without a task: %q", p)
	}

	code, raw = env.create(sharedBody(repo, map[string]any{"kind": session.KindCodex, "driver": session.DriverManaged,
		"idempotency_key": "empty-managed"}))
	if code != http.StatusCreated {
		t.Fatalf("managed create = %d %s", code, raw)
	}
	d := <-sends
	if !strings.HasPrefix(d.prompt, "[agent-fleet:spawn from=parent1] ") ||
		!strings.Contains(d.prompt, "[agent-fleet:shared-working-copy]") {
		t.Fatalf("managed: no envelope or warning without a task: %q", d.prompt)
	}
	if want := (agents.Origin{Kind: agents.OriginSpawn, From: "parent1"}); d.origin != want {
		t.Fatalf("managed origin = %+v, want %+v", d.origin, want)
	}
	if d.source != TurnSourceSpawn {
		t.Fatalf("managed injection source = %q, want %q", d.source, TurnSourceSpawn)
	}
}

// Managed success with a task, and the replay of an identical shared create.
func TestSharedWorkingCopyManagedAndReplay(t *testing.T) {
	env, repo, _, _ := sharedCopyEnv(t)
	sends := useFakeManagedDriver(t, session.KindCodex)
	b := sharedBody(repo, map[string]any{"kind": session.KindCodex, "driver": session.DriverManaged,
		"initial_prompt": "review it", "idempotency_key": "replay-1"})
	code, raw := env.create(b)
	if code != http.StatusCreated {
		t.Fatalf("create = %d %s", code, raw)
	}
	var first session.Session
	_ = json.Unmarshal(raw, &first)
	d := <-sends
	if !strings.Contains(d.prompt, "review it") || !strings.Contains(d.prompt, "[agent-fleet:shared-working-copy]") {
		t.Fatalf("managed prompt: %q", d.prompt)
	}
	code, raw = env.create(b)
	var again session.Session
	_ = json.Unmarshal(raw, &again)
	if code != http.StatusOK || again.Name != first.Name {
		t.Fatalf("replay = %d %s, want 200 with %s", code, raw, first.Name)
	}
	if len(sends) != 0 {
		t.Fatal("the replay delivered a second first instruction")
	}
}

// The opt-in is for the copy the parent is running in NOW: not one a stopped or archived parent
// merely used to run in, and not one its stored (alias) Dir names today.
func TestSharedWorkingCopyNeedsLiveParentInCanonicalDir(t *testing.T) {
	env, repo, _, _ := sharedCopyEnv(t)
	busy := func(code int, raw []byte) bool {
		return code == http.StatusConflict && strings.Contains(string(raw), "spawn_working_copy_busy")
	}
	// A stranger is working in repo, the parent is stopped.
	env.fixture(session.Meta{Name: "intruder", Kind: session.KindClaude, Dir: repo, Origin: session.OriginUser})
	alive := sessionAliveFn
	sessionAliveFn = func(m session.Meta) bool { return m.Name == "intruder" || m.Name == "stranger" }
	t.Cleanup(func() { sessionAliveFn = alive })
	if code, raw := env.create(sharedBody(repo, map[string]any{"idempotency_key": "stopped"})); !busy(code, raw) {
		t.Fatalf("stopped parent = %d %s, want 409 busy", code, raw)
	}
	// Archived (still "alive" by the liveness seam).
	sessionAliveFn = func(m session.Meta) bool { return true }
	pm, _ := session.ReadMeta("parent1")
	pm.Archived = true
	session.WriteMeta(pm)
	if code, raw := env.create(sharedBody(repo, map[string]any{"idempotency_key": "archived"})); !busy(code, raw) {
		t.Fatalf("archived parent = %d %s, want 409 busy", code, raw)
	}
	pm.Archived = false
	session.WriteMeta(pm)

	// A parent launched through a symlink that now points elsewhere: its stored Dir does not
	// prove where it runs, so the opt-in is not honoured and the stranger's copy stays guarded.
	realA := filepath.Join(env.home, "repos", "a")
	realB := filepath.Join(env.home, "repos", "b")
	for _, d := range []string{realA, realB} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(env.home, "repos", "link")
	if err := os.Symlink(realB, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	pm.Dir = link // launched as link -> a, retargeted to b since
	session.WriteMeta(pm)
	env.fixture(session.Meta{Name: "inb", Kind: session.KindClaude, Dir: realB, Origin: session.OriginUser})
	if code, raw := env.create(sharedBody(realB, map[string]any{"idempotency_key": "retarget"})); !busy(code, raw) {
		t.Fatalf("retargeted alias parent = %d %s, want 409 busy", code, raw)
	}
}

// worktree=true must still reach EnsureWorktree with the flag set: a new directory, no shared
// warning, same as without it.
func TestSharedWorkingCopyFlagDoesNotTouchWorktreeLaunch(t *testing.T) {
	env, repo, _, prompts := sharedCopyEnv(t)
	gitInit(t, repo)
	for i, extra := range []map[string]any{{}, {"allow_shared_working_copy": true}} {
		b := spawnBody(map[string]any{"dir": repo, "worktree": true, "initial_prompt": "go",
			"idempotency_key": "wt-git-" + string(rune('a'+i))})
		for k, v := range extra {
			b[k] = v
		}
		code, raw := env.create(b)
		if code != http.StatusCreated {
			t.Fatalf("case %d: = %d %s", i, code, raw)
		}
		var created session.Session
		_ = json.Unmarshal(raw, &created)
		m, _ := session.ReadMeta(created.Name)
		if m.Dir == repo || m.Dir == "" {
			t.Fatalf("case %d: Dir = %q, want a new worktree", i, m.Dir)
		}
		if p := <-prompts; strings.Contains(p, "shared-working-copy") {
			t.Fatalf("case %d: worktree launch got the shared warning: %q", i, p)
		}
	}
}

// Without an explicit key the fingerprint is the dedupe: it must tell a shared create from a
// plain one.
func TestCreateIdempotencyKeyFingerprintSeesSharedFlag(t *testing.T) {
	a := &CreateReq{ReportTo: "conv", Dir: "/d", InitialPrompt: "x"}
	b := &CreateReq{ReportTo: "conv", Dir: "/d", InitialPrompt: "x", AllowSharedWorkingCopy: true}
	if createIdempotencyKey(a) == "" || createIdempotencyKey(a) == createIdempotencyKey(b) {
		t.Fatal("the fallback key ignores allow_shared_working_copy")
	}
}

// The guard is waived on ONE liveness answer and the launch applies what that answer said: a
// parent that stops between two reads must not get the waiver without the canonical dir and the
// warning.
func TestSharedWorkingCopyDecidedOnceEvenIfParentStopsMidCreate(t *testing.T) {
	env, repo, _, prompts := sharedCopyEnv(t)
	reads := 0
	sessionAliveFn = func(m session.Meta) bool {
		if m.Name != "parent1" {
			return m.Name == "stranger"
		}
		reads++
		return reads == 1 // alive for the first read only
	}
	code, raw := env.create(sharedBody(repo, map[string]any{"initial_prompt": "review", "idempotency_key": "once"}))
	if code != http.StatusCreated {
		t.Fatalf("create = %d %s", code, raw)
	}
	if p := <-prompts; !strings.Contains(p, "[agent-fleet:shared-working-copy]") {
		t.Fatalf("waived guard without the warning: %q", p)
	}
}

// The busy refusal tells the caller the precondition, so it does not repeat a call that cannot work.
func TestSharedWorkingCopyBusyMessageNamesPrecondition(t *testing.T) {
	ref := func() *SpawnRefusal {
		env, repo, _, _ := sharedCopyEnv(t)
		_ = env
		return spawnWorkingCopyRefusal(repo, "stranger", false)
	}()
	if ref == nil || !strings.Contains(ref.Message, "symlink") {
		t.Fatalf("busy message omits the canonical-path precondition: %v", ref)
	}
}
