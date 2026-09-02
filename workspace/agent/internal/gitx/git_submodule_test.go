package gitx

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestSSHToHTTPS covers the submodule URL rewrite (CodeLeaf parity), including the
// real bitbucket SSH submodules that broke clone before the fix.
func TestSSHToHTTPS(t *testing.T) {
	cases := []struct{ in, want string }{
		// scp-like form (the failing lib-usr submodules)
		{"git@bitbucket.org:example-org/lib-bundle.git", "https://bitbucket.org/example-org/lib-bundle.git"},
		{"git@github.com:owner/repo.git", "https://github.com/owner/repo.git"},
		{"git@github.com:owner/repo", "https://github.com/owner/repo"},
		// ssh:// form, optional user and port
		{"ssh://git@github.com/owner/repo.git", "https://github.com/owner/repo.git"},
		{"ssh://git@bitbucket.org:22/example-org/lib-core.git", "https://bitbucket.org/example-org/lib-core.git"},
		{"ssh://github.com/owner/repo", "https://github.com/owner/repo"},
		// self-hosted host (host-agnostic)
		{"git@git.example.com:team/lib.git", "https://git.example.com/team/lib.git"},
		// already HTTPS / other schemes pass through unchanged
		{"https://github.com/owner/repo.git", "https://github.com/owner/repo.git"},
		{"http://example.com/x.git", "http://example.com/x.git"},
		{"../relative/submodule", "../relative/submodule"},
	}
	for _, c := range cases {
		if got := SSHToHTTPS(c.in); got != c.want {
			t.Errorf("sshToHTTPS(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestSSHURLHost pins the host extraction that submoduleInsteadOfArgs builds its rewrite
// rules from — SSH forms yield a host; HTTPS/local do not.
func TestSSHURLHost(t *testing.T) {
	cases := []struct {
		in   string
		host string
		ok   bool
	}{
		{"git@bitbucket.org:example-org/lib-core.git", "bitbucket.org", true},
		{"ssh://git@github.com:22/owner/repo.git", "github.com", true},
		{"git@git.example.com:team/lib.git", "git.example.com", true},
		{"https://bitbucket.org/example-org/lib-core.git", "", false},
		{"../relative/submodule", "", false},
	}
	for _, c := range cases {
		host, ok := sshURLHost(c.in)
		if ok != c.ok || host != c.host {
			t.Errorf("sshURLHost(%q) = (%q, %v), want (%q, %v)", c.in, host, ok, c.host, c.ok)
		}
	}
}

// TestSubmoduleInsteadOfArgs verifies the recursive-rewrite flags: one deduped pair of
// url.insteadOf rules per SSH host (covering scp + ssh:// spellings), and nothing for a
// repo whose submodules are already HTTPS. This is what lets a NESTED SSH submodule
// (lib-svc/lib-core) clone over HTTPS instead of failing on publickey.
func TestSubmoduleInsteadOfArgs(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	mustRun(t, dir, "git", "init", "-q")
	// Two submodules on the SAME host (must dedupe to one rule pair) + one already-HTTPS.
	gitmodules := "" +
		"[submodule \"a\"]\n\tpath = a\n\turl = git@bitbucket.org:example-org/lib-core.git\n" +
		"[submodule \"b\"]\n\tpath = b\n\turl = ssh://git@bitbucket.org/example-org/lib-svc.git\n" +
		"[submodule \"c\"]\n\tpath = c\n\turl = https://bitbucket.org/example-org/lib-docs.git\n"
	if err := os.WriteFile(filepath.Join(dir, ".gitmodules"), []byte(gitmodules), 0o644); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(submoduleInsteadOfArgs(dir), " ")
	want := "-c url.https://bitbucket.org/.insteadOf=git@bitbucket.org: " +
		"-c url.https://bitbucket.org/.insteadOf=ssh://git@bitbucket.org/"
	if got != want {
		t.Errorf("submoduleInsteadOfArgs =\n  %q\nwant\n  %q", got, want)
	}

	// A repo with only HTTPS submodules needs no rewrite flags.
	dir2 := t.TempDir()
	mustRun(t, dir2, "git", "init", "-q")
	if err := os.WriteFile(filepath.Join(dir2, ".gitmodules"),
		[]byte("[submodule \"x\"]\n\tpath = x\n\turl = https://github.com/o/r.git\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if args := submoduleInsteadOfArgs(dir2); len(args) != 0 {
		t.Errorf("expected no flags for HTTPS-only submodules, got %v", args)
	}
}

func mustRun(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %s: %v: %s", name, strings.Join(args, " "), err, out)
	}
}
