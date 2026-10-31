package memoryx

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func externalMode(t *testing.T, mode string) {
	t.Helper()
	old := ExternalMemoryMode
	ExternalMemoryMode = func() string { return mode }
	t.Cleanup(func() { ExternalMemoryMode = old })
}

const extBase = "/agents/memory/external/"

// Every external route refuses while the setting is off or unset, whatever the in-workspace
// switch says, and writes need the third state.
func TestAgentMemoryExternalGate(t *testing.T) {
	_, _, _ = agentMemTestEnv(t) // agentMemory switch ON
	reads := []string{"projects", "entries", "search?q=x", "read?name=x"}
	for _, mode := range []string{"", "off", "bogus"} {
		externalMode(t, mode)
		for _, p := range reads {
			code, b := memberDo(t, http.MethodGet, extBase+p, nil)
			if code != http.StatusForbidden || !strings.Contains(string(b), errCodeMemoryExternalOff) {
				t.Errorf("mode %q GET %s: %d %s", mode, p, code, b)
			}
		}
	}
	old := ExternalMemoryMode
	ExternalMemoryMode = nil
	if code, _ := memberDo(t, http.MethodGet, extBase+"projects", nil); code != http.StatusForbidden {
		t.Errorf("nil hook: %d", code)
	}
	ExternalMemoryMode = old

	externalMode(t, ExternalMemoryRead)
	if code, _ := memberDo(t, http.MethodGet, extBase+"entries", nil); code != http.StatusOK {
		t.Errorf("read mode index: %d", code)
	}
	for _, p := range []string{"entries", "forget"} {
		code, b := memberDo(t, http.MethodPost, extBase+p, map[string]any{"name": "x", "description": "d", "type": "project", "body": "b"})
		if code != http.StatusForbidden || !strings.Contains(string(b), "read-only") {
			t.Errorf("read mode POST %s: %d %s", p, code, b)
		}
	}
}

// The switch for in-workspace sessions does not open the external routes' counterpart the other
// way round either: the sessions' routes keep answering to agentMemory alone.
func TestAgentMemoryExternalIsSeparateFromAgentMemory(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	externalMode(t, ExternalMemoryWrite)
	AgentMemoryEnabled = func() bool { return false }
	if code, _ := memberDo(t, http.MethodGet, extBase+"entries", nil); code != http.StatusOK {
		t.Errorf("external with agentMemory off: %d", code)
	}
	if code, _ := memberDo(t, http.MethodGet, "/agents/memory/entries", nil); code != http.StatusForbidden {
		t.Errorf("session route with agentMemory off: %d", code)
	}
}

// An outside client lists projects without paths, names one explicitly, saves as author kind
// "external" with its (sanitised) client name in the commit, reads, and forgets.
func TestAgentMemoryExternalRoundTrip(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	c := agentMemCallerT(t, "claude-main")
	agentMemSaveN(t, c, "seed", "project", time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC))
	externalMode(t, ExternalMemoryWrite)

	code, b := memberDo(t, http.MethodGet, extBase+"projects", nil)
	var pl struct {
		Projects []map[string]any `json:"projects"`
	}
	if code != 200 || json.Unmarshal(b, &pl) != nil || len(pl.Projects) != 1 || pl.Projects[0]["id"] != c.Project.ID {
		t.Fatalf("projects: %d %s", code, b)
	}
	if strings.Contains(string(b), c.Project.Root) || strings.Contains(string(b), "root") {
		t.Fatalf("projects leak the working copy path: %s", b)
	}

	code, b = memberDo(t, http.MethodPost, extBase+"entries", map[string]any{
		"project": c.Project.ID, "scope": "project", "name": "from-outside", "description": "d", "type": "project",
		"body": "hello", "client": "claude-code\n/1.2 <x>", "session": "claude-main",
	})
	var wr agentMemWriteResult
	if code != 200 || json.Unmarshal(b, &wr) != nil || wr.Revision != 1 {
		t.Fatalf("save: %d %s", code, b)
	}
	code, b = memberDo(t, http.MethodGet, extBase+"read?project="+c.Project.ID+"&scope=project&name=from-outside", nil)
	var e agentMemEntry
	if code != 200 || json.Unmarshal(b, &e) != nil || e.AuthorKind != "external" || e.AuthorSession != "unknown" || e.Body != "hello" {
		t.Fatalf("read: %d %s", code, b)
	}
	msg, err := memoryGitRun("log", "-1", "--format=%B")
	if err != nil || !strings.Contains(msg, "AF-Author-Kind: external") || !strings.Contains(msg, "AF-External-Client: claude-code/1.2 x") {
		t.Fatalf("commit message: %q %v", msg, err)
	}
	if strings.Contains(msg, "<") {
		t.Fatalf("client name not sanitised: %q", msg)
	}

	if code, b = memberDo(t, http.MethodGet, extBase+"search?project="+c.Project.ID+"&q=hello", nil); code != 200 || !strings.Contains(string(b), "from-outside") {
		t.Fatalf("search: %d %s", code, b)
	}
	// Without a project only the user scope is visible.
	if code, b = memberDo(t, http.MethodGet, extBase+"entries", nil); code != 200 || strings.Contains(string(b), "from-outside") {
		t.Fatalf("index without project: %d %s", code, b)
	}
	code, b = memberDo(t, http.MethodPost, extBase+"forget", map[string]any{
		"project": c.Project.ID, "scope": "project", "name": "from-outside", "revision": 1,
	})
	if code != 200 || !strings.Contains(string(b), `"deleted":true`) {
		t.Fatalf("forget: %d %s", code, b)
	}
}

// A project the store does not hold is refused, not created; the secret scan runs unchanged.
func TestAgentMemoryExternalRefusals(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	externalMode(t, ExternalMemoryWrite)
	code, _ := memberDo(t, http.MethodPost, extBase+"entries", map[string]any{
		"project": "nosuch", "scope": "project", "name": "x", "description": "d", "type": "project", "body": "b"})
	if code != http.StatusNotFound {
		t.Errorf("unknown project: %d", code)
	}
	code, b := memberDo(t, http.MethodPost, extBase+"entries", map[string]any{
		"scope": "user", "name": "leak", "description": "d", "type": "user", "body": "token ghp_" + strings.Repeat("a1B2", 9)})
	if code != http.StatusUnprocessableEntity || !strings.Contains(string(b), errCodeMemorySecretDetected) {
		t.Errorf("secret: %d %s", code, b)
	}
}

// A memory limited to other kinds is not readable by name from outside, and the refused read is not
// a use; one limited to "external" is.
func TestAgentMemoryExternalReadHonoursKinds(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	c := agentMemCallerT(t, "claude-main")
	externalMode(t, ExternalMemoryRead)
	for name, kinds := range map[string][]string{"only-claude": {"claude"}, "for-outside": {"external"}} {
		if _, err := agentMemSave(c, agentMemSaveReq{Name: name, Description: "d", Type: "project", Body: "b", Kinds: kinds}, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	q := extBase + "read?project=" + c.Project.ID + "&scope=project&name="
	if code, b := memberDo(t, http.MethodGet, q+"only-claude", nil); code != http.StatusNotFound || strings.Contains(string(b), `"body"`) {
		t.Fatalf("kind-limited read: %d %s", code, b)
	}
	if got := agentMemLoadUsage("projects/" + c.Project.ID)["only-claude"]; got != 0 {
		t.Fatalf("a refused read counted as a use: %d", got)
	}
	if code, _ := memberDo(t, http.MethodGet, q+"for-outside", nil); code != http.StatusOK {
		t.Fatalf("external-kind read: %d", code)
	}
}

// extRepo seeds a project with one memory whose working copy has the given origin.
func extRepo(t *testing.T, home, name, origin string) agentMemCaller {
	t.Helper()
	remoteRepo(t, home, name, origin)
	c := agentMemCallerT(t, "claude-"+name)
	agentMemSaveN(t, c, "seed", "project", time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC))
	return c
}

func extSave(t *testing.T, m map[string]any) (int, string) {
	t.Helper()
	body := map[string]any{"name": "fact", "description": "d", "type": "project", "body": "b"}
	for k, v := range m {
		body[k] = v
	}
	code, b := memberDo(t, http.MethodPost, extBase+"entries", body)
	return code, string(b)
}

// Every spelling of one repository's remote reaches the same project, on reads and on writes, and
// the write's answer names the project for the CP's audit.
func TestAgentMemoryExternalRepositorySelectsTheProject(t *testing.T) {
	home, _, _ := agentMemTestEnv(t)
	a := extRepo(t, home, "alpha", "https://github.com/k-k1/alpha.git")
	b := extRepo(t, home, "beta", "git@github.com:k-k1/beta.git")
	externalMode(t, ExternalMemoryWrite)
	for i, repo := range []string{
		"https://github.com/k-k1/alpha", "https://GitHub.com/k-k1/alpha.git/", "git@github.com:k-k1/alpha.git",
		"ssh://git@github.com/k-k1/alpha.git", "https://tok:x@github.com:443/k-k1/alpha",
	} {
		name := fmt.Sprintf("via-remote-%d", i)
		code, out := extSave(t, map[string]any{"scope": "project", "repository": repo, "name": name})
		if code != 200 || !strings.Contains(out, `"project":"`+a.Project.ID+`"`) {
			t.Fatalf("%s: %d %s", repo, code, out)
		}
		if _, err := agentMemRead(a, "project", name); err != nil {
			t.Fatalf("%s: not saved into alpha: %v", repo, err)
		}
		if _, err := agentMemRead(b, "project", name); err == nil {
			t.Fatalf("%s: leaked into beta", repo)
		}
	}
	for _, repo := range []string{"https://github.com/k-k1/alpha", "https://GitHub.com/k-k1/alpha.git/", "git@github.com:k-k1/alpha.git", "ssh://git@github.com/k-k1/alpha.git"} {
		code, out := memberDo(t, http.MethodGet, extBase+"read?scope=project&name=seed&repository="+url.QueryEscape(repo), nil)
		if code != 200 || !strings.Contains(string(out), `"name":"seed"`) {
			t.Errorf("read via %s: %d %s", repo, code, out)
		}
	}
	code, out := memberDo(t, http.MethodGet, extBase+"entries?repository="+url.QueryEscape("git@github.com:k-k1/beta"), nil)
	if code != 200 || !strings.Contains(string(out), "seed") {
		t.Errorf("index via repository: %d %s", code, out)
	}
	code, out = memberDo(t, http.MethodGet, extBase+"search?q=seed&repository="+url.QueryEscape("https://github.com/k-k1/beta"), nil)
	if code != 200 || !strings.Contains(string(out), b.Project.ID) {
		t.Errorf("search via repository: %d %s", code, out)
	}
	// project and repository that agree are fine; forget answers the project too.
	code, out = memberDo(t, http.MethodPost, extBase+"forget", map[string]any{
		"scope": "project", "project": a.Project.ID, "repository": "https://github.com/k-k1/alpha", "name": "seed", "revision": 1})
	if code != 200 || !strings.Contains(string(out), `"project":"`+a.Project.ID+`"`) {
		t.Errorf("forget: %d %s", code, out)
	}
}

// The `remote` that memory_projects lists is accepted back as `repository` (#3098), and only when
// it is a forge host with owner and repo; look-alikes stay refused.
func TestAgentMemoryExternalRepositoryAcceptsListedRemote(t *testing.T) {
	home, _, _ := agentMemTestEnv(t)
	a := extRepo(t, home, "alpha", "git@github.com:k-k1/Alpha.git")
	externalMode(t, ExternalMemoryWrite)
	code, out := memberDo(t, http.MethodGet, extBase+"projects", nil)
	if code != 200 {
		t.Fatalf("projects: %d %s", code, out)
	}
	var list struct {
		Projects []struct{ ID, Remote string }
	}
	if err := json.Unmarshal(out, &list); err != nil {
		t.Fatal(err)
	}
	remote := ""
	for _, p := range list.Projects {
		if p.ID == a.Project.ID {
			remote = p.Remote
		}
	}
	if remote != "github.com/k-k1/Alpha" {
		t.Fatalf("listed remote = %q in %s", remote, out)
	}
	for i, repo := range []string{remote, "GitHub.com/k-k1/Alpha", "github.com/k-k1/Alpha.git"} {
		name := fmt.Sprintf("via-listed-%d", i)
		code, out := extSave(t, map[string]any{"scope": "project", "repository": repo, "name": name})
		if code != 200 || !strings.Contains(out, `"project":"`+a.Project.ID+`"`) {
			t.Fatalf("save via %s: %d %s", repo, code, out)
		}
		code, raw := memberDo(t, http.MethodGet, extBase+"search?q=seed&repository="+url.QueryEscape(repo), nil)
		if code != 200 || !strings.Contains(string(raw), a.Project.ID) {
			t.Errorf("search via %s: %d %s", repo, code, raw)
		}
	}
	for _, repo := range []string{
		"foo/bar", "./k-k1/alpha", "github.com/k-k1", "github.com/.git", "github.com", "github.com/", "github.com/k-k1/",
		"git.example.com/team/repo", "tok:x@github.com/k-k1/alpha",
		"github.com/k-k1/alpha?x=1", "/github.com/k-k1/alpha", "ghe.example.com/k-k1/alpha",
	} {
		if code, out := extSave(t, map[string]any{"scope": "project", "repository": repo, "name": "nope"}); code != 400 {
			t.Errorf("%q accepted: %d %s", repo, code, out)
		}
	}
	// A non-forge host stays refused even when a project lists it.
	extRepo(t, home, "ghe", "https://ghe.example.com/k-k1/alpha.git")
	if code, out := extSave(t, map[string]any{"scope": "project", "repository": "ghe.example.com/k-k1/alpha", "name": "nope"}); code != 400 {
		t.Errorf("self-hosted listed remote accepted: %d %s", code, out)
	}
}

// A remote that matches no project, matches two, names a different project than `project`, or is
// not a remote at all is refused; a self-hosted host is not github.com.
func TestAgentMemoryExternalRepositoryRefusals(t *testing.T) {
	home, _, _ := agentMemTestEnv(t)
	a := extRepo(t, home, "alpha", "https://github.com/k-k1/alpha.git")
	b := extRepo(t, home, "beta", "https://github.com/k-k1/beta.git")
	ghe := extRepo(t, home, "ghe", "https://ghe.example.com:8443/k-k1/alpha.git")
	externalMode(t, ExternalMemoryWrite)

	for _, c := range []struct {
		name string
		m    map[string]any
		code int
		sub  string
	}{
		{"unmatched", map[string]any{"repository": "https://github.com/k-k1/nosuch"}, 404, "no project"},
		{"other host", map[string]any{"repository": "https://gitlab.com/k-k1/alpha"}, 404, "no project"},
		{"conflict", map[string]any{"repository": "https://github.com/k-k1/alpha", "project": b.Project.ID}, 400, "different projects"},
		{"not a remote", map[string]any{"repository": "/home/dev/repos/alpha"}, 400, "not a remote"},
		{"ssh on an unknown host", map[string]any{"repository": "git@ghe.example.com:k-k1/alpha.git"}, 400, "not a remote"},
	} {
		m := map[string]any{"scope": "project"}
		for k, v := range c.m {
			m[k] = v
		}
		if code, out := extSave(t, m); code != c.code || !strings.Contains(out, c.sub) {
			t.Errorf("%s save: %d %s", c.name, code, out)
		}
		q := url.Values{"q": {"seed"}}
		for k, v := range c.m {
			q.Set(k, v.(string))
		}
		if code, out := memberDo(t, http.MethodGet, extBase+"search?"+q.Encode(), nil); code != c.code {
			t.Errorf("%s search: %d %s", c.name, code, out)
		}
	}
	// A self-hosted host is never resolved from a remote (its port is not kept), with or without
	// a port, in either direction; the id still works.
	for _, repo := range []string{
		"https://ghe.example.com/k-k1/alpha", "https://ghe.example.com:8443/k-k1/alpha.git", "https://ghe.example.com:9443/k-k1/alpha.git",
	} {
		if code, out := extSave(t, map[string]any{"scope": "project", "repository": repo, "name": "ghe"}); code != 400 || !strings.Contains(out, "cannot be matched safely") {
			t.Errorf("ghe save %s: %d %s", repo, code, out)
		}
	}
	if _, err := agentMemRead(ghe, "project", "ghe"); err == nil {
		t.Error("a repository on a self-hosted host wrote into a project")
	}
	if code, out := extSave(t, map[string]any{"scope": "project", "project": ghe.Project.ID, "name": "ghe"}); code != 200 {
		t.Errorf("ghe by id: %d %s", code, out)
	}

	// Two projects with one remote are ambiguous, never picked between.
	remoteRepo(t, home, "alpha-copy", "https://github.com/k-k1/alpha.git")
	c2 := agentMemCallerT(t, "claude-alpha-copy")
	agentMemSaveN(t, c2, "seed", "project", time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC))
	if c2.Project.ID == a.Project.ID {
		t.Skip("the two working copies share a project id")
	}
	code, out := extSave(t, map[string]any{"scope": "project", "repository": "https://github.com/k-k1/alpha"})
	if code != http.StatusConflict || !strings.Contains(out, a.Project.ID) || !strings.Contains(out, c2.Project.ID) {
		t.Errorf("ambiguous: %d %s", code, out)
	}
}

// A write must say where it goes: an omitted scope used to default to the user scope.
func TestAgentMemoryExternalWriteRequiresScope(t *testing.T) {
	home, _, _ := agentMemTestEnv(t)
	a := extRepo(t, home, "alpha", "https://github.com/k-k1/alpha.git")
	externalMode(t, ExternalMemoryWrite)
	for _, m := range []map[string]any{{}, {"project": a.Project.ID}, {"repository": "https://github.com/k-k1/alpha"}} {
		code, out := extSave(t, m)
		if code != 400 || !strings.Contains(out, "scope is required") || !strings.Contains(out, "git remote get-url origin") {
			t.Errorf("save %v: %d %s", m, code, out)
		}
		code, fb := memberDo(t, http.MethodPost, extBase+"forget", map[string]any{"name": "seed", "revision": 1, "project": m["project"], "repository": m["repository"]})
		if out = string(fb); code != 400 || !strings.Contains(out, "scope is required") {
			t.Errorf("forget %v: %d %s", m, code, out)
		}
	}
	if code, out := extSave(t, map[string]any{"scope": "project"}); code != 400 || !strings.Contains(out, "needs `repository`") {
		t.Errorf("project scope without a project: %d %s", code, out)
	}
	if code, out := extSave(t, map[string]any{"scope": "user", "type": "user"}); code != 200 || strings.Contains(out, `"project"`) {
		t.Errorf("explicit user scope: %d %s", code, out)
	}
	if _, err := agentMemRead(a, "project", "fact"); err == nil {
		t.Error("a refused save wrote into the project")
	}
}

// An explicit scope that misses names the other readable scope holding the name, and a read of
// the hinted scope still works. No hint when the other scope misses too, when the memory there is
// limited to other kinds, or when the caller has no project.
func TestAgentMemoryReadMissHintsOtherScope(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	c := agentMemCallerT(t, "claude-main")
	now := time.Now()
	if _, err := agentMemSave(c, agentMemSaveReq{Scope: agentMemScopeUser, Name: "in-user", Description: "d", Type: "user", Body: "b"}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := agentMemSave(c, agentMemSaveReq{Scope: agentMemScopeUser, Name: "user-claude-only", Description: "d", Type: "user", Body: "b", Kinds: []string{"claude"}}, now); err != nil {
		t.Fatal(err)
	}
	agentMemSaveN(t, c, "in-project", "project", now)

	_, err := agentMemRead(c, agentMemScopeProject, "in-user")
	want := `no memory by that name in scope project; one exists in scope user (pass scope "user" or omit scope)`
	if agentMemCode(err) != errCodeMemoryNotFound || err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("hint: %v", err)
	}
	if _, err := agentMemRead(c, agentMemScopeUser, "in-project"); err == nil || !strings.Contains(err.Error(), `one exists in scope project`) {
		t.Fatalf("reverse hint: %v", err)
	}
	if _, err := agentMemRead(c, "", "in-user"); err != nil {
		t.Fatalf("omitted scope: %v", err)
	}
	if _, err := agentMemRead(c, agentMemScopeProject, "nowhere"); err == nil || strings.Contains(err.Error(), "exists") {
		t.Fatalf("hint where the other scope misses too: %v", err)
	}

	ext := c
	ext.Kind = agentMemExternalKind
	if _, err := agentMemRead(ext, agentMemScopeProject, "user-claude-only"); err == nil || strings.Contains(err.Error(), "exists") {
		t.Fatalf("hint revealed a kinds-hidden memory: %v", err)
	}

	usesBefore := agentMemLoadUsage("user")["in-user"]
	_, _ = agentMemRead(c, agentMemScopeProject, "in-user")
	if got := agentMemLoadUsage("user")["in-user"]; got != usesBefore {
		t.Fatalf("the probe counted a use: %d -> %d", usesBefore, got)
	}

	noProject := c
	noProject.Project = nil
	if _, err := agentMemRead(noProject, agentMemScopeUser, "in-project"); err == nil || strings.Contains(err.Error(), "exists") {
		t.Fatalf("hint for a scope the caller cannot read: %v", err)
	}
}

// A forget that misses its explicit scope names the other scope, and the revision check is not
// reached or loosened.
func TestAgentMemoryForgetMissHintsOtherScope(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	c := agentMemCallerT(t, "claude-main")
	now := time.Now()
	if _, err := agentMemSave(c, agentMemSaveReq{Scope: agentMemScopeUser, Name: "in-user", Description: "d", Type: "user", Body: "b"}, now); err != nil {
		t.Fatal(err)
	}
	_, err := agentMemForget(c, agentMemForgetReq{Scope: agentMemScopeProject, Name: "in-user", Revision: 1}, now)
	if agentMemCode(err) != errCodeMemoryNotFound || err == nil || !strings.Contains(err.Error(), `in scope project; one exists in scope user (read it there first and forget it with that revision)`) {
		t.Fatalf("forget hint: %v", err)
	}
	if _, err := agentMemForget(c, agentMemForgetReq{Scope: agentMemScopeProject, Name: "nowhere", Revision: 1}, now); err == nil || strings.Contains(err.Error(), "exists") {
		t.Fatalf("forget hint where nothing exists: %v", err)
	}
	// A same-named memory at another revision: the hint must not suggest reusing this revision.
	if _, err := agentMemForget(c, agentMemForgetReq{Scope: agentMemScopeProject, Name: "in-user", Revision: 1}, now); err == nil || strings.Contains(err.Error(), "pass scope") {
		t.Fatalf("hint tells the caller to retry with the same revision: %v", err)
	}
	if res, err := agentMemForget(c, agentMemForgetReq{Scope: agentMemScopeUser, Name: "in-user", Revision: 99}, now); err == nil || agentMemCode(err) != errCodeMemoryConflict {
		t.Fatalf("revision check lost: %+v %v", res, err)
	}
}

// A file the secret scan withheld is not offered by the hint, for a member-kind or an external
// caller; the fixture is checked to be withheld by a direct read.
func TestAgentMemoryMissHintSkipsWithheldFile(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	c := agentMemCallerT(t, "claude-main")
	key := agentMemFakeAWS()
	memoryMkdirAll(t, filepath.Join(agentMemDir(), "user"))
	memoryWrite(t, filepath.Join(agentMemDir(), "user", "manual.md"), "---\nname: manual\ndescription: key "+key+"\n---\nbody\n")
	if _, err := agentMemRead(c, agentMemScopeUser, "manual"); agentMemCode(err) != errCodeMemorySecretDetected {
		t.Fatalf("fixture is not withheld: %v", err)
	}
	ext := c
	ext.Kind = agentMemExternalKind
	for _, cc := range []agentMemCaller{c, ext} {
		_, err := agentMemRead(cc, agentMemScopeProject, "manual")
		if agentMemCode(err) != errCodeMemoryNotFound || strings.Contains(err.Error(), "exists") {
			t.Fatalf("read hint (%s): %v", cc.Kind, err)
		}
		_, err = agentMemForget(cc, agentMemForgetReq{Scope: agentMemScopeProject, Name: "manual", Revision: 1}, time.Now())
		if agentMemCode(err) != errCodeMemoryNotFound || strings.Contains(err.Error(), "exists") {
			t.Fatalf("forget hint (%s): %v", cc.Kind, err)
		}
	}
}
