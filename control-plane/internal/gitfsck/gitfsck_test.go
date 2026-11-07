package gitfsck

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/datalayout"
)

func fsckGit(t *testing.T, dir string, stdin string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func fsckMust(t *testing.T, dir, stdin string, args ...string) string {
	t.Helper()
	out, err := fsckGit(t, dir, stdin, args...)
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return out
}

func newFsckBare(t *testing.T, configure bool) string {
	t.Helper()
	bare := filepath.Join(t.TempDir(), "r.git")
	fsckMust(t, ".", "", "init", "--bare", "--initial-branch=main", bare)
	if configure {
		if err := Ensure(context.Background(), bare); err != nil {
			t.Fatal(err)
		}
	}
	return bare
}

// workWithGitmodules returns a work tree whose HEAD commit carries .gitmodules.
func workWithGitmodules(t *testing.T, modules string) string {
	t.Helper()
	w := t.TempDir()
	fsckMust(t, w, "", "init", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(w, ".gitmodules"), []byte(modules), 0o644); err != nil {
		t.Fatal(err)
	}
	fsckMust(t, w, "", "add", ".gitmodules")
	fsckMust(t, w, "", "commit", "-m", "c")
	return w
}

func TestInternalGitFsckRejectsMalformedGitmodules(t *testing.T) {
	bad := "[submodule \"../evil\"]\n\tpath = x\n\turl = https://example.com/x\n"
	w := workWithGitmodules(t, bad)

	if out, err := fsckGit(t, w, "", "push", newFsckBare(t, false), "main"); err != nil {
		t.Fatalf("control: unconfigured bare should accept the push: %v\n%s", err, out)
	}
	bare := newFsckBare(t, true)
	out, err := fsckGit(t, w, "", "push", bare, "main")
	if err == nil {
		t.Fatalf("push with a malformed .gitmodules name was accepted:\n%s", out)
	}
	if !strings.Contains(out, "gitmodulesName") {
		t.Errorf("rejection should name gitmodulesName, got:\n%s", out)
	}
}

func TestInternalGitFsckAcceptsOrdinaryAndLegacyPush(t *testing.T) {
	bare := newFsckBare(t, true)

	ok := workWithGitmodules(t, "[submodule \"lib\"]\n\tpath = lib\n\turl = https://example.com/lib\n")
	if out, err := fsckGit(t, ok, "", "push", bare, "main"); err != nil {
		t.Fatalf("ordinary push rejected: %v\n%s", err, out)
	}

	// A commit with a bad timezone: fsck's default severity is error, so it is
	// rejected without the downgrade and accepted with it.
	tree := fsckMust(t, ok, "", "rev-parse", "HEAD^{tree}")
	raw := "tree " + tree + "\nauthor t <t@example.com> 1700000000 +051800\ncommitter t <t@example.com> 1700000000 +051800\n\nlegacy\n"
	legacy := fsckMust(t, ok, raw, "hash-object", "-t", "commit", "-w", "--stdin", "--literally")

	strict := newFsckBare(t, false)
	fsckMust(t, strict, "", "config", "receive.fsckObjects", "true")
	if out, err := fsckGit(t, ok, "", "push", strict, legacy+":refs/heads/legacy"); err == nil {
		t.Fatalf("control: bad timezone should be rejected under plain receive.fsckObjects:\n%s", out)
	}
	if out, err := fsckGit(t, ok, "", "push", bare, legacy+":refs/heads/legacy"); err != nil {
		t.Fatalf("legacy timezone should only warn: %v\n%s", err, out)
	}
}

func TestEnsureInternalGitFsckKeepsOperatorOverride(t *testing.T) {
	bare := newFsckBare(t, false)
	fsckMust(t, bare, "", "config", "receive.fsckObjects", "false")
	fsckMust(t, bare, "", "config", "receive.fsck.badTimezone", "ignore")
	for i := 0; i < 2; i++ { // second run proves idempotence
		if err := Ensure(context.Background(), bare); err != nil {
			t.Fatal(err)
		}
	}
	if got := fsckMust(t, bare, "", "config", "receive.fsckObjects"); got != "false" {
		t.Errorf("operator override clobbered: %q", got)
	}
	if got := fsckMust(t, bare, "", "config", "receive.fsck.badTimezone"); got != "ignore" {
		t.Errorf("operator override clobbered: %q", got)
	}
	if got := fsckMust(t, bare, "", "config", "receive.fsck.badDate"); got != "warn" {
		t.Errorf("missing key not filled: %q", got)
	}
	if got := fsckMust(t, bare, "", "config", "--get-all", "receive.fsck.badDate"); strings.Count(got, "\n") != 0 {
		t.Errorf("duplicate entries: %q", got)
	}
}

func TestBackfillSkipsBrokenRepo(t *testing.T) {
	root := t.TempDir()
	gitRoot := filepath.Join(root, datalayout.GitDir, "t1")
	if err := os.MkdirAll(gitRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	fsckMust(t, ".", "", "init", "--bare", filepath.Join(gitRoot, "good.git"))
	// A directory that looks like a repo but is not one.
	if err := os.MkdirAll(filepath.Join(gitRoot, "broken.git"), 0o700); err != nil {
		t.Fatal(err)
	}
	ok, failed := Backfill(context.Background(), root)
	if ok != 1 || failed != 1 {
		t.Fatalf("ok=%d failed=%d, want 1/1", ok, failed)
	}
	if got := fsckMust(t, filepath.Join(gitRoot, "good.git"), "", "config", "receive.fsckObjects"); got != "true" {
		t.Errorf("good repo not configured: %q", got)
	}
}
