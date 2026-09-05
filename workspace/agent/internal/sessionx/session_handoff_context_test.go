package sessionx

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
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
