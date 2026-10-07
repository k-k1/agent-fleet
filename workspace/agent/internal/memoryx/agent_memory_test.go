package memoryx

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// agentMemTestEnv builds the snapshot test's HOME plus a git main clone under ~/repos with one
// linked worktree, and registers a claude session in the clone, a codex session in the worktree
// and a shell session in the home directory.
func agentMemTestEnv(t *testing.T) (home, clone, wt string) {
	t.Helper()
	home, _, _ = memoryTestEnv(t)
	oldHook := AgentMemoryEnabled
	AgentMemoryEnabled = func() bool { return true }
	t.Cleanup(func() { AgentMemoryEnabled = oldHook })
	clone = filepath.Join(home, "repos", "demo")
	wt = filepath.Join(home, "repos", "demo@feature-x")
	memoryMkdirAll(t, clone)
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	git(clone, "init", "--quiet", "-b", "main")
	git(clone, "commit", "--quiet", "--allow-empty", "-m", "init")
	git(clone, "worktree", "add", "--quiet", "-b", "feature-x", wt)

	session.WriteMeta(session.Meta{Name: "claude-main", Dir: clone, Kind: "claude"})
	session.WriteMeta(session.Meta{Name: "codex-wt", Dir: wt, Kind: "codex"})
	session.WriteMeta(session.Meta{Name: "shell-home", Dir: home, Kind: "shell"})
	return home, clone, wt
}

func agentMemCallerT(t *testing.T, name string) agentMemCaller {
	t.Helper()
	c, err := agentMemResolveCaller(name)
	if err != nil {
		t.Fatalf("resolve %s: %v", name, err)
	}
	return c
}

func agentMemCode(err error) string {
	var ue *memoryUserErr
	if errors.As(err, &ue) {
		return ue.Code
	}
	return ""
}

// A worktree shares its main clone's project memory, and what one kind saves another reads at
// once, with the writer's kind and session recorded in the file and in its own commit.
func TestAgentMemorySharedAcrossKindsAndWorktrees(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	now := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	claudeC, codexC := agentMemCallerT(t, "claude-main"), agentMemCallerT(t, "codex-wt")
	if claudeC.Project == nil || codexC.Project == nil || claudeC.Project.ID != codexC.Project.ID {
		t.Fatalf("clone and worktree must be one project: %+v vs %+v", claudeC.Project, codexC.Project)
	}
	if claudeC.Project.Display != "demo" {
		t.Errorf("display = %q, want demo", claudeC.Project.Display)
	}

	res, err := agentMemSave(claudeC, agentMemSaveReq{Name: "build-cmd", Description: "how to build", Type: "project", Body: "run make"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Created || res.Revision != 1 || res.Scope != agentMemScopeProject || res.Commit == "" {
		t.Fatalf("save result = %+v", res)
	}

	got, err := agentMemRead(codexC, "", "build-cmd")
	if err != nil {
		t.Fatal(err)
	}
	if got.Body != "run make" || got.AuthorKind != "claude" || got.AuthorSession != "claude-main" || got.Revision != 1 {
		t.Fatalf("read by codex = %+v", got)
	}

	msg, err := memoryGitRun("log", "-1", "--format=%B", memoryBranch)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"AF-Trigger: agent-memory", "AF-Op: create", "AF-Author-Kind: claude", "AF-Author-Session: claude-main"} {
		if !strings.Contains(msg, want) {
			t.Errorf("commit message lacks %q:\n%s", want, msg)
		}
	}
	// The commit carries af/ alone: claude's live memory waits for its own snapshot rather than
	// being attributed to this author.
	files, err := memoryGitRun("show", "--name-only", "--format=", memoryBranch)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range strings.Split(files, "\n") {
		if !strings.HasPrefix(f, "af/projects/"+claudeC.Project.ID+"/") {
			t.Errorf("commit touched %q outside the project's af/ directory", f)
		}
	}
	if !strings.Contains(files, "project.json") {
		t.Errorf("project.json was not committed: %q", files)
	}
}

func TestAgentMemoryRevisionChecks(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	now := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	c := agentMemCallerT(t, "claude-main")
	save := func(rev int, body string) error {
		_, err := agentMemSave(c, agentMemSaveReq{Name: "m", Description: "d", Body: body, Revision: rev}, now)
		return err
	}
	if err := save(0, "v1"); err != nil {
		t.Fatal(err)
	}
	if code := agentMemCode(save(0, "again")); code != errCodeMemoryConflict {
		t.Errorf("create over an existing memory: code %q, want conflict", code)
	}
	if err := save(1, "v2"); err != nil {
		t.Fatalf("update from the current revision: %v", err)
	}
	if code := agentMemCode(save(1, "stale")); code != errCodeMemoryConflict {
		t.Errorf("update from a stale revision: code %q, want conflict", code)
	}
	if code := agentMemCode(save(5, "x")); code != errCodeMemoryConflict {
		t.Errorf("update from a future revision: code %q, want conflict", code)
	}
	e, err := agentMemRead(c, agentMemScopeProject, "m")
	if err != nil || e.Body != "v2" || e.Revision != 2 {
		t.Fatalf("after updates: %+v, %v", e, err)
	}
	if _, err := agentMemSave(c, agentMemSaveReq{Name: "gone", Description: "d", Body: "b", Revision: 3}, now); agentMemCode(err) != errCodeMemoryNotFound {
		t.Errorf("update of a missing memory: %v, want not_found", err)
	}
}

// A secret is refused before anything is written or committed, and the refusal names the rule
// and line without the value.
func TestAgentMemorySecretRefused(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	c := agentMemCallerT(t, "claude-main")
	key := "AKIA" + "ABCDEFGHIJKLMNOP"
	_, err := agentMemSave(c, agentMemSaveReq{Name: "aws", Description: "creds", Body: "line one\nkey " + key}, time.Now())
	var se *agentMemSecretErr
	if !errors.As(err, &se) || len(se.Findings) == 0 {
		t.Fatalf("err = %v, want a secret refusal", err)
	}
	b, _ := json.Marshal(se.Findings)
	if strings.Contains(string(b), key) {
		t.Fatalf("findings carry the raw value: %s", b)
	}
	if se.Findings[0].Rule != "aws-access-key-id" {
		t.Errorf("rule = %q", se.Findings[0].Rule)
	}
	if _, err := agentMemRead(c, "", "aws"); agentMemCode(err) != errCodeMemoryNotFound {
		t.Errorf("refused memory is readable: %v", err)
	}
	if memoryHasCommits() {
		t.Error("a refused write left a commit")
	}
}

func TestAgentMemoryForgetKeepsHistory(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	now := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	c := agentMemCallerT(t, "codex-wt")
	if _, err := agentMemSave(c, agentMemSaveReq{Name: "old", Description: "d", Body: "lesson"}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := agentMemForget(c, agentMemForgetReq{Name: "old", Revision: 7}, now); agentMemCode(err) != errCodeMemoryConflict {
		t.Fatalf("forget from a wrong revision: %v", err)
	}
	res, err := agentMemForget(c, agentMemForgetReq{Name: "old", Revision: 1}, now)
	if err != nil || !res.Deleted {
		t.Fatalf("forget: %+v, %v", res, err)
	}
	if _, err := agentMemRead(c, "", "old"); agentMemCode(err) != errCodeMemoryNotFound {
		t.Errorf("forgotten memory still readable: %v", err)
	}
	body, err := memoryGitRun("show", memoryBranch+"~1:af/projects/"+c.Project.ID+"/old.md")
	if err != nil || !strings.Contains(body, "lesson") {
		t.Errorf("history lost the forgotten memory: %q, %v", body, err)
	}
	msg, _ := memoryGitRun("log", "-1", "--format=%B", memoryBranch)
	if !strings.Contains(msg, "AF-Op: forget") || !strings.Contains(msg, "AF-Author-Kind: codex") {
		t.Errorf("forget commit message:\n%s", msg)
	}
}

// A session outside ~/repos has the user scope only; the user scope is shared by every project.
func TestAgentMemoryScopes(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	now := time.Now()
	shell, claudeC := agentMemCallerT(t, "shell-home"), agentMemCallerT(t, "claude-main")
	if shell.Project != nil {
		t.Fatalf("a home-directory session got a project: %+v", shell.Project)
	}
	if _, err := agentMemSave(shell, agentMemSaveReq{Scope: agentMemScopeProject, Name: "x", Description: "d", Body: "b"}, now); agentMemCode(err) != errCodeMemoryNoProject {
		t.Errorf("project scope without a project: %v", err)
	}
	if _, err := agentMemSave(shell, agentMemSaveReq{Name: "pref", Description: "user pref", Body: "answer in Japanese"}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := agentMemSave(claudeC, agentMemSaveReq{Name: "pref", Description: "project pref", Body: "project"}, now); err != nil {
		t.Fatal(err)
	}
	idx, err := agentMemListIndex(claudeC, 0)
	if err != nil {
		t.Fatal(err)
	}
	scopes := map[string]bool{}
	for _, e := range idx.Entries {
		if e.Body != "" {
			t.Errorf("index carries a body: %+v", e)
		}
		scopes[e.Scope] = true
	}
	if !scopes[agentMemScopeUser] || !scopes[agentMemScopeProject] {
		t.Errorf("index scopes = %v, want both", scopes)
	}
	// With no scope a read prefers the project; an explicit scope reaches the other.
	if e, _ := agentMemRead(claudeC, "", "pref"); e.Body != "project" {
		t.Errorf("default read = %q, want the project's", e.Body)
	}
	if e, _ := agentMemRead(claudeC, agentMemScopeUser, "pref"); e.Body != "answer in Japanese" {
		t.Errorf("user read = %q", e.Body)
	}
}

func TestAgentMemoryKindsFilterAndSearch(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	now := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	claudeC, codexC := agentMemCallerT(t, "claude-main"), agentMemCallerT(t, "codex-wt")
	if _, err := agentMemSave(claudeC, agentMemSaveReq{Name: "claude-only", Description: "tui quirk", Kinds: []string{"claude"}, Body: "The spinner regex drifts"}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := agentMemSave(claudeC, agentMemSaveReq{Name: "shared", Description: "go tests", Body: "Run go test with -p 2 when memory is tight\nunrelated line"}, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	idx, _ := agentMemListIndex(codexC, 0)
	if len(idx.Entries) != 1 || idx.Entries[0].Name != "shared" {
		t.Errorf("codex index = %+v, want only the shared memory", idx.Entries)
	}
	idx, _ = agentMemListIndex(claudeC, 0)
	if len(idx.Entries) != 2 || idx.Entries[0].Name != "shared" {
		t.Errorf("claude index = %+v, want both, newest first", idx.Entries)
	}
	hits, err := agentMemSearch(codexC, "GO  tight", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Name != "shared" || len(hits[0].Snippets) != 1 || hits[0].Body != "" {
		t.Fatalf("hits = %+v", hits)
	}
	if hits, _ := agentMemSearch(codexC, "spinner", 0); len(hits) != 0 {
		t.Errorf("search reached a memory for another kind: %+v", hits)
	}
	if _, err := agentMemSearch(codexC, "  ", 0); agentMemCode(err) != errCodeMemoryBadRequest {
		t.Errorf("empty query: %v", err)
	}
}

func TestAgentMemoryRenderParseRoundTrip(t *testing.T) {
	in := agentMemEntry{
		Name: "x", Description: `has: a colon and "quotes"`, Type: "feedback", Kinds: []string{"claude", "codex"},
		Revision: 3, AuthorKind: "claude", AuthorSession: "s", Created: "2026-10-04T00:00:00Z",
		Updated: "2026-10-04T01:00:00Z", Body: "line 1\n---\nline 3",
	}
	out, ok := agentMemParse(agentMemRender(in))
	if !ok {
		t.Fatal("did not parse")
	}
	out.Name, out.Scope = in.Name, in.Scope
	a, _ := json.Marshal(in)
	b, _ := json.Marshal(out)
	if string(a) != string(b) {
		t.Fatalf("round trip\n in: %s\nout: %s", a, b)
	}
	// A claude memory file (plain YAML, nested metadata) still loads.
	e, ok := agentMemParse([]byte("---\nname: n\ndescription: plain text\nmetadata:\n  type: user\n---\n\nbody\n"))
	if !ok || e.Description != "plain text" || strings.TrimSpace(e.Body) != "body" {
		t.Fatalf("claude-style file = %+v, %v", e, ok)
	}
}

func TestAgentMemoryValidation(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	c := agentMemCallerT(t, "claude-main")
	for _, req := range []agentMemSaveReq{
		{Name: "Bad Name", Description: "d", Body: "b"},
		{Name: "../escape", Description: "d", Body: "b"},
		{Name: "ok", Description: "", Body: "b"},
		{Name: "ok", Description: "two\nlines", Body: "b"},
		{Name: "ok", Description: "d", Body: ""},
		{Name: "ok", Description: "d", Body: "b", Type: "order"},
		{Name: "ok", Description: "d", Body: "b", Kinds: []string{"Claude Code"}},
		{Name: "ok", Description: "d", Body: "b", Scope: "global"},
	} {
		if _, err := agentMemSave(c, req, time.Now()); agentMemCode(err) != errCodeMemoryBadRequest {
			t.Errorf("%+v: err %v, want bad_request", req, err)
		}
	}
	if _, err := agentMemResolveCaller("no-such-session"); agentMemCode(err) != errCodeMemoryBadRequest {
		t.Errorf("unknown session: %v", err)
	}
	// No session at all is an author AF cannot establish, recorded as unknown.
	anon, err := agentMemResolveCaller("")
	if err != nil || anon.Kind != agentMemUnknown || anon.Session != agentMemUnknown || anon.Project != nil {
		t.Errorf("anonymous caller = %+v, %v", anon, err)
	}
}

// The REST refusal of a secret is 422 with findings, and the response never carries the value.
func TestAgentMemorySaveHandlerSecret(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	mux := buildMux()
	key := "ghp_" + strings.Repeat("a1B2", 9)
	body, _ := json.Marshal(agentMemSaveReq{Session: "claude-main", Name: "tok", Description: "d", Body: "token " + key})
	w := smokeDo(t, mux, http.MethodPost, "/agents/memory/entries", "", string(body))
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), key) || !strings.Contains(w.Body.String(), errCodeMemorySecretDetected) {
		t.Fatalf("body = %s", w.Body)
	}

	body, _ = json.Marshal(agentMemSaveReq{Session: "claude-main", Name: "fine", Description: "d", Body: "ok"})
	if w := smokeDo(t, mux, http.MethodPost, "/agents/memory/entries", "", string(body)); w.Code != http.StatusOK {
		t.Fatalf("save status %d: %s", w.Code, w.Body)
	}
	w = smokeDo(t, mux, http.MethodGet, "/agents/memory/entries/read?session=codex-wt&name=fine", "", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"authorKind":"claude"`) {
		t.Fatalf("read status %d: %s", w.Code, w.Body)
	}
	w = smokeDo(t, mux, http.MethodGet, "/agents/memory/entries?session=nope", "", "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unknown session status %d", w.Code)
	}
}

// ---- review round 1 (PR #1657): one test per finding ----

func agentMemFakeAWS() string { return "AKIA" + "ZXCVBNMLKJHGFDSA" }

// The scan cannot be dodged by NUL, by padding a line past the old 8 KiB cut, or by putting a
// placeholder of the same rule first on the line.
func TestAgentMemorySecretScanCannotBeDodged(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	c := agentMemCallerT(t, "claude-main")
	key := agentMemFakeAWS()
	for name, body := range map[string]string{
		"nul":         "note\x00 " + key,
		"padded":      strings.Repeat("x", 9000) + " " + key,
		"placeholder": "AKIAEXAMPLEEXAMPLE00 then " + key,
	} {
		_, err := agentMemSave(c, agentMemSaveReq{Name: "dodge", Description: "d", Body: body}, time.Now())
		var se *agentMemSecretErr
		if !errors.As(err, &se) {
			t.Errorf("%s: err = %v, want a refusal", name, err)
		}
	}
	// The shared scanner itself sees a key behind a placeholder of the same rule.
	if f := memoryScanContent("x", []byte("AKIAEXAMPLEEXAMPLE00 "+key)); len(f) != 1 {
		t.Errorf("scanner findings behind a placeholder = %+v", f)
	}
}

// The raw description is scanned: rendering escapes the quotes the generic rule looks for.
func TestAgentMemoryDescriptionScannedRaw(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	c := agentMemCallerT(t, "claude-main")
	// Assembled at run time: a literal of this shape is what the repository's own secret scan
	// (gitleaks over every pushed branch) flags.
	desc := "pass" + "word" + `: "` + "Q7v5M9w2" + "J8s6R4p3" + `"`
	_, err := agentMemSave(c, agentMemSaveReq{Name: "pw", Description: desc, Body: "b"}, time.Now())
	var se *agentMemSecretErr
	if !errors.As(err, &se) || se.Findings[0].Path != "description" {
		t.Fatalf("err = %v, want a refusal on description", err)
	}
}

// A file put in the store by hand is scanned before it is shown, and a write never sweeps it
// into its own commit.
func TestAgentMemoryHandEditedFileIsWithheldAndNotCommitted(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	c := agentMemCallerT(t, "claude-main")
	key := agentMemFakeAWS()
	memoryMkdirAll(t, filepath.Join(agentMemDir(), "user"))
	memoryWrite(t, filepath.Join(agentMemDir(), "user", "manual.md"), "---\nname: manual\ndescription: key "+key+"\n---\nbody\n")

	idx, err := agentMemListIndex(c, 0)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(idx)
	if strings.Contains(string(b), key) || idx.Withheld != 1 {
		t.Fatalf("index = %s", b)
	}
	if _, err := agentMemRead(c, "", "manual"); agentMemCode(err) != errCodeMemorySecretDetected || strings.Contains(err.Error(), key) {
		t.Fatalf("read of a withheld file = %v", err)
	}
	if hits, _ := agentMemSearch(c, "key", 0); len(hits) != 0 {
		t.Fatalf("search reached a withheld file: %+v", hits)
	}

	if _, err := agentMemSave(c, agentMemSaveReq{Name: "safe", Description: "d", Body: "b"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	files, _ := memoryGitRun("ls-tree", "-r", "--name-only", memoryBranch)
	if strings.Contains(files, "manual.md") {
		t.Fatalf("a hand-edited file was committed with someone else's write:\n%s", files)
	}
}

// A failed commit leaves the store, staging and the index as they were, so the next snapshot
// cannot commit the change either.
func TestAgentMemoryFailedCommitRollsBackStaging(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	c := agentMemCallerT(t, "claude-main")
	if _, err := agentMemSave(c, agentMemSaveReq{Name: "m", Description: "d", Body: "v1"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(memoryRepoDir(), "index.lock")
	memoryWrite(t, lock, "")
	if _, err := agentMemSave(c, agentMemSaveReq{Name: "m", Description: "d", Body: "v2", Revision: 1}, time.Now()); err == nil {
		t.Fatal("save succeeded with index.lock held")
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	if e, _ := agentMemRead(c, "", "m"); e.Body != "v1" || e.Revision != 1 {
		t.Fatalf("live after a failed save = %+v", e)
	}
	if _, err := memorySnapshot(memoryTriggerManual, time.Now()); err != nil {
		t.Fatal(err)
	}
	body, _ := memoryGitRun("show", memoryBranch+":af/projects/"+c.Project.ID+"/m.md")
	if strings.Contains(body, "v2") {
		t.Fatalf("the failed change reached history through a later snapshot:\n%s", body)
	}
}

// Symlinks inside the store are refused for reading and for writing.
func TestAgentMemorySymlinksRefused(t *testing.T) {
	home, _, _ := agentMemTestEnv(t)
	c := agentMemCallerT(t, "shell-home")
	outside := filepath.Join(home, "outside")
	memoryMkdirAll(t, outside)
	memoryWrite(t, filepath.Join(outside, "x.md"), "---\nname: x\ndescription: d\n---\nexternal\n")
	memoryMkdirAll(t, filepath.Join(agentMemDir(), "user"))
	if err := os.Symlink(filepath.Join(outside, "x.md"), filepath.Join(agentMemDir(), "user", "x.md")); err != nil {
		t.Fatal(err)
	}
	if e, err := agentMemRead(c, "", "x"); err == nil {
		t.Fatalf("read followed a symlink: %+v", e)
	}
	if err := os.RemoveAll(filepath.Join(agentMemDir(), "user")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(agentMemDir(), "user")); err != nil {
		t.Fatal(err)
	}
	if _, err := agentMemSave(c, agentMemSaveReq{Name: "y", Description: "d", Body: "b"}, time.Now()); err == nil {
		t.Fatal("save wrote through a symlinked scope directory")
	}
	if _, err := os.Stat(filepath.Join(outside, "y.md")); err == nil {
		t.Fatal("a file was written outside the store")
	}
}

// Two repositories whose git dirs share a parent are two projects.
func TestAgentMemorySeparateGitDirsAreDistinctProjects(t *testing.T) {
	home, _, _ := agentMemTestEnv(t)
	meta := filepath.Join(home, "metadata")
	memoryMkdirAll(t, meta)
	ids := map[string]bool{}
	for _, n := range []string{"a", "b"} {
		dir := filepath.Join(home, "repos", n)
		memoryMkdirAll(t, dir)
		cmd := exec.Command("git", "init", "--quiet", "--separate-git-dir", filepath.Join(meta, n), dir)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git init: %v: %s", err, out)
		}
		p := agentMemProjectFor(dir)
		if p == nil || p.Root != dir {
			t.Fatalf("project for %s = %+v", dir, p)
		}
		ids[p.ID] = true
	}
	if len(ids) != 2 {
		t.Fatalf("separate git dirs collided: %v", ids)
	}
}

func TestAgentMemorySessionNameIsValidated(t *testing.T) {
	home, _, _ := agentMemTestEnv(t)
	// The forged meta names itself as the caller asked, so only the name check stops it.
	memoryWrite(t, filepath.Join(home, "forged.json"), `{"name":"../forged","dir":"/","kind":"claude"}`)
	for _, n := range []string{"../forged", "a/b", "claude-main\x00"} {
		if _, err := agentMemResolveCaller(n); agentMemCode(err) != errCodeMemoryBadRequest {
			t.Errorf("%q: %v", n, err)
		}
	}
}

// A file with no revision reads as revision 1, so a blind create cannot overwrite it.
func TestAgentMemoryHandFileWithoutRevisionIsNotOverwritten(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	c := agentMemCallerT(t, "shell-home")
	memoryMkdirAll(t, filepath.Join(agentMemDir(), "user"))
	memoryWrite(t, filepath.Join(agentMemDir(), "user", "x.md"), "---\nname: x\ndescription: d\n---\nmine\n")
	if _, err := agentMemSave(c, agentMemSaveReq{Name: "x", Description: "d", Body: "blind"}, time.Now()); agentMemCode(err) != errCodeMemoryConflict {
		t.Fatalf("blind create over a hand file: %v", err)
	}
	if res, err := agentMemSave(c, agentMemSaveReq{Name: "x", Description: "d", Body: "read first", Revision: 1}, time.Now()); err != nil || res.Revision != 2 {
		t.Fatalf("update from revision 1: %+v, %v", res, err)
	}
}

// Forget then re-create never reuses a revision, so a reader holding the old one is refused.
func TestAgentMemoryRecreateDoesNotReuseRevision(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	c := agentMemCallerT(t, "claude-main")
	if _, err := agentMemSave(c, agentMemSaveReq{Name: "x", Description: "d", Body: "old"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := agentMemForget(c, agentMemForgetReq{Name: "x", Revision: 1}, time.Now()); err != nil {
		t.Fatal(err)
	}
	res, err := agentMemSave(c, agentMemSaveReq{Name: "x", Description: "d", Body: "new"}, time.Now())
	if err != nil || res.Revision != 2 {
		t.Fatalf("re-create = %+v, %v", res, err)
	}
	if _, err := agentMemSave(c, agentMemSaveReq{Name: "x", Description: "d", Body: "stale", Revision: 1}, time.Now()); agentMemCode(err) != errCodeMemoryConflict {
		t.Fatalf("stale save after re-create: %v", err)
	}
	if _, err := agentMemForget(c, agentMemForgetReq{Name: "x", Revision: 1}, time.Now()); agentMemCode(err) != errCodeMemoryConflict {
		t.Fatalf("stale forget after re-create: %v", err)
	}
}

// Readers wait for a write in progress, so they never see a change that may be rolled back.
func TestAgentMemoryReadersWaitForWriter(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	c := agentMemCallerT(t, "claude-main")
	agentMemMu.Lock()
	done := make(chan struct{})
	go func() { _, _ = agentMemListIndex(c, 0); close(done) }()
	select {
	case <-done:
		agentMemMu.Unlock()
		t.Fatal("index did not wait for the writer")
	case <-time.After(100 * time.Millisecond):
	}
	agentMemMu.Unlock()
	<-done
}

// Refusals before the scan never echo the value.
func TestAgentMemoryErrorsDoNotEchoInput(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	c := agentMemCallerT(t, "claude-main")
	key := agentMemFakeAWS()
	errs := []error{}
	_, err := agentMemSave(c, agentMemSaveReq{Name: key, Description: "d", Body: "b"}, time.Now())
	errs = append(errs, err)
	_, err = agentMemSave(c, agentMemSaveReq{Name: "ok", Description: "d", Body: "b", Kinds: []string{key}}, time.Now())
	errs = append(errs, err)
	_, err = agentMemRead(c, "", key)
	errs = append(errs, err)
	_, err = agentMemForget(c, agentMemForgetReq{Name: key, Revision: 1}, time.Now())
	errs = append(errs, err)
	_, err = agentMemResolveCaller(key)
	errs = append(errs, err)
	for i, err := range errs {
		if err == nil || strings.Contains(err.Error(), key) {
			t.Errorf("case %d: %v", i, err)
		}
	}
}

// ---- review round 2 (PR #1657) ----
// Secret-shaped values are assembled at run time: gitleaks scans every pushed branch.

func agentMemFakeSlackName() string { return "xo" + "xb-" + "1234567890" + "-abcdefghij" }

// What a reader is shown is scanned decoded: a JSON escape cannot hide a key, and a name that is
// itself secret-shaped withholds the file.
func TestAgentMemoryPublishedValuesAreScanned(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	c := agentMemCallerT(t, "shell-home")
	escaped := `A` + agentMemFakeAWS()[1:]
	memoryMkdirAll(t, filepath.Join(agentMemDir(), "user"))
	memoryWrite(t, filepath.Join(agentMemDir(), "user", "esc.md"), "---\nname: \"esc\"\ndescription: \"key "+escaped+"\"\n---\nbody\n")
	memoryWrite(t, filepath.Join(agentMemDir(), "user", agentMemFakeSlackName()+".md"), "---\nname: x\ndescription: safe\n---\nsafe\n")
	idx, err := agentMemListIndex(c, 0)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(idx)
	if idx.Withheld != 2 || strings.Contains(string(b), agentMemFakeAWS()) || strings.Contains(string(b), agentMemFakeSlackName()) {
		t.Fatalf("index = %s", b)
	}
}

// A refusal never builds a finding path from the name.
func TestAgentMemorySecretNameNotEchoed(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	c := agentMemCallerT(t, "shell-home")
	_, err := agentMemSave(c, agentMemSaveReq{Name: agentMemFakeSlackName(), Description: "d", Body: "b"}, time.Now())
	var se *agentMemSecretErr
	if !errors.As(err, &se) {
		t.Fatalf("err = %v, want a refusal", err)
	}
	b, _ := json.Marshal(se.Findings)
	if strings.Contains(string(b), agentMemFakeSlackName()) {
		t.Fatalf("findings carry the name: %s", b)
	}
}

// The tombstone directory is held to the same symlink rule, and a tombstone that cannot be
// written refuses the forget instead of losing the revision.
func TestAgentMemoryTombstoneSafety(t *testing.T) {
	home, _, _ := agentMemTestEnv(t)
	c := agentMemCallerT(t, "shell-home")
	outside := filepath.Join(home, "outside")
	memoryMkdirAll(t, outside)
	memoryWrite(t, filepath.Join(outside, "x"), "17\n")
	memoryMkdirAll(t, filepath.Join(agentMemDir(), "user"))
	if err := os.Symlink(outside, filepath.Join(agentMemDir(), "user", agentMemTombDir)); err != nil {
		t.Fatal(err)
	}
	if _, err := agentMemSave(c, agentMemSaveReq{Name: "x", Description: "d", Body: "b"}, time.Now()); err == nil {
		t.Fatal("create read a tombstone through a symlink")
	}
	if _, err := os.Stat(filepath.Join(outside, "x")); err != nil {
		t.Fatalf("the outside file was touched: %v", err)
	}

	if err := os.Remove(filepath.Join(agentMemDir(), "user", agentMemTombDir)); err != nil {
		t.Fatal(err)
	}
	if _, err := agentMemSave(c, agentMemSaveReq{Name: "y", Description: "d", Body: "b"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	memoryWrite(t, filepath.Join(agentMemDir(), "user", agentMemTombDir), "not a directory")
	if _, err := agentMemForget(c, agentMemForgetReq{Name: "y", Revision: 1}, time.Now()); err == nil {
		t.Fatal("forget succeeded without a tombstone")
	}
	if e, err := agentMemRead(c, "", "y"); err != nil || e.Body != "b" {
		t.Fatalf("memory after a refused forget = %+v, %v", e, err)
	}
}

// An oversized file is refused, never truncated (and so never written back truncated).
func TestAgentMemoryOversizedFileRefused(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	c := agentMemCallerT(t, "shell-home")
	big := "---\nname: big\ndescription: d\n---\n" + strings.Repeat("line\n", agentMemMaxFile/5+10)
	memoryMkdirAll(t, filepath.Join(agentMemDir(), "user"))
	p := filepath.Join(agentMemDir(), "user", "big.md")
	memoryWrite(t, p, big)
	if _, err := agentMemRead(c, "", "big"); err == nil {
		t.Fatal("read an oversized file")
	}
	if _, err := agentMemSave(c, agentMemSaveReq{Name: "big", Description: "d", Body: "small", Revision: 1}, time.Now()); err == nil {
		t.Fatal("updated an oversized file")
	}
	if b, _ := os.ReadFile(p); string(b) != big {
		t.Fatal("the oversized file was changed")
	}
}

// A FIFO in the store is skipped, not opened.
func TestAgentMemoryFIFODoesNotBlock(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	c := agentMemCallerT(t, "shell-home")
	memoryMkdirAll(t, filepath.Join(agentMemDir(), "user"))
	if err := syscall.Mkfifo(filepath.Join(agentMemDir(), "user", "fifo.md"), 0o600); err != nil {
		t.Skip("mkfifo:", err)
	}
	done := make(chan struct{})
	go func() {
		_, _ = agentMemListIndex(c, 0)
		_, _ = agentMemRead(c, "", "fifo")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a FIFO blocked the store")
	}
}

// project.json is committed, so the folder name it records is scanned too.
func TestAgentMemoryProjectInfoScanned(t *testing.T) {
	home, _, _ := agentMemTestEnv(t)
	dir := filepath.Join(home, "repos", agentMemFakeAWS())
	memoryMkdirAll(t, dir)
	session.WriteMeta(session.Meta{Name: "odd", Dir: dir, Kind: "claude"})
	c := agentMemCallerT(t, "odd")
	_, err := agentMemSave(c, agentMemSaveReq{Name: "m", Description: "d", Body: "b"}, time.Now())
	var se *agentMemSecretErr
	if !errors.As(err, &se) || se.Findings[0].Path != "project" {
		t.Fatalf("err = %v, want a refusal on project", err)
	}
	if memoryHasCommits() {
		t.Fatal("project info with a secret was committed")
	}
}

// Every worktree of one bare repository is one project.
func TestAgentMemoryBareRepoWorktreesShareAProject(t *testing.T) {
	home, _, _ := agentMemTestEnv(t)
	bare := filepath.Join(home, "bare.git")
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	git(home, "init", "--quiet", "--bare", "-b", "main", bare)
	seed := filepath.Join(home, "seed")
	git(home, "clone", "--quiet", bare, seed)
	git(seed, "commit", "--quiet", "--allow-empty", "-m", "init")
	git(seed, "push", "--quiet", "origin", "main")
	left, right := filepath.Join(home, "repos", "left"), filepath.Join(home, "repos", "right")
	git(bare, "worktree", "add", "--quiet", left, "main")
	git(bare, "worktree", "add", "--quiet", "-b", "other", right)
	pl, pr := agentMemProjectFor(left), agentMemProjectFor(right)
	if pl == nil || pr == nil || pl.ID != pr.ID {
		t.Fatalf("bare worktrees: %+v vs %+v", pl, pr)
	}
}

// A memory put in the store by hand, never committed, can still be forgotten, and the forget is
// recorded with its author.
func TestAgentMemoryForgetUntrackedFile(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	c := agentMemCallerT(t, "shell-home")
	memoryMkdirAll(t, filepath.Join(agentMemDir(), "user"))
	memoryWrite(t, filepath.Join(agentMemDir(), "user", "manual.md"), "---\nname: manual\ndescription: d\n---\nhand\n")
	res, err := agentMemForget(c, agentMemForgetReq{Name: "manual", Revision: 1}, time.Now())
	if err != nil || !res.Deleted {
		t.Fatalf("forget = %+v, %v", res, err)
	}
	if _, err := agentMemRead(c, "", "manual"); agentMemCode(err) != errCodeMemoryNotFound {
		t.Fatalf("read after forget: %v", err)
	}
	msg, _ := memoryGitRun("log", "-1", "--format=%B", memoryBranch)
	if !strings.Contains(msg, "AF-Op: forget") || !strings.Contains(msg, "AF-Author-Kind: shell") {
		t.Fatalf("forget not recorded:\n%s", msg)
	}
}

// ---- review round 3 (PR #1657) ----

// The raw folder name is scanned too: JSON escapes the quotes the generic rule needs.
func TestAgentMemoryProjectInfoScannedRaw(t *testing.T) {
	home, _, _ := agentMemTestEnv(t)
	dir := filepath.Join(home, "repos", "pass"+"word: \""+"Q7v5M9w2"+"J8s6R4p3"+"\"")
	memoryMkdirAll(t, dir)
	session.WriteMeta(session.Meta{Name: "odd2", Dir: dir, Kind: "claude"})
	_, err := agentMemSave(agentMemCallerT(t, "odd2"), agentMemSaveReq{Name: "m", Description: "d", Body: "b"}, time.Now())
	var se *agentMemSecretErr
	if !errors.As(err, &se) || se.Findings[0].Path != "project" {
		t.Fatalf("err = %v, want a refusal on project", err)
	}
}

// A git failure while checking whether a path is tracked fails the forget instead of
// recording an empty one.
func TestAgentMemoryForgetFailsWhenIndexUnreadable(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	c := agentMemCallerT(t, "shell-home")
	if _, err := agentMemSave(c, agentMemSaveReq{Name: "x", Description: "d", Body: "b"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	head, _ := memoryGitRun("rev-parse", memoryBranch)
	memoryWrite(t, filepath.Join(memoryRepoDir(), "index"), "corrupt")
	if _, err := agentMemForget(c, agentMemForgetReq{Name: "x", Revision: 1}, time.Now()); err == nil {
		t.Fatal("forget succeeded with an unreadable index")
	}
	if now, _ := memoryGitRun("rev-parse", memoryBranch); now != head {
		t.Fatal("a commit was recorded")
	}
	if e, err := agentMemRead(c, "", "x"); err != nil || e.Body != "b" {
		t.Fatalf("memory after a failed forget = %+v, %v", e, err)
	}
}

// A file that is there but cannot be read as a memory is counted as withheld, not ignored.
func TestAgentMemoryMalformedFilesAreCounted(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	c := agentMemCallerT(t, "shell-home")
	memoryMkdirAll(t, filepath.Join(agentMemDir(), "user"))
	memoryWrite(t, filepath.Join(agentMemDir(), "user", "malformed.md"), "no frontmatter\n")
	memoryWrite(t, filepath.Join(agentMemDir(), "user", "big.md"), "---\nname: big\ndescription: d\n---\n"+strings.Repeat("x\n", agentMemMaxFile))
	idx, err := agentMemListIndex(c, 0)
	if err != nil || idx.Withheld != 2 || len(idx.Entries) != 0 {
		t.Fatalf("index = %+v, %v", idx, err)
	}
}

// ---- review round 4 (PR #1657) ----

// A token-shaped name never comes back out: read and forget refuse it before touching a path,
// and an OS error that quotes a path reaches neither the response nor the log.
func TestAgentMemoryUnreadableTokenNamedFileNotEchoed(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	name := agentMemFakeSlackName()
	memoryMkdirAll(t, filepath.Join(agentMemDir(), "user"))
	p := filepath.Join(agentMemDir(), "user", name+".md")
	memoryWrite(t, p, "---\nname: x\ndescription: d\n---\nb\n")
	if err := os.Chmod(p, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(p, 0o600) })

	var logs strings.Builder
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	mux := buildMux()
	for _, req := range []struct{ method, path, body string }{
		{http.MethodGet, "/agents/memory/entries/read?session=shell-home&name=" + name, ""},
		{http.MethodPost, "/agents/memory/entries/forget", `{"session":"shell-home","name":"` + name + `","revision":1}`},
		{http.MethodGet, "/agents/memory/entries?session=shell-home", ""},
	} {
		w := smokeDo(t, mux, req.method, req.path, "", req.body)
		if strings.Contains(w.Body.String(), name) {
			t.Errorf("%s %s echoed the name: %s", req.method, req.path, w.Body)
		}
	}
	// The boundary itself: a path-bearing OS error is reduced to its kind.
	w := httptest.NewRecorder()
	agentMemWriteErr(w, &os.PathError{Op: "open", Path: "/x/user/" + name + ".md", Err: fs.ErrPermission})
	if strings.Contains(w.Body.String(), name) || !strings.Contains(w.Body.String(), "permission denied") {
		t.Errorf("boundary response = %s", w.Body)
	}
	if strings.Contains(logs.String(), name) {
		t.Errorf("log carries the name:\n%s", logs.String())
	}
}

// With the switch off (the default, ADR 0108) the tools' routes refuse every session, while the
// Console's change list still answers, so what was written while it was on can be reviewed.
func TestAgentMemoryRoutesRefuseWhenSwitchedOff(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	if _, err := agentMemSave(agentMemCallerT(t, "claude-main"), agentMemSaveReq{Name: "a", Description: "d", Body: "v1"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, hook := range []func() bool{nil, func() bool { return false }} {
		AgentMemoryEnabled = hook
		mux := buildMux()
		for _, r := range []struct{ method, path, body string }{
			{http.MethodGet, "/agents/memory/entries?session=claude-main", ""},
			{http.MethodGet, "/agents/memory/entries/search?session=claude-main&q=v1", ""},
			{http.MethodGet, "/agents/memory/entries/read?session=claude-main&name=a", ""},
			{http.MethodPost, "/agents/memory/entries", `{"session":"claude-main","name":"b","description":"d","body":"x"}`},
			{http.MethodPost, "/agents/memory/entries/forget", `{"session":"claude-main","name":"a","revision":1}`},
		} {
			w := smokeDo(t, mux, r.method, r.path, "", r.body)
			if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), errCodeMemoryDisabled) {
				t.Errorf("%s %s with the switch off: %d %s", r.method, r.path, w.Code, w.Body)
			}
		}
		if w := smokeDo(t, mux, http.MethodGet, "/agents/memory/entries/changes", "", ""); w.Code != http.StatusOK {
			t.Errorf("change list with the switch off: %d %s", w.Code, w.Body)
		}
	}
	if _, err := agentMemRead(agentMemCallerT(t, "claude-main"), "", "a"); err != nil {
		t.Fatalf("the memory itself is kept: %v", err)
	}
}

// IndexJSONFor is what lcpp's system prompt is built from: the project's entries for a working
// copy and a kind, with no session to name, bounded by the budget.
func TestIndexJSONForServesAWorkingCopyWithoutASession(t *testing.T) {
	_, clone, wt := agentMemTestEnv(t)
	now := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	c := agentMemCallerT(t, "claude-main")
	if _, err := agentMemSave(c, agentMemSaveReq{Name: "build-cmd", Description: "how to build", Body: "run make", Type: "project"}, now); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{clone, wt} { // a worktree shares its clone's memory
		raw, err := IndexJSONFor(dir, "lcpp", 0)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(raw, `"build-cmd"`) || strings.Contains(raw, "run make") {
			t.Errorf("%s: want the entry without its body, got %s", dir, raw)
		}
	}
	// Outside ~/repos there is no project: user scope only, so the project's entry is absent.
	raw, err := IndexJSONFor(t.TempDir(), "lcpp", 0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "build-cmd") {
		t.Errorf("a directory outside ~/repos must not see the project's memory: %s", raw)
	}
}
