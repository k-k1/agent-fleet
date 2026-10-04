package memoryx

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	idx, err := agentMemListIndex(claudeC)
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
	idx, _ := agentMemListIndex(codexC)
	if len(idx.Entries) != 1 || idx.Entries[0].Name != "shared" {
		t.Errorf("codex index = %+v, want only the shared memory", idx.Entries)
	}
	idx, _ = agentMemListIndex(claudeC)
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
