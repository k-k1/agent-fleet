package gitx

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// twoRemoteClone builds a clone whose second remote ("aaa", sorting before origin)
// also has main and topic/x, with no local main — the shape behind #2268. aaa/main
// is made older than origin/main so the picker showing the wrong one is visible.
func twoRemoteClone(t *testing.T) (dir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	seed := filepath.Join(home, "seed")
	gitInit(t, seed) // main + feature
	gitAt(t, seed, "branch", "topic/x")
	for _, r := range []string{"origin", "aaa"} {
		gitAt(t, home, "clone", "--bare", seed, r+".git")
	}
	// origin/main gets a newer commit than aaa/main.
	gitAt(t, seed, "commit", "--allow-empty", "-m", "newer on origin")
	gitAt(t, seed, "push", filepath.Join(home, "origin.git"), "main")

	dir = filepath.Join(home, "repos", "app")
	gitAt(t, home, "clone", filepath.Join(home, "origin.git"), dir)
	gitAt(t, dir, "remote", "add", "aaa", filepath.Join(home, "aaa.git"))
	gitAt(t, dir, "fetch", "aaa")
	gitAt(t, dir, "remote", "set-head", "origin", "main")
	gitAt(t, dir, "checkout", "-b", "work")
	gitAt(t, dir, "branch", "-D", "main")
	return dir
}

func TestBranchInfosTwoRemotes(t *testing.T) {
	dir := twoRemoteClone(t)
	byName := map[string]BranchInfo{}
	for _, b := range gitBranchInfos(dir, "work") {
		byName[b.Name] = b
	}
	if _, ok := byName["origin"]; ok || byName["HEAD"].Name != "" {
		t.Errorf("refs/remotes/origin/HEAD leaked into the list as origin or HEAD")
	}
	for _, n := range []string{"main", "topic/x"} {
		b, ok := byName[n]
		if !ok || !b.Remote || b.RemoteName != "origin" {
			t.Errorf("%s = %+v, want remote-only entry from origin", n, b)
		}
	}
	if b := byName["main"]; b.Subject != "newer on origin" {
		t.Errorf("main shows subject %q, want origin's tip", b.Subject)
	}
}

// postCheckout drives HandleRepoCheckout the way the Console does.
func postCheckout(t *testing.T, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	old := deps.LiveSessionsInDir
	deps.LiveSessionsInDir = func(string) []string { return nil }
	t.Cleanup(func() { deps.LiveSessionsInDir = old })
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/repos/app/checkout", bytes.NewReader(raw))
	req.SetPathValue("name", "app")
	rec := httptest.NewRecorder()
	HandleRepoCheckout(rec, req)
	return rec
}

// addSlashRemote adds a remote whose name contains a slash, holding only-up and
// up/deep (plus `both`, also pushed to aaa), with its symbolic HEAD set.
func addSlashRemote(t *testing.T, dir string) {
	t.Helper()
	home := filepath.Dir(filepath.Dir(dir))
	seed := filepath.Join(home, "seed")
	gitAt(t, seed, "branch", "only-up")
	gitAt(t, seed, "branch", "up/deep")
	gitAt(t, seed, "branch", "both")
	gitAt(t, home, "clone", "--bare", seed, "up.git")
	gitAt(t, seed, "push", filepath.Join(home, "aaa.git"), "both")
	gitAt(t, dir, "remote", "add", "team/upstream", filepath.Join(home, "up.git"))
	gitAt(t, dir, "fetch", "--all")
	gitAt(t, dir, "remote", "set-head", "team/upstream", "main")
}

func TestSlashRemoteName(t *testing.T) {
	dir := twoRemoteClone(t)
	addSlashRemote(t, dir)
	gitAt(t, dir, "config", "branch.work.remote", "team/upstream")

	byName := map[string]BranchInfo{}
	for _, b := range gitBranchInfos(dir, "work") {
		byName[b.Name] = b
	}
	for _, n := range []string{"upstream/HEAD", "upstream/main", "HEAD", "origin"} {
		if _, ok := byName[n]; ok {
			t.Errorf("%q leaked into the list: a slash remote was split at its first slash", n)
		}
	}
	for n, want := range map[string]string{"only-up": "team/upstream", "up/deep": "team/upstream", "main": "origin"} {
		if got := byName[n]; got.RemoteName != want {
			t.Errorf("%s = %+v, want remote %s", n, got, want)
		}
	}
	// aaa sorts before team/upstream, but the current branch tracks team/upstream.
	if got := byName["both"].RemoteName; got != "team/upstream" {
		t.Errorf("both from %q, want the current branch's upstream remote", got)
	}

	for _, c := range []struct{ branch, remote string }{{"only-up", "team/upstream"}, {"up/deep", "team/upstream"}, {"both", ""}} {
		if rec := postCheckout(t, map[string]any{"branch": c.branch, "remote": c.remote}); rec.Code != http.StatusOK {
			t.Fatalf("checkout %s = %d: %s", c.branch, rec.Code, rec.Body)
		}
		if got := GitCurrentBranch(dir); got != c.branch {
			t.Fatalf("branch = %q, want %q (slash remote must not rename it)", got, c.branch)
		}
		up := strings.TrimSpace(gitAt(t, dir, "rev-parse", "--abbrev-ref", c.branch+"@{upstream}"))
		if want := "team/upstream/" + c.branch; up != want {
			t.Errorf("upstream of %s = %q, want %q", c.branch, up, want)
		}
	}

	// The graph splits the same way.
	remotes := remoteNames(dir)
	if r, b, ok := splitRemoteRef(remotes, "team/upstream/up/deep"); !ok || r != "team/upstream" || b != "up/deep" {
		t.Errorf("splitRemoteRef = %q %q %v", r, b, ok)
	}
	if _, _, ok := splitRemoteRef(remotes, "gone/main"); ok {
		t.Error("a ref under an unconfigured remote must not be attributed to one")
	}
}

// A free-form ref keeps plain `git checkout` semantics: only a picked branch gets the
// remote-tracking treatment.
func TestCheckoutRefKeepsTagSemantics(t *testing.T) {
	dir := twoRemoteClone(t)
	initSha := strings.TrimSpace(gitAt(t, dir, "rev-parse", "aaa/main"))
	gitAt(t, dir, "tag", "main", initSha)
	if rec := postCheckout(t, map[string]any{"ref": "main"}); rec.Code != http.StatusOK {
		t.Fatalf("checkout = %d: %s", rec.Code, rec.Body)
	}
	if got := GitCurrentBranch(dir); got == "main" {
		t.Fatal("ref=main created a local branch instead of checking out the tag")
	}
	if head := strings.TrimSpace(gitAt(t, dir, "rev-parse", "HEAD")); head != initSha {
		t.Errorf("HEAD = %s, want the tag's commit %s", head, initSha)
	}
}

// `origin/main` as a short name is ambiguous once a local branch is called that; the
// checkout passes the full ref.
func TestCheckoutAmbiguousShortName(t *testing.T) {
	dir := twoRemoteClone(t)
	gitAt(t, dir, "branch", "origin/main", "work")
	if rec := postCheckout(t, map[string]any{"branch": "main", "remote": "origin"}); rec.Code != http.StatusOK {
		t.Fatalf("checkout = %d: %s", rec.Code, rec.Body)
	}
	want := strings.TrimSpace(gitAt(t, dir, "rev-parse", "refs/remotes/origin/main"))
	if head := strings.TrimSpace(gitAt(t, dir, "rev-parse", "HEAD")); head != want {
		t.Errorf("HEAD = %s, want origin/main %s", head, want)
	}
}

func TestCheckoutTwoRemotes(t *testing.T) {
	for _, c := range []struct{ name, branch, remote, wantRemote string }{
		{"bare name (old client)", "main", "", "origin"},
		{"explicit origin", "main", "origin", "origin"},
		{"explicit other remote", "main", "aaa", "aaa"},
		{"slash in branch", "topic/x", "origin", "origin"},
		{"unknown remote ignored", "main", "nope", "origin"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := twoRemoteClone(t)
			old := deps.LiveSessionsInDir
			deps.LiveSessionsInDir = func(string) []string { return nil }
			defer func() { deps.LiveSessionsInDir = old }()

			body, _ := json.Marshal(map[string]any{"branch": c.branch, "remote": c.remote})
			req := httptest.NewRequest("POST", "/repos/app/checkout", bytes.NewReader(body))
			req.SetPathValue("name", "app")
			rec := httptest.NewRecorder()
			HandleRepoCheckout(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("checkout = %d: %s", rec.Code, rec.Body)
			}
			if got := GitCurrentBranch(dir); got != c.branch {
				t.Fatalf("branch = %q, want %q", got, c.branch)
			}
			up := strings.TrimSpace(gitAt(t, dir, "rev-parse", "--abbrev-ref", c.branch+"@{upstream}"))
			if want := c.wantRemote + "/" + c.branch; up != want {
				t.Errorf("upstream = %q, want %q", up, want)
			}
		})
	}
}
