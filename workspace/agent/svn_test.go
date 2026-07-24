package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

func TestDeriveSvnName(t *testing.T) {
	cases := map[string]string{
		"https://host/svn/myrepo/trunk":        "myrepo", // bare trunk → repo name
		"https://host/svn/myrepo/trunk/":       "myrepo",
		"https://host/svn/myrepo/branches/x":   "x",
		"https://host/svn/myrepo":              "myrepo",
		"https://host/svn/myrepo/tags/rel-1.0": "rel-1.0",
		"https://host/a/b/c":                   "c",
		"":                                     "",
	}
	for in, want := range cases {
		if got := deriveSvnName(in); got != want {
			t.Errorf("deriveSvnName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSvnBuildURL(t *testing.T) {
	cases := []struct{ base, sub, want string }{
		{"https://host/svn/repo", "", "https://host/svn/repo"},
		{"https://host/svn/repo/", "trunk", "https://host/svn/repo/trunk"},
		{"https://host/svn/repo", "/trunk/sub/", "https://host/svn/repo/trunk/sub"},
		{"  https://host/svn/repo  ", " trunk ", "https://host/svn/repo/trunk"},
	}
	for _, c := range cases {
		if got := svnBuildURL(c.base, c.sub); got != c.want {
			t.Errorf("svnBuildURL(%q,%q) = %q, want %q", c.base, c.sub, got, c.want)
		}
	}
}

// TestResolveRepoDirUnicode locks in that a folder name may be Japanese (or any
// Unicode letters/numbers) — an SVN checkout target can be named 日本語プロジェクト —
// while path traversal and other unsafe names stay rejected.
func TestResolveRepoDirUnicode(t *testing.T) {
	ok := []string{"日本語プロジェクト", "repo", "repo-2", "my.repo", "x@feat-1", "数字123", "café"}
	for _, name := range ok {
		if dir, valid := resolveRepoDir(name); !valid {
			t.Errorf("resolveRepoDir(%q) rejected a valid name", name)
		} else if filepath.Base(dir) != name {
			t.Errorf("resolveRepoDir(%q) mapped to %q", name, dir)
		}
	}
	bad := []string{"", "..", "../evil", "a/b", ".hidden", "-flag", "@at", "a b", "sub\x00null"}
	for _, name := range bad {
		if _, valid := resolveRepoDir(name); valid {
			t.Errorf("resolveRepoDir(%q) accepted an unsafe name", name)
		}
	}
}

func TestSvnLocked(t *testing.T) {
	locked := []string{
		"svn: E155004: Working copy '/x' locked",
		"svn: run 'svn cleanup' to remove locks",
		"is already locked",
	}
	for _, s := range locked {
		if !svnLocked(s) {
			t.Errorf("svnLocked(%q) = false, want true", s)
		}
	}
	if svnLocked("svn: E170013: Unable to connect") {
		t.Error("svnLocked matched an unrelated error")
	}
}

func TestPickSvnCred(t *testing.T) {
	list := []secrets.SVNCred{
		{URLPrefix: "https://host/svn", Username: "broad"},
		{URLPrefix: "https://host/svn/repo", Username: "narrow"},
		{URLPrefix: "https://other", Username: "other"},
	}
	if c := pickSvnCred(list, "https://host/svn/repo/trunk"); c == nil || c.Username != "narrow" {
		t.Errorf("longest-prefix match failed: %+v", c)
	}
	if c := pickSvnCred(list, "https://host/svn/elsewhere"); c == nil || c.Username != "broad" {
		t.Errorf("broad match failed: %+v", c)
	}
	if c := pickSvnCred(list, "https://nomatch/x"); c != nil {
		t.Errorf("expected no match, got %+v", c)
	}
}

func TestSvnAuthedArgs(t *testing.T) {
	has := func(a []string, s string) bool {
		for _, x := range a {
			if x == s {
				return true
			}
		}
		return false
	}
	// No creds: base flags only, no auth, no trust.
	got, authed := svnAuthedArgs(nil, "checkout", "url", "dir")
	if authed || has(got, svnTrustFailures) || has(got, "--username") {
		t.Errorf("nil creds should add no auth/trust: %v authed=%v", got, authed)
	}
	// Trust only (no username): trust flag present, still no --username / not authed.
	got, authed = svnAuthedArgs(&secrets.SVNCred{TrustCert: true}, "update", "dir")
	if authed || !has(got, svnTrustFailures) || has(got, "--username") {
		t.Errorf("trust-only creds wrong: %v authed=%v", got, authed)
	}
	// Auth + trust: both wired, password never in argv.
	got, authed = svnAuthedArgs(&secrets.SVNCred{Username: "u", Password: "secret", TrustCert: true}, "checkout", "url", "dir")
	if !authed || !has(got, svnTrustFailures) || !has(got, "--username") || !has(got, "--password-from-stdin") || has(got, "secret") {
		t.Errorf("auth+trust wrong: %v authed=%v", got, authed)
	}
	// Order: subcommand args come last (svn wants global options before the subcommand).
	if got[len(got)-3] != "checkout" || got[len(got)-1] != "dir" {
		t.Errorf("subcommand args not trailing: %v", got)
	}
}

// TestSvnCheckoutFileRepo exercises the svn runner + info/dirty/cleanup against a
// local file:// repository — no network, no auth, no secrets store. Skipped when
// svn/svnadmin are absent (e.g. the native runtime relies on host tools).
func TestSvnCheckoutFileRepo(t *testing.T) {
	if !svnAvailable() {
		t.Skip("svn not installed")
	}
	if _, err := exec.LookPath("svnadmin"); err != nil {
		t.Skip("svnadmin not installed")
	}
	root := t.TempDir()
	repo := filepath.Join(root, "srv")
	if out, err := exec.Command("svnadmin", "create", repo).CombinedOutput(); err != nil {
		t.Fatalf("svnadmin create: %v: %s", err, out)
	}
	// Seed one file via a throwaway import dir.
	seed := filepath.Join(root, "seed")
	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seed, "hello.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repoURL := "file://" + repo
	if out, err := exec.Command("svn", "import", "--non-interactive", "-m", "seed", seed, repoURL+"/trunk").CombinedOutput(); err != nil {
		t.Fatalf("svn import: %v: %s", err, out)
	}

	wc := filepath.Join(root, "wc")
	out, err := runSvnAuthedHealing(context.Background(), wc, nil, "checkout", repoURL+"/trunk", wc)
	if err != nil {
		t.Fatalf("checkout: %v: %s", err, out)
	}
	if !isSvnRepo(wc) {
		t.Fatal("expected an svn working copy after checkout")
	}
	if rev, url := svnInfo(wc); rev == "" || url == "" {
		t.Fatalf("svnInfo empty: rev=%q url=%q", rev, url)
	}
	if svnDirty(wc) {
		t.Error("fresh checkout should not be dirty")
	}
	if err := os.WriteFile(filepath.Join(wc, "hello.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !svnDirty(wc) {
		t.Error("modified working copy should be dirty")
	}
	if out, err := svnCleanup(wc); err != nil {
		t.Fatalf("cleanup: %v: %s", err, out)
	}
}
