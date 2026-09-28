package branchrule

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// isolateGit keeps the developer's global and system git config out of the test.
func isolateGit(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@example.invalid")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@example.invalid")
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// newRepo is a clone-like repository on main with one commit and origin pointing at a
// Bitbucket-looking URL. The remote-tracking refs are written by hand: nothing is fetched.
func newRepo(t *testing.T) string {
	t.Helper()
	isolateGit(t)
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	git(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
	git(t, dir, "remote", "add", "origin", "git@bitbucket.org:acme/web.git")
	return dir
}

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commitFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	writeFile(t, dir, rel, content)
	git(t, dir, "add", "--", rel)
	git(t, dir, "commit", "-q", "-m", "add "+rel)
}

// remoteBranch creates refs/remotes/origin/<name> at HEAD.
func remoteBranch(t *testing.T, dir, name string) {
	t.Helper()
	git(t, dir, "update-ref", "refs/remotes/origin/"+name, "HEAD")
}

func layersFor(repo Repo, user ...Rule) []Layer {
	return []Layer{repo.Layer, UserLayer(user, ""), Builtin()}
}

func hasWarning(ws []Warning, code, substr string) bool {
	for _, w := range ws {
		if w.Code == code && strings.Contains(w.Message, substr) {
			return true
		}
	}
	return false
}
