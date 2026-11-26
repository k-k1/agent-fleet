package sessionx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// gitRun runs git for a test, with the same env as gitInit.
func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

// TestHandoffContextBlocksBranchNeverPushed is the one that matters: a branch with no
// upstream reports ahead 0 (the `# branch.ab` line is absent entirely), so a gate that looks
// only at ahead>0 lets a never-pushed branch through - exactly the case a handoff hits most
// often.
func TestHandoffContextBlocksBranchNeverPushed(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	gitInit(t, dir)
	gitRun(t, dir, "checkout", "-q", "-b", "temp/work")

	c := buildHandoffContext(dir)
	if c.Vcs != "git" {
		t.Fatalf("vcs = %q, want git", c.Vcs)
	}
	if c.Ahead != 0 {
		t.Fatalf("ahead = %d, want 0 (no upstream means git reports nothing)", c.Ahead)
	}
	if !c.NoUpstream {
		t.Fatal("noUpstream = false; a branch with no upstream must be detected")
	}
	if c.Blocked != handoffBlockNoUpstream {
		t.Fatalf("blocked = %q, want %q", c.Blocked, handoffBlockNoUpstream)
	}
}

func TestHandoffContextUnpushedAndClean(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	if out, err := exec.Command("git", "init", "--bare", "-b", "main", origin).CombinedOutput(); err != nil {
		t.Fatalf("init bare: %v: %s", err, out)
	}
	work := filepath.Join(root, "work")
	gitInit(t, work)
	gitRun(t, work, "remote", "add", "origin", origin)
	gitRun(t, work, "push", "-q", "-u", "origin", "main")

	// Pushed and clean: let it through.
	c := buildHandoffContext(work)
	if c.Blocked != "" {
		t.Fatalf("blocked = %q, want empty for a pushed clean branch", c.Blocked)
	}
	if c.Warning != "" {
		t.Fatalf("warning = %q, want empty", c.Warning)
	}
	if c.Branch != "main" || c.HeadSha == "" || c.Remote == "" {
		t.Fatalf("coordinates incomplete: %+v", c)
	}

	// Uncommitted work only warns, never blocks: some handoffs discard it on purpose.
	if err := os.WriteFile(filepath.Join(work, "f"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	if c = buildHandoffContext(work); c.Blocked != "" || c.Warning != handoffWarnDirty {
		t.Fatalf("dirty: blocked=%q warning=%q, want blocked empty and warning %q", c.Blocked, c.Warning, handoffWarnDirty)
	}

	// Unpushed commits block.
	gitRun(t, work, "commit", "-qam", "local only")
	if c = buildHandoffContext(work); c.Blocked != handoffBlockUnpushed {
		t.Fatalf("blocked = %q, want %q (ahead=%d)", c.Blocked, handoffBlockUnpushed, c.Ahead)
	}
}

func TestHandoffContextDetachedIsBlocked(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	gitInit(t, dir)
	gitRun(t, dir, "checkout", "-q", "--detach")
	if c := buildHandoffContext(dir); c.Blocked != handoffBlockDetached {
		t.Fatalf("blocked = %q, want %q", c.Blocked, handoffBlockDetached)
	}
}

func TestHandoffContextNonRepoIsNotGated(t *testing.T) {
	dir := t.TempDir()
	c := buildHandoffContext(dir)
	if c.Vcs != "" {
		t.Fatalf("vcs = %q, want empty for a plain folder", c.Vcs)
	}
	if c.Blocked != "" {
		t.Fatalf("blocked = %q; a non-git working copy has no push concept to gate on", c.Blocked)
	}
}

// TestSanitizeRemoteURL: the remote carried in an offer must lose its credentials. Passed on
// to another member intact, a handoff becomes a token handover.
func TestSanitizeRemoteURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://github.com/k-k1/agent-fleet.git", "https://github.com/k-k1/agent-fleet.git"},
		{"https://x-access-token:ghp_secret@github.com/k-k1/af.git", "https://github.com/k-k1/af.git"},
		{"git@github.com:k-k1/af.git", "https://github.com/k-k1/af.git"},
		{"ssh://git@example.com:2222/k-k1/af.git", "https://example.com/k-k1/af.git"},
		{"/srv/local/repo.git", "/srv/local/repo.git"},
	}
	for _, c := range cases {
		if got := sanitizeRemoteURL(c.in); got != c.want {
			t.Errorf("sanitizeRemoteURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	for _, in := range []string{"https://user:tok@host/x.git", "user:tok@weird"} {
		if got := sanitizeRemoteURL(in); containsCredential(got) {
			t.Errorf("sanitizeRemoteURL(%q) = %q still carries a credential", in, got)
		}
	}
}

func containsCredential(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '@' {
			return true
		}
	}
	return false
}

// A session in a space is handed over with the space warning, whether or not the folder is
// also a git working copy: its files and attachments stay in the giver's home either way.
func TestHandoffContextForSpaceWarns(t *testing.T) {
	isolateAgentState(t)
	dir := placeSpace(t, "notes")

	// The stored place decides.
	c := handoffContextFor(session.Meta{Dir: dir, Place: &session.Place{Kind: session.PlaceSpace, ID: "notes"}})
	if !c.Space || c.Warning != handoffWarnSpace || c.Blocked != "" {
		t.Fatalf("stored place: space=%v warning=%q blocked=%q, want the space warning and no block", c.Space, c.Warning, c.Blocked)
	}
	// A meta from before the place field falls back to the folder's marker.
	if c = handoffContextFor(session.Meta{Dir: dir}); !c.Space || c.Warning != handoffWarnSpace {
		t.Fatalf("no stored place: space=%v warning=%q, want the space warning", c.Space, c.Warning)
	}
	// A repository gets neither.
	repo := filepath.Join(homeDir(), "repos", "app")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if c = handoffContextFor(session.Meta{Dir: repo, Place: &session.Place{Kind: session.PlaceRepo, ID: "app"}}); c.Space || c.Warning != "" {
		t.Fatalf("repository: space=%v warning=%q, want neither", c.Space, c.Warning)
	}
}

// A space that is also a git working copy still gates on the push, and its warning replaces
// the dirty one: one acknowledgement, and it already says that only what git publishes travels.
func TestHandoffContextForSpaceKeepsGitGate(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	isolateAgentState(t)
	dir := placeSpace(t, "gitspace")
	gitInit(t, dir)
	gitRun(t, dir, "checkout", "-q", "-b", "temp/work")
	if err := os.WriteFile(filepath.Join(dir, "scratch.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := handoffContextFor(session.Meta{Dir: dir, Place: &session.Place{Kind: session.PlaceSpace, ID: "gitspace"}})
	if c.Blocked != handoffBlockNoUpstream {
		t.Fatalf("blocked = %q, want %q (a space's git gate still applies)", c.Blocked, handoffBlockNoUpstream)
	}
	if !c.Dirty || c.Warning != handoffWarnSpace {
		t.Fatalf("dirty=%v warning=%q, want dirty with the space warning", c.Dirty, c.Warning)
	}
}

// The GET handler decides by the place stored in the session's own record, not by looking at the
// folder again: a record that says space wins over a folder with no marker, and a record that
// says repository wins over a folder that carries one (a space and a repository can share a
// name, and the place was fixed at create).
func TestHandoffContextHandlerTrustsTheStoredPlace(t *testing.T) {
	isolateAgentState(t)
	marked := placeSpace(t, "marked")
	unmarked := filepath.Join(homeDir(), "spaces", "unmarked")
	if err := os.MkdirAll(unmarked, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		dir   string
		place *session.Place
		want  bool
	}{
		{"hs-stored-space", unmarked, &session.Place{Kind: session.PlaceSpace, ID: "unmarked"}, true},
		{"hs-stored-repo", marked, &session.Place{Kind: session.PlaceRepo, ID: "marked"}, false},
		{"hs-no-place", marked, nil, true},
	} {
		session.WriteMeta(session.Meta{Name: tc.name, Dir: tc.dir, Place: tc.place})
		r := httptest.NewRequest(http.MethodGet, "/sessions/"+tc.name+"/handoff-context", nil)
		r.SetPathValue("name", tc.name)
		w := httptest.NewRecorder()
		HandleSessionHandoffContext(w, r)
		var c handoffContext
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &c) != nil {
			t.Fatalf("%s: %d %s", tc.name, w.Code, w.Body.String())
		}
		if c.Space != tc.want || (c.Warning == handoffWarnSpace) != tc.want {
			t.Fatalf("%s: space=%v warning=%q, want space=%v", tc.name, c.Space, c.Warning, tc.want)
		}
	}
}
