package gitx

import (
	"encoding/json"
	"log"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// autorunClone builds origin (default branch main, plus a branch "other") and a clone in
// $HOME/repos/app. content is committed to main's settings file unless it is "".
func autorunClone(t *testing.T, content string) (home, seed, dir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home = t.TempDir()
	t.Setenv("HOME", home)
	seed = filepath.Join(home, "seed")
	gitInit(t, seed)
	if content != "" {
		writeFile(t, filepath.Join(seed, AutorunFilePath), content)
		gitAt(t, seed, "add", AutorunFilePath)
		gitAt(t, seed, "commit", "-m", "settings")
	}
	gitAt(t, seed, "checkout", "-b", "other")
	writeFile(t, filepath.Join(seed, AutorunFilePath), "version: 1 # other\n")
	gitAt(t, seed, "add", AutorunFilePath)
	gitAt(t, seed, "commit", "-m", "other settings")
	gitAt(t, seed, "checkout", "main")
	gitAt(t, home, "clone", "--bare", seed, "origin.git")
	dir = filepath.Join(home, "repos", "app")
	gitAt(t, home, "clone", filepath.Join(home, "origin.git"), dir)
	return home, seed, dir
}

func getAutorunFile(t *testing.T) (int, AutorunFile, string) {
	t.Helper()
	req := httptest.NewRequest("GET", "/repos/app/autorun-file", nil)
	req.SetPathValue("name", "app")
	rec := httptest.NewRecorder()
	HandleAutorunFile(rec, req)
	var f AutorunFile
	_ = json.Unmarshal(rec.Body.Bytes(), &f)
	return rec.Code, f, rec.Body.String()
}

// The file is read from the commit origin's default branch names after a fetch: not the
// working tree, not the checked-out branch, and not a stale or repointed origin/HEAD.
func TestAutorunFileReadsOnlyTheRemoteDefaultBranch(t *testing.T) {
	home, seed, dir := autorunClone(t, "version: 1 # main v1\n")
	// What a run in the workspace can do: edit the working tree, commit on its own branch,
	// repoint origin/HEAD at another branch.
	gitAt(t, dir, "checkout", "-b", "work")
	writeFile(t, filepath.Join(dir, AutorunFilePath), "version: 1 # working tree\n")
	gitAt(t, dir, "commit", "-am", "local settings")
	gitAt(t, dir, "remote", "set-head", "origin", "other")
	// main moves on origin after the clone: only a fetch sees it.
	writeFile(t, filepath.Join(seed, AutorunFilePath), "version: 1 # main v2\n")
	gitAt(t, seed, "commit", "-am", "settings v2")
	gitAt(t, seed, "push", filepath.Join(home, "origin.git"), "main")
	want := strings.TrimSpace(gitAt(t, seed, "rev-parse", "main"))

	code, f, body := getAutorunFile(t)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if !f.Found || f.Content != "version: 1 # main v2\n" || f.Branch != "main" || f.Commit != want || f.Path != AutorunFilePath ||
		f.Remote != filepath.Join(home, "origin.git") {
		t.Fatalf("got %+v, want main's v2 at %s", f, want)
	}
}

// What the clone says after the fetch does not matter: a tracking ref repointed the moment the
// fetch writes it, and a replace ref that swaps the file's blob, both leave the read on the
// commit origin named and its genuine content.
func TestAutorunFileIgnoresRefsTheCloneRewrites(t *testing.T) {
	home, seed, dir := autorunClone(t, "version: 1 # old\n")
	// main moves on origin, so the fetch writes origin/main and the hook runs.
	writeFile(t, filepath.Join(seed, AutorunFilePath), "version: 1 # main\n")
	gitAt(t, seed, "commit", "-am", "settings v2")
	gitAt(t, seed, "push", filepath.Join(home, "origin.git"), "main")
	want := strings.TrimSpace(gitAt(t, seed, "rev-parse", "main"))
	other := strings.TrimSpace(gitAt(t, dir, "rev-parse", "origin/other"))
	hook := filepath.Join(dir, ".git", "hooks", "reference-transaction")
	writeFile(t, hook, "#!/bin/sh\n[ \"$1\" = committed ] || exit 0\n[ -n \"$AF_TEST_REPOINTED\" ] && exit 0\n"+
		"grep -q refs/remotes/origin/main || exit 0\nAF_TEST_REPOINTED=1 git update-ref refs/remotes/origin/main "+other+"\n")
	if err := os.Chmod(hook, 0o755); err != nil {
		t.Fatal(err)
	}
	// The replace ref is made ahead for the blob the new commit will bring.
	blob := strings.TrimSpace(gitAt(t, seed, "rev-parse", want+":"+AutorunFilePath))
	fake := strings.TrimSpace(gitAt(t, dir, "rev-parse", "origin/other:"+AutorunFilePath))
	gitAt(t, dir, "update-ref", "refs/replace/"+blob, fake)

	code, f, body := getAutorunFile(t)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if got := strings.TrimSpace(gitAt(t, dir, "rev-parse", "origin/main")); got != other {
		t.Fatalf("the hook did not repoint origin/main (%s): the test proves nothing", got)
	}
	if f.Commit != want || f.Content != "version: 1 # main\n" {
		t.Fatalf("got %+v, want origin's %s with its own content", f, want)
	}
}

func TestDisplayRemote(t *testing.T) {
	for in, want := range map[string]string{
		"https://user:tok@github.invalid/o/r.git":                                  "https://github.invalid/o/r.git",
		"https://github.invalid/o/r.git?token=abc#frag":                            "https://github.invalid/o/r.git",
		"ssh://git@github.invalid:22/o/r.git":                                      "ssh://github.invalid:22/o/r.git",
		"git@github.invalid:o/r.git":                                               "github.invalid:o/r.git",
		"/srv/git/r.git":                                                           "/srv/git/r.git",
		"file:///srv/git/r.git":                                                    "file:///srv/git/r.git",
		"https://github.invalid/o/r%20x.git":                                       remoteHidden,
		"ext::sh -c 'curl secret'":                                                 remoteHidden,
		"https://github.invalid/o/r.git;tok=abc":                                   remoteHidden,
		"/srv/git/r.git?x":                                                         remoteHidden,
		"weird remote with spaces":                                                 remoteHidden,
		"https://git.invalid/o%2Fr/x.git":                                          remoteHidden,
		"https://git.invalid/ghp_abcdefghijklmnop/o/r.git":                         remoteHidden,
		"git.invalid:a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8/r.git":                   remoteHidden,
		"https://git.invalid/sk-tools/r.git":                                       "https://git.invalid/sk-tools/r.git",
		"https://git.invalid/o/a-very-long-repository-name-without-any-digits.git": "https://git.invalid/o/a-very-long-repository-name-without-any-digits.git",
	} {
		if got := displayRemote(in); got != want {
			t.Errorf("%s: got %s, want %s", in, got, want)
		}
	}
}

// A failed step logs its code only: git's output, which can carry a URL or a credential, stays
// out of the log and out of the answer.
func TestAutorunFileFailureLeavesGitOutputOut(t *testing.T) {
	_, _, dir := autorunClone(t, "version: 1\n")
	secretPath := "/nonexistent/ghp_" + strings.Repeat("Q", 36)
	gitAt(t, dir, "remote", "set-url", "origin", secretPath)
	var logged strings.Builder
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	code, _, body := getAutorunFile(t)
	if code != 502 || !strings.Contains(body, "no_default_branch") {
		t.Fatalf("status %d: %s", code, body)
	}
	if !strings.Contains(logged.String(), "no_default_branch") {
		t.Fatalf("nothing logged: %q", logged.String())
	}
	if strings.Contains(logged.String()+body, "QQQQ") {
		t.Fatalf("git output leaked: log %q body %s", logged.String(), body)
	}
}

// A remote path that carries a token is not shown on a successful read either.
func TestAutorunFileHidesATokenInTheRemotePath(t *testing.T) {
	home, _, dir := autorunClone(t, "version: 1\n")
	tokDir := filepath.Join(home, "ghp_"+strings.Repeat("Q", 36))
	if err := os.Rename(filepath.Join(home, "origin.git"), tokDir); err != nil {
		t.Fatal(err)
	}
	gitAt(t, dir, "remote", "set-url", "origin", tokDir)
	code, f, body := getAutorunFile(t)
	if code != 200 || !f.Found || f.Remote != remoteHidden || strings.Contains(body, "QQQQ") {
		t.Fatalf("status %d: %s", code, body)
	}
}

// Every credential shape the Control Plane refuses hides the remote on a successful read, not
// only the token prefixes the path heuristic knows.
func TestAutorunFileHidesEveryCredentialShapeInTheRemote(t *testing.T) {
	home, _, dir := autorunClone(t, "version: 1\n")
	key := "sk-" + strings.Repeat("A1", 20)
	keyDir := filepath.Join(home, key)
	if err := os.Rename(filepath.Join(home, "origin.git"), keyDir); err != nil {
		t.Fatal(err)
	}
	gitAt(t, dir, "remote", "set-url", "origin", keyDir)
	code, f, body := getAutorunFile(t)
	if code != 200 || !f.Found || f.Remote != remoteHidden || strings.Contains(body, "A1A1") {
		t.Fatalf("status %d: %s", code, body)
	}
	if got := displayRemote("https://git.invalid/" + key + "/r.git"); got != remoteHidden {
		t.Fatalf("network remote: %s", got)
	}
}

// A read changes nothing but the one tracking ref, whatever pruning the clone's settings ask for.
func TestAutorunFileLeavesOtherRefsAlone(t *testing.T) {
	_, _, dir := autorunClone(t, "version: 1\n")
	gitAt(t, dir, "config", "fetch.prune", "true")
	gitAt(t, dir, "config", "fetch.pruneTags", "true")
	gitAt(t, dir, "config", "--add", "remote.origin.fetch", "+refs/tags/*:refs/tags/*")
	gitAt(t, dir, "tag", "local-only")
	gitAt(t, dir, "update-ref", "refs/remotes/origin/gone", "HEAD")
	_ = os.Remove(filepath.Join(dir, ".git", "FETCH_HEAD"))
	if code, _, body := getAutorunFile(t); code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	for _, ref := range []string{"refs/tags/local-only", "refs/remotes/origin/gone"} {
		if err := exec.Command("git", "-C", dir, "show-ref", "--verify", "--quiet", ref).Run(); err != nil {
			t.Errorf("the read removed %s", ref)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".git", "FETCH_HEAD")); err == nil {
		t.Errorf("the read wrote FETCH_HEAD")
	}
}

// An extra remote.<name>.fetch mapping of the default branch is configured state the read must not
// act on: --refmap= leaves it out, so only origin/<default> moves.
func TestAutorunFileKeepsConfiguredExtraMappings(t *testing.T) {
	home, seed, dir := autorunClone(t, "version: 1 # old\n")
	gitAt(t, dir, "config", "--add", "remote.origin.fetch", "+refs/heads/main:refs/remotes/extra/main")
	gitAt(t, dir, "fetch", "origin")
	extraBefore := gitAt(t, dir, "rev-parse", "refs/remotes/extra/main")
	writeFile(t, filepath.Join(seed, AutorunFilePath), "version: 1 # new\n")
	gitAt(t, seed, "commit", "-am", "new")
	gitAt(t, seed, "push", filepath.Join(home, "origin.git"), "main")
	want := gitAt(t, seed, "rev-parse", "HEAD")

	if code, f, body := getAutorunFile(t); code != 200 || f.Content != "version: 1 # new\n" {
		t.Fatalf("status %d: %s", code, body)
	}
	if got := gitAt(t, dir, "rev-parse", "refs/remotes/origin/main"); got != want {
		t.Errorf("origin/main = %s, want %s", got, want)
	}
	if got := gitAt(t, dir, "rev-parse", "refs/remotes/extra/main"); got != extraBefore {
		t.Errorf("the read moved the extra mapping to %s, want %s", got, extraBefore)
	}
}

// The named remote is used for the network: its URL rewriting applies once, and its own
// upload-pack setting is honoured.
func TestAutorunFileUsesTheNamedRemote(t *testing.T) {
	home, seed, dir := autorunClone(t, "version: 1 # old\n")
	// origin moves after the clone: only a fetch from origin itself brings the new commit.
	writeFile(t, filepath.Join(seed, AutorunFilePath), "version: 1 # real\n")
	gitAt(t, seed, "commit", "-am", "real")
	gitAt(t, seed, "push", filepath.Join(home, "origin.git"), "main")
	// A second repository that a second rewrite would reach.
	evil := filepath.Join(home, "evil")
	gitInit(t, filepath.Join(home, "evilseed"))
	writeFile(t, filepath.Join(home, "evilseed", AutorunFilePath), "version: 1 # evil\n")
	gitAt(t, filepath.Join(home, "evilseed"), "add", AutorunFilePath)
	gitAt(t, filepath.Join(home, "evilseed"), "commit", "-m", "evil")
	if err := os.MkdirAll(evil, 0o755); err != nil {
		t.Fatal(err)
	}
	gitAt(t, evil, "clone", "--bare", filepath.Join(home, "evilseed"), "origin.git")
	gitAt(t, dir, "config", "url."+home+"/.insteadOf", "alias:")
	gitAt(t, dir, "config", "url."+evil+"/.insteadOf", home+"/")
	gitAt(t, dir, "remote", "set-url", "origin", "alias:origin.git")
	marker := filepath.Join(home, "upload-pack-ran")
	gitAt(t, dir, "config", "remote.origin.uploadpack", "echo x >> "+marker+"; git-upload-pack")

	code, f, body := getAutorunFile(t)
	if code != 200 || f.Content != "version: 1 # real\n" {
		t.Fatalf("status %d: %s", code, body)
	}
	if b, _ := os.ReadFile(marker); strings.Count(string(b), "x") != 2 {
		t.Fatalf("origin's upload-pack ran %d times, want 2 (ls-remote and fetch)", strings.Count(string(b), "x"))
	}
}

// origin repointed while the read runs fails the read.
func TestAutorunFileOriginChangedDuringTheRead(t *testing.T) {
	home, seed, dir := autorunClone(t, "version: 1 # old\n")
	writeFile(t, filepath.Join(seed, AutorunFilePath), "version: 1 # new\n")
	gitAt(t, seed, "commit", "-am", "v2")
	gitAt(t, seed, "push", filepath.Join(home, "origin.git"), "main")
	hook := filepath.Join(dir, ".git", "hooks", "reference-transaction")
	writeFile(t, hook, "#!/bin/sh\n[ \"$1\" = committed ] || exit 0\ngit remote set-url origin "+filepath.Join(home, "seed")+"\n")
	if err := os.Chmod(hook, 0o755); err != nil {
		t.Fatal(err)
	}
	if code, _, body := getAutorunFile(t); code != 502 || !strings.Contains(body, "origin_changed") {
		t.Fatalf("status %d: %s", code, body)
	}
}

// A lazy fetch of a missing blob that hangs ends with the read's deadline, not after it.
func TestAutorunFileLazyFetchIsBoundedByTheDeadline(t *testing.T) {
	home, seed, _ := autorunClone(t, "version: 1\n")
	gitAt(t, filepath.Join(home, "origin.git"), "config", "uploadpack.allowFilter", "true")
	dir := filepath.Join(home, "repos", "app")
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	gitAt(t, home, "clone", "--filter=blob:none", "--no-checkout", "file://"+filepath.Join(home, "origin.git"), dir)
	_ = seed
	// origin's upload-pack answers ls-remote and the fetch, then hangs: the third call is the
	// lazy fetch cat-file makes for the blob the partial clone lacks.
	script := filepath.Join(home, "upload-pack.sh")
	writeFile(t, script, "#!/bin/sh\nc="+filepath.Join(home, "calls")+"\nn=$(cat $c 2>/dev/null || echo 0)\necho $((n+1)) > $c\n"+
		"[ $n -ge 2 ] && sleep 30.4817\nexec git-upload-pack \"$@\"\n")
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	gitAt(t, dir, "config", "remote.origin.uploadpack", script)
	old := autorunWait
	autorunWait = 3 * time.Second
	t.Cleanup(func() { autorunWait = old })
	start := time.Now()
	code, _, body := getAutorunFile(t)
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("took %v (status %d: %s)", took, code, body)
	}
	if code != 502 || !strings.Contains(body, "timeout") {
		t.Fatalf("status %d: %s", code, body)
	}
	if b, _ := os.ReadFile(filepath.Join(home, "calls")); strings.TrimSpace(string(b)) != "3" {
		t.Fatalf("upload-pack ran %q times: the hang was not the lazy fetch", b)
	}
	// The whole process group went with it: no sleeping upload-pack is left behind.
	deadline := time.Now().Add(3 * time.Second)
	for sleepersLeft("30.4817") {
		if time.Now().After(deadline) {
			t.Fatalf("the lazy fetch's upload-pack outlived the read")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func sleepersLeft(marker string) bool {
	ents, _ := os.ReadDir("/proc")
	for _, e := range ents {
		b, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		// The sleep itself, not a shell whose script mentions it.
		if err == nil && string(b) == "sleep\x00"+marker+"\x00" {
			return true
		}
	}
	return false
}

func TestAutorunFileAbsent(t *testing.T) {
	autorunClone(t, "")
	code, f, body := getAutorunFile(t)
	if code != 200 || f.Found || f.Content != "" || f.Commit == "" || f.Branch != "main" {
		t.Fatalf("status %d, %+v (%s): want found=false with the commit read", code, f, body)
	}
}

func TestAutorunFileRefusesASymlinkAndAnOversizedFile(t *testing.T) {
	home, seed, _ := autorunClone(t, "version: 1\n")
	if err := os.Remove(filepath.Join(seed, AutorunFilePath)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../f", filepath.Join(seed, AutorunFilePath)); err != nil {
		t.Fatal(err)
	}
	gitAt(t, seed, "add", AutorunFilePath)
	gitAt(t, seed, "commit", "-m", "symlink")
	gitAt(t, seed, "push", filepath.Join(home, "origin.git"), "main")
	if code, _, body := getAutorunFile(t); code != 422 || !strings.Contains(body, "file_not_regular") {
		t.Fatalf("symlink: status %d: %s", code, body)
	}

	if err := os.Remove(filepath.Join(seed, AutorunFilePath)); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(seed, AutorunFilePath), strings.Repeat("#", AutorunFileMaxBytes+1))
	gitAt(t, seed, "add", AutorunFilePath)
	gitAt(t, seed, "commit", "-m", "big")
	gitAt(t, seed, "push", filepath.Join(home, "origin.git"), "main")
	if code, _, body := getAutorunFile(t); code != 422 || !strings.Contains(body, "file_too_large") {
		t.Fatalf("oversized: status %d: %s", code, body)
	}
}

// A byte that is not UTF-8 refuses the file: JSON would turn it into U+FFFD and the Control
// Plane would compare text the commit does not hold.
func TestAutorunFileRefusesBytesThatAreNotUTF8(t *testing.T) {
	autorunClone(t, "version: 1\ncategories:\n  q:\n    instruction: caf\xe9\n")
	if code, f, body := getAutorunFile(t); code != 422 || !strings.Contains(body, "file_not_utf8") || f.Content != "" {
		t.Fatalf("status %d: %s", code, body)
	}
}

func TestAutorunFileWithoutOrigin(t *testing.T) {
	_, _, dir := autorunClone(t, "version: 1\n")
	gitAt(t, dir, "remote", "remove", "origin")
	if code, _, body := getAutorunFile(t); code != 502 || !strings.Contains(body, "no_origin") || strings.Contains(body, "fatal") {
		t.Fatalf("status %d: %s", code, body)
	}
}

func getAutorunInstruction(t *testing.T, commit, p string) (int, AutorunInstruction, string) {
	t.Helper()
	q := url.Values{"commit": {commit}, "path": {p}}
	req := httptest.NewRequest("GET", "/repos/app/autorun-file/instruction?"+q.Encode(), nil)
	req.SetPathValue("name", "app")
	rec := httptest.NewRecorder()
	HandleAutorunInstruction(rec, req)
	var f AutorunInstruction
	_ = json.Unmarshal(rec.Body.Bytes(), &f)
	return rec.Code, f, rec.Body.String()
}

// The instruction comes from the commit the settings file was read at, not from the working
// tree, a working branch or a later commit of the default branch.
func TestAutorunInstructionReadsTheSettingsCommit(t *testing.T) {
	home, seed, dir := autorunClone(t, "version: 1\n")
	writeFile(t, filepath.Join(seed, "docs/run.md"), "Run it.\n")
	writeFile(t, filepath.Join(seed, "docs/指示.md"), "日本語\n")
	gitAt(t, seed, "add", "docs")
	gitAt(t, seed, "commit", "-m", "instruction")
	gitAt(t, seed, "push", filepath.Join(home, "origin.git"), "main")
	code, f, body := getAutorunFile(t)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	// After the read: origin moves on, and a run edits the file on its own branch and in the tree.
	writeFile(t, filepath.Join(seed, "docs/run.md"), "Later.\n")
	gitAt(t, seed, "commit", "-am", "later")
	gitAt(t, seed, "push", filepath.Join(home, "origin.git"), "main")
	gitAt(t, dir, "fetch", "origin")
	gitAt(t, dir, "checkout", "-b", "work", "origin/main")
	writeFile(t, filepath.Join(dir, "docs/run.md"), "Local.\n")
	gitAt(t, dir, "commit", "-am", "local")
	writeFile(t, filepath.Join(dir, "docs/run.md"), "Tree.\n")

	code, in, body := getAutorunInstruction(t, f.Commit, "docs/run.md")
	if code != 200 || !in.Found || in.Content != "Run it.\n" || in.Commit != f.Commit || in.Path != "docs/run.md" {
		t.Fatalf("status %d, %+v (%s): want the settings commit's text", code, in, body)
	}
	if code, in, body := getAutorunInstruction(t, f.Commit, "docs/指示.md"); code != 200 || in.Content != "日本語\n" {
		t.Fatalf("non-ASCII name: status %d, %+v (%s)", code, in, body)
	}
	if code, in, body := getAutorunInstruction(t, f.Commit, "docs/none.md"); code != 200 || in.Found || in.Content != "" {
		t.Fatalf("missing file: status %d, %+v (%s)", code, in, body)
	}
	if code, _, body := getAutorunInstruction(t, f.Commit, "docs"); code != 422 || !strings.Contains(body, "file_not_regular") {
		t.Fatalf("directory: status %d: %s", code, body)
	}
}

func TestAutorunInstructionRefusesBadInput(t *testing.T) {
	home, seed, _ := autorunClone(t, "version: 1\n")
	code, f, body := getAutorunFile(t)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	for _, p := range []string{"", ".", "..", "../x", "/etc/passwd", "a/../b", "./a", "a/", `a\b`, "a//b"} {
		if code, _, body := getAutorunInstruction(t, f.Commit, p); code != 400 || !strings.Contains(body, "bad_path") {
			t.Errorf("path %q: status %d: %s", p, code, body)
		}
	}
	for _, c := range []string{"", "HEAD", "main", strings.Repeat("a", 39), "-" + strings.Repeat("a", 39)} {
		if code, _, body := getAutorunInstruction(t, c, "a.md"); code != 400 || !strings.Contains(body, "bad_commit") {
			t.Errorf("commit %q: status %d: %s", c, code, body)
		}
	}
	// A well-formed id the clone does not hold.
	if code, _, body := getAutorunInstruction(t, strings.Repeat("0", 40), "a.md"); code != 409 || !strings.Contains(body, "commit_missing") {
		t.Fatalf("unknown commit: status %d: %s", code, body)
	}
	// A symbolic link, an oversized file and bytes that are not UTF-8.
	if err := os.Symlink("../f", filepath.Join(seed, "link.md")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(seed, "big.md"), strings.Repeat("#", AutorunInstructionMaxBytes+1))
	writeFile(t, filepath.Join(seed, "latin1.md"), "caf\xe9")
	gitAt(t, seed, "add", ".")
	gitAt(t, seed, "commit", "-m", "bad files")
	gitAt(t, seed, "push", filepath.Join(home, "origin.git"), "main")
	if code, f, body = getAutorunFile(t); code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	for p, want := range map[string]string{"link.md": "file_not_regular", "big.md": "file_too_large", "latin1.md": "file_not_utf8"} {
		if code, in, body := getAutorunInstruction(t, f.Commit, p); code != 422 || !strings.Contains(body, want) || in.Content != "" {
			t.Errorf("%s: status %d: %s", p, code, body)
		}
	}
}

// A name git could read as pathspec magic or a glob is the file of that name.
func TestAutorunInstructionReadsPathsLiterally(t *testing.T) {
	home, seed, _ := autorunClone(t, "version: 1\n")
	writeFile(t, filepath.Join(seed, ":run.md"), "colon\n")
	writeFile(t, filepath.Join(seed, "a.md"), "a\n")
	gitAt(t, seed, "add", "--", ":(literal):run.md", "a.md")
	gitAt(t, seed, "commit", "-m", "odd names")
	gitAt(t, seed, "push", filepath.Join(home, "origin.git"), "main")
	_, f, _ := getAutorunFile(t)
	if code, in, body := getAutorunInstruction(t, f.Commit, ":run.md"); code != 200 || !in.Found || in.Content != "colon\n" {
		t.Fatalf(":run.md: status %d, %+v (%s)", code, in, body)
	}
	if code, in, body := getAutorunInstruction(t, f.Commit, "*.md"); code != 200 || in.Found {
		t.Fatalf("*.md matched a file: status %d, %+v (%s)", code, in, body)
	}
}

// Only a commit's own full id: an annotated tag that peels to the commit is refused, and so is
// a 40-digit prefix in a SHA-256 clone.
func TestAutorunInstructionWantsAFullCommitID(t *testing.T) {
	_, _, dir := autorunClone(t, "version: 1\n")
	_, f, _ := getAutorunFile(t)
	gitAt(t, dir, "tag", "-a", "-m", "t", "v1", f.Commit)
	tag := strings.TrimSpace(gitAt(t, dir, "rev-parse", "v1"))
	if code, _, body := getAutorunInstruction(t, tag, AutorunFilePath); code != 409 || !strings.Contains(body, "commit_missing") {
		t.Fatalf("annotated tag: status %d: %s", code, body)
	}

	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	gitAt(t, filepath.Dir(dir), "init", "-q", "--object-format=sha256", "-b", "main", dir)
	writeFile(t, filepath.Join(dir, "run.md"), "x\n")
	gitAt(t, dir, "add", "run.md")
	gitAt(t, dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "-m", "c")
	full := strings.TrimSpace(gitAt(t, dir, "rev-parse", "HEAD"))
	if len(full) != 64 {
		t.Skipf("no SHA-256 object format here (%q)", full)
	}
	if code, _, body := getAutorunInstruction(t, full[:40], "run.md"); code != 409 || !strings.Contains(body, "commit_missing") {
		t.Fatalf("prefix: status %d: %s", code, body)
	}
	if code, in, body := getAutorunInstruction(t, full, "run.md"); code != 200 || in.Content != "x\n" {
		t.Fatalf("full id: status %d, %+v (%s)", code, in, body)
	}
}
