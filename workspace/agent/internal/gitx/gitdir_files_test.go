package gitx

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// revParseDirInfo is the reference: the exact rev-parse GitDirInfo used to run.
func revParseDirInfo(dir string) (string, bool) {
	out, err := Run(dir, "rev-parse", "--abbrev-ref", "HEAD", "--absolute-git-dir")
	if err != nil {
		return "", false
	}
	lines := strings.Split(out, "\n")
	b := strings.TrimSpace(lines[0])
	if b == "HEAD" {
		b = "(detached)"
	}
	wt := len(lines) >= 2 && strings.Contains(filepath.ToSlash(strings.TrimSpace(lines[1])), "/.git/worktrees/")
	return b, wt
}

func TestGitDirInfoFromFilesMatchesRevParse(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	main := filepath.Join(root, "main")
	gitAt(t, root, "init", "-q", "-b", "main", main)
	gitAt(t, main, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "i")
	gitAt(t, main, "worktree", "add", "-q", "-b", "feat/deep/name", filepath.Join(root, "wt"))
	gitAt(t, main, "worktree", "add", "-q", "--detach", filepath.Join(root, "wt-det"))
	gitAt(t, main, "branch", "packed/one")
	gitAt(t, main, "pack-refs", "--all")
	gitAt(t, main, "worktree", "add", "-q", filepath.Join(root, "wt-packed"), "packed/one")
	gitAt(t, main, "branch", "amb")
	gitAt(t, main, "tag", "amb") // `amb` is now ambiguous: git abbreviates to heads/amb
	gitAt(t, main, "worktree", "add", "-q", filepath.Join(root, "wt-amb"), "amb")
	unborn := filepath.Join(root, "unborn")
	gitAt(t, root, "init", "-q", "-b", "fresh", unborn)
	bare := filepath.Join(root, "bare.git")
	gitAt(t, root, "init", "-q", "--bare", bare)
	detMain := filepath.Join(root, "det-main")
	gitAt(t, root, "clone", "-q", main, detMain)
	gitAt(t, detMain, "checkout", "-q", "--detach")
	if err := os.MkdirAll(filepath.Join(main, "sub", "deeper"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "plain"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	wtlink := filepath.Join(root, "wtlink")
	if err := os.Symlink(main, link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "wt"), wtlink); err != nil {
		t.Fatal(err)
	}

	// Review round 1: symlink into another repo's subdir / into no repo, and repos
	// whose files disagree with what git accepts.
	other := filepath.Join(root, "other")
	gitAt(t, root, "init", "-q", "-b", "target-branch", other)
	gitAt(t, other, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "i")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(other, "sub"), 0o755))
	must(os.Symlink(filepath.Join(other, "sub"), filepath.Join(main, "alias-into-repo")))
	must(os.MkdirAll(filepath.Join(root, "norepo", "d"), 0o755))
	must(os.Symlink(filepath.Join(root, "norepo", "d"), filepath.Join(main, "alias-out")))
	clone := func(name string) string {
		d := filepath.Join(root, name)
		gitAt(t, root, "clone", "-q", main, d)
		return d
	}
	symref := clone("symref") // refs/heads/main is a symbolic ref to another branch
	gitAt(t, symref, "branch", "elsewhere")
	must(os.WriteFile(filepath.Join(symref, ".git", "refs", "heads", "main"), []byte("ref: refs/heads/elsewhere\n"), 0o644))
	broken := clone("broken") // loose ref with garbage content
	must(os.WriteFile(filepath.Join(broken, ".git", "refs", "heads", "main"), []byte("garbage\n"), 0o644))
	noobj := clone("noobjects")
	must(os.Rename(filepath.Join(noobj, ".git", "objects"), filepath.Join(noobj, ".git", "objects.away")))
	spaced := clone("spaced") // leading blank in HEAD
	must(os.WriteFile(filepath.Join(spaced, ".git", "HEAD"), []byte(" ref: refs/heads/main\n"), 0o644))
	badpacked := clone("badpacked")
	gitAt(t, badpacked, "pack-refs", "--all")
	must(os.WriteFile(filepath.Join(badpacked, ".git", "packed-refs"), []byte("zzzz refs/heads/main\n"), 0o644))

	cases := map[string]string{
		"symlink into other repo subdir": filepath.Join(main, "alias-into-repo"),
		"symlink out of any repo":        filepath.Join(main, "alias-out"),
		"symbolic loose branch ref":      symref,
		"garbage loose branch ref":       broken,
		"objects dir missing":            noobj,
		"HEAD with leading blank":        spaced,
		"malformed packed-refs":          badpacked,
		"main checkout":                  main,
		"slash branch worktree":          filepath.Join(root, "wt"),
		"detached worktree":              filepath.Join(root, "wt-det"),
		"detached clone":                 detMain,
		"packed-refs branch":             filepath.Join(root, "wt-packed"),
		"ambiguous with tag":             filepath.Join(root, "wt-amb"),
		"subdir":                         filepath.Join(main, "sub"),
		"nested subdir":                  filepath.Join(main, "sub", "deeper"),
		"unborn branch":                  unborn,
		"bare repo":                      bare,
		"non-repo":                       filepath.Join(root, "plain"),
		"missing dir":                    filepath.Join(root, "nope"),
		"symlinked main":                 link,
		"symlinked worktree":             wtlink,
		"symlinked main subdir":          filepath.Join(link, "sub"),
		"inside .git of main":            filepath.Join(main, ".git"),
		"inside worktree gitdir":         filepath.Join(main, ".git", "worktrees", "wt"),
	}
	for name, dir := range cases {
		t.Run(name, func(t *testing.T) {
			wantB, wantWT := revParseDirInfo(dir)
			if b, wt, ok := gitDirInfoFromFiles(dir); ok && (b != wantB || wt != wantWT) {
				t.Fatalf("files = (%q,%v), rev-parse = (%q,%v)", b, wt, wantB, wantWT)
			}
			if b, wt := GitDirInfo(dir); b != wantB || wt != wantWT {
				t.Fatalf("GitDirInfo = (%q,%v), rev-parse = (%q,%v)", b, wt, wantB, wantWT)
			}
		})
	}

	// The common cases must be answered without a process, or the fix is a no-op.
	for _, name := range []string{"main checkout", "slash branch worktree", "detached worktree", "detached clone",
		"packed-refs branch", "subdir", "nested subdir", "unborn branch", "non-repo", "missing dir", "symlinked worktree"} {
		before := SpawnCount()
		GitDirInfo(cases[name])
		if n := SpawnCount() - before; n != 0 {
			t.Errorf("%s: GitDirInfo spawned %d git processes, want 0", name, n)
		}
	}
	// A case files cannot settle (ambiguous short name) still gets git's answer.
	if b, _ := GitDirInfo(cases["ambiguous with tag"]); b != "heads/amb" {
		t.Errorf("ambiguous: got %q, want heads/amb", b)
	}
}

func TestGitDirInfoFallsBackUnderGitEnv(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	gitAt(t, dir, "init", "-q", "-b", "main")
	t.Setenv("GIT_DIR", filepath.Join(dir, ".git"))
	before := SpawnCount()
	GitDirInfo(dir)
	if SpawnCount()-before != 1 {
		t.Fatal("GIT_DIR set: want the rev-parse fallback")
	}
}
