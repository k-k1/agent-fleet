package branchrule

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func readRepo(t *testing.T, dir string, bb *BitbucketCache) Repo {
	t.Helper()
	return ReadRepo(context.Background(), dir, ReadOptions{ID: "bitbucket.org/acme/web", Bitbucket: bb})
}

// Acceptance: a clone with no declaration branches off the parent's HEAD.
func TestNoDeclarationBaseIsParentHead(t *testing.T) {
	dir := newRepo(t)
	git(t, dir, "checkout", "-q", "-b", "develop")
	repo := readRepo(t, dir, nil)
	if len(repo.Layer.Rules) != 0 || repo.Gitflow != "absent" || len(repo.Warnings) != 0 {
		t.Fatalf("repo = %+v", repo)
	}
	got := Name(layersFor(repo), "bitbucket.org/acme/web", Request{Item: &Item{Key: "PROJ-1", Title: "Add login"}})
	if got.Name != "feature/PROJ-1-add-login" || got.Base != "head" {
		t.Fatalf("got %+v", got)
	}
	value, branch, w := ResolveBase(dir, got.Base)
	if value != "head" || branch != "develop" || w != nil {
		t.Errorf("ResolveBase = %q %q %v, want the parent's current branch", value, branch, w)
	}
}

func TestSuggestGitflowWhenOriginHasDevelop(t *testing.T) {
	dir := newRepo(t)
	remoteBranch(t, dir, "develop")
	if got := readRepo(t, dir, nil).Gitflow; got != "suggest" {
		t.Errorf("gitflow = %q, want suggest", got)
	}
}

func TestAgentFleetFile(t *testing.T) {
	dir := newRepo(t)
	commitFile(t, dir, FilePath, `[naming]
	name = {prefix}{key}
	base = develop
[type "hotfix"]
	prefix = hotfix/
	base = main
	from = Incident
	from = Outage
[type "spike"]
	prefix = spike/
[type "bugfix"]
	prefix = bad..prefix/
[other]
	x = 1
`)
	repo := readRepo(t, dir, nil)
	if len(repo.Layer.Rules) != 1 {
		t.Fatalf("rules = %+v", repo.Layer.Rules)
	}
	r := repo.Layer.Rules[0]
	if *r.Name != "{prefix}{key}" || *r.Base != "develop" || *r.Types["hotfix"].Prefix != "hotfix/" ||
		strings.Join(r.Types["hotfix"].From, ",") != "Incident,Outage" {
		t.Errorf("rule = %+v", r)
	}
	for _, w := range []struct{ code, sub string }{
		{"unknown_kind", "spike"}, {"bad_ref", "type.bugfix.prefix"}, {"unknown_key", "other.x"},
	} {
		if !hasWarning(repo.Warnings, w.code, w.sub) {
			t.Errorf("missing warning %s %s in %+v", w.code, w.sub, repo.Warnings)
		}
	}
	got := Name(layersFor(repo), "", Request{Item: &Item{Key: "PROJ-9", Labels: []string{"outage"}}})
	if got.Kind != "hotfix" || got.Name != "hotfix/PROJ-9" || got.Base != "main" {
		t.Errorf("got %+v", got)
	}
	if got.Sources["base"] != "repository: "+FilePath+" type.hotfix.base" {
		t.Errorf("sources.base = %q", got.Sources["base"])
	}
}

// Acceptance: an include.path inside .agent-fleet/branches must not be followed.
func TestAgentFleetFileIncludeNotFollowed(t *testing.T) {
	dir := newRepo(t)
	leak := filepath.Join(t.TempDir(), "leak.cfg")
	if err := os.WriteFile(leak, []byte("[naming]\n\tname = LEAKED\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	content := "[naming]\n\tname = {prefix}{ref}\n[include]\n\tpath = " + leak + "\n"
	commitFile(t, dir, FilePath, content)

	// Positive control: without --no-includes git does follow it from stdin, so the file
	// above really is a working include.
	cmd := exec.Command("git", "config", "--file", "-", "--get", "naming.name")
	cmd.Stdin = strings.NewReader(content)
	if out, _ := cmd.Output(); strings.TrimSpace(string(out)) != "LEAKED" {
		t.Fatalf("control: the include was not followed without --no-includes (%q); the test proves nothing", out)
	}

	repo := readRepo(t, dir, nil)
	if n := *repo.Layer.Rules[0].Name; n != "{prefix}{ref}" {
		t.Errorf("name = %q: the include was followed", n)
	}
	if !hasWarning(repo.Warnings, "unknown_key", "include.path") {
		t.Errorf("include.path should be reported as an unknown key: %+v", repo.Warnings)
	}
}

func TestCommittedFileReadFromHeadOnly(t *testing.T) {
	dir := newRepo(t)
	// Uncommitted: does not count.
	writeFile(t, dir, FilePath, "[naming]\n\tbase = develop\n")
	if rs := readRepo(t, dir, nil).Layer.Rules; len(rs) != 0 {
		t.Errorf("uncommitted file was read: %+v", rs)
	}
	// A committed symlink is a path string and is never followed.
	target := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(target, []byte("[naming]\n\tbase = develop\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, FilePath)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, FilePath)); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", FilePath)
	git(t, dir, "commit", "-q", "-m", "symlink")
	repo := readRepo(t, dir, nil)
	if len(repo.Layer.Rules) != 0 || !hasWarning(repo.Warnings, "not_regular_file", FilePath) {
		t.Errorf("symlink: %+v", repo)
	}
	// Over 16 KiB: ignored.
	git(t, dir, "rm", "-q", FilePath)
	commitFile(t, dir, FilePath, "[naming]\n\tbase = develop\n#"+strings.Repeat("x", maxCommitted)+"\n")
	if repo := readRepo(t, dir, nil); len(repo.Layer.Rules) != 0 || !hasWarning(repo.Warnings, "file_too_large", FilePath) {
		t.Errorf("large file: %+v", repo)
	}
}

func TestGitflowAvhInCloneConfig(t *testing.T) {
	dir := newRepo(t)
	git(t, dir, "config", "gitflow.prefix.feature", "feature/")
	git(t, dir, "config", "gitflow.prefix.release", "release/")
	git(t, dir, "config", "gitflow.prefix.hotfix", "hotfix/")
	git(t, dir, "config", "gitflow.prefix.support", "support/")
	git(t, dir, "config", "gitflow.prefix.versiontag", "")
	git(t, dir, "config", "gitflow.branch.master", "main")
	// Gated on both branch keys: with develop missing nothing counts.
	if repo := readRepo(t, dir, nil); len(repo.Layer.Rules) != 0 || repo.Gitflow == "declared" || len(repo.Warnings) != 0 {
		t.Fatalf("half-initialised avh form counted: %+v", repo)
	}
	git(t, dir, "config", "gitflow.branch.develop", "develop")
	repo := readRepo(t, dir, nil)
	if repo.Gitflow != "declared" || len(repo.Warnings) != 0 {
		t.Fatalf("repo = %+v", repo)
	}
	layers := layersFor(repo)
	// nvie/Fork have no bugfix, so a Bug is feature/<key> off develop.
	bug := Name(layers, "", Request{Item: &Item{Key: "PROJ-2", Type: "Bug", Title: "Crash"}})
	if bug.Kind != "feature" || bug.Name != "feature/PROJ-2-crash" || bug.Base != "develop" {
		t.Errorf("bug = %+v", bug)
	}
	if bug.Sources["base"] != "repository: git config gitflow.branch.develop" {
		t.Errorf("sources.base = %q", bug.Sources["base"])
	}
	if hf := Name(layers, "", Request{Kind: "hotfix", Slug: "x"}); hf.Base != "main" || hf.Name != "hotfix/x" {
		t.Errorf("hotfix = %+v", hf)
	}
}

func TestGitflowNativeForm(t *testing.T) {
	dir := newRepo(t)
	gf := `[gitflow]
	version = 1.0
[gitflow "branch.develop"]
	type = base
	parent = main
[gitflow "branch.feature"]
	type = topic
	parent = develop
	startpoint = trunk
	prefix = feat/
[gitflow "branch.hotfix"]
	type = topic
	parent = main
	prefix = hotfix/
[gitflow "branch.experiment"]
	type = topic
	parent = develop
	prefix = exp/
`
	commitFile(t, dir, GitflowFile, gf)
	repo := readRepo(t, dir, nil)
	if repo.Gitflow != "declared" || !hasWarning(repo.Warnings, "topic_not_kind", "experiment") {
		t.Fatalf("repo = %+v", repo)
	}
	e := Effect(layersFor(repo), "")
	want := map[string]KindView{"feature": {"feature", "feat/", "trunk"}, "hotfix": {"hotfix", "hotfix/", "main"}}
	if len(e.Kinds) != 2 {
		t.Fatalf("kinds = %+v", e.Kinds)
	}
	for _, k := range e.Kinds {
		if want[k.Kind] != k {
			t.Errorf("kind %+v, want %+v", k, want[k.Kind])
		}
	}

	// Without gitflow.version the native form does not count.
	dir2 := newRepo(t)
	commitFile(t, dir2, GitflowFile, strings.Replace(gf, "version = 1.0", "other = 1", 1))
	if repo := readRepo(t, dir2, nil); len(repo.Layer.Rules) != 0 {
		t.Errorf("native form counted without gitflow.version: %+v", repo.Layer.Rules)
	}
}

func TestRepositorySourcesMergePerField(t *testing.T) {
	dir := newRepo(t)
	commitFile(t, dir, FilePath, "[naming]\n\tname = {prefix}{key}\n\tbase = release\n")
	git(t, dir, "config", "gitflow.branch.master", "main")
	git(t, dir, "config", "gitflow.branch.develop", "develop")
	git(t, dir, "config", "gitflow.prefix.feature", "feature/")
	git(t, dir, "config", "gitflow.prefix.hotfix", "hf/")
	repo := readRepo(t, dir, nil)
	layers := layersFor(repo, Rule{Match: "bitbucket.org/acme/web", Name: str("{prefix}{ref}-{slug}")})
	got := Name(layers, "bitbucket.org/acme/web", Request{Item: &Item{Key: "PROJ-3"}, Kind: "hotfix"})
	// name from the file (strongest); the git-flow hotfix base beats the file's naming.base,
	// because inside the repository layer a kind base comes before a rule base.
	if got.Name != "hf/PROJ-3" || got.Base != "main" {
		t.Errorf("got %+v", got)
	}
}

func TestResolveBaseMissingWarns(t *testing.T) {
	dir := newRepo(t)
	if v, b, w := ResolveBase(dir, "develop"); v != "head" || b != "main" || w == nil || w.Code != "base_missing" {
		t.Errorf("missing base: %q %q %v", v, b, w)
	}
	remoteBranch(t, dir, "develop")
	if v, b, w := ResolveBase(dir, "develop"); v != "develop" || b != "develop" || w != nil {
		t.Errorf("remote-only base: %q %q %v", v, b, w)
	}
	if v, _, w := ResolveBase(dir, "default"); v != "head" || w == nil {
		t.Errorf("default without origin/HEAD: %q %v", v, w)
	}
	git(t, dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/develop")
	if v, b, w := ResolveBase(dir, "default"); v != "default" || b != "develop" || w != nil {
		t.Errorf("default: %q %q %v", v, b, w)
	}
}

func TestValidators(t *testing.T) {
	isolateGit(t)
	for v, want := range map[string]bool{"feature/": true, "": true, "a..b/": false, "-x/": false} {
		if ValidPrefix(v) != want {
			t.Errorf("ValidPrefix(%q) != %v", v, want)
		}
	}
	for v, want := range map[string]bool{"develop": true, "head": true, "@{-1}": false, "--help": false, "a b": false} {
		if ValidBase(v) != want {
			t.Errorf("ValidBase(%q) != %v", v, want)
		}
	}
	for v, want := range map[string]bool{"": true, "v": true, "v..": false} {
		if validTagPrefix(v) != want {
			t.Errorf("validTagPrefix(%q) != %v", v, want)
		}
	}
}

// --- Bitbucket ---

const stockModel = `{"development":{"name":"main","use_mainbranch":true},
 "branch_types":[{"kind":"bugfix","prefix":"bugfix/"},{"kind":"feature","prefix":"feature/"},
 {"kind":"hotfix","prefix":"hotfix/"},{"kind":"release","prefix":"release/"}]}`

func fixedFetcher(body string) Fetcher {
	return func(context.Context, string, string) ([]byte, error) { return []byte(body), nil }
}

// Acceptance: a git-flow repository whose Bitbucket model is the stock default.
func TestGitflowRepoWithStockBitbucketModel(t *testing.T) {
	dir := newRepo(t)
	git(t, dir, "config", "gitflow.branch.master", "main")
	git(t, dir, "config", "gitflow.branch.develop", "develop")
	git(t, dir, "config", "gitflow.prefix.feature", "feature/")
	repo := readRepo(t, dir, NewBitbucketCache(fixedFetcher(stockModel)))
	if repo.Bitbucket != "default" || repo.BitbucketFetchedAt == 0 {
		t.Fatalf("bitbucket = %q at %d", repo.Bitbucket, repo.BitbucketFetchedAt)
	}
	for _, r := range repo.Layer.Rules {
		if r.Source == "bitbucket" {
			t.Fatalf("the stock model counted: %+v", r)
		}
	}
	got := Name(layersFor(repo), "", Request{Item: &Item{Key: "PROJ-5", Type: "Bug"}})
	// The stock model's bugfix must not enter the kind set, and its main must not be the base.
	if got.Kind != "feature" || got.Base != "develop" {
		t.Errorf("got %+v", got)
	}
}

func TestBitbucketConfiguredModelCounts(t *testing.T) {
	dir := newRepo(t)
	model := `{"development":{"name":"develop","use_mainbranch":false},
	 "branch_types":[{"kind":"feature","prefix":"feat/"},{"kind":"hotfix","prefix":"hotfix/"}]}`
	repo := readRepo(t, dir, NewBitbucketCache(fixedFetcher(model)))
	if repo.Bitbucket != "ok" {
		t.Fatalf("bitbucket = %q", repo.Bitbucket)
	}
	got := Name(layersFor(repo), "", Request{Item: &Item{Key: "PROJ-6", Type: "Bug"}})
	if got.Name != "feat/PROJ-6" || got.Base != "develop" || got.Sources["base"] != "repository: bitbucket development" {
		t.Errorf("got %+v", got)
	}
}

func TestBitbucketPendingThenCached(t *testing.T) {
	dir := newRepo(t)
	release := make(chan struct{})
	var calls atomic.Int32
	c := NewBitbucketCache(func(context.Context, string, string) ([]byte, error) {
		calls.Add(1)
		<-release
		return []byte(`{"development":{"name":"develop","use_mainbranch":false}}`), nil
	})
	c.Wait = 50 * time.Millisecond
	repo := readRepo(t, dir, c)
	if repo.Bitbucket != "pending" || !hasWarning(repo.Warnings, "bitbucket_pending", "") {
		t.Fatalf("repo = %+v", repo)
	}
	// A second resolve while the fetch is in flight does not start another one.
	_ = readRepo(t, dir, c)
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for {
		repo = readRepo(t, dir, c)
		if repo.Bitbucket == "ok" || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if repo.Bitbucket != "ok" || *repo.Layer.Rules[0].Base != "develop" {
		t.Fatalf("after the fetch: %+v", repo)
	}
	_ = readRepo(t, dir, c)
	if n := calls.Load(); n != 1 {
		t.Errorf("fetches = %d, want 1 (in-flight guard and the ten-minute copy)", n)
	}
	// ?refresh=1 fetches again.
	_ = ReadRepo(context.Background(), dir, ReadOptions{ID: "bitbucket.org/acme/web", Bitbucket: c, Refresh: true})
	if n := calls.Load(); n != 2 {
		t.Errorf("fetches after refresh = %d, want 2", n)
	}
}

func TestBitbucketNoConnectionIsQuiet(t *testing.T) {
	dir := newRepo(t)
	c := NewBitbucketCache(func(context.Context, string, string) ([]byte, error) { return nil, ErrNoConnection })
	repo := readRepo(t, dir, c)
	if repo.Bitbucket != "none" || len(repo.Warnings) != 0 {
		t.Errorf("repo = %+v", repo)
	}
	// A non-Bitbucket origin never asks.
	repo = ReadRepo(context.Background(), dir, ReadOptions{ID: "github.com/acme/web", Bitbucket: c})
	if repo.Bitbucket != "" {
		t.Errorf("github origin: %q", repo.Bitbucket)
	}
}

// A failed refresh keeps the last good copy and says so, instead of dropping its base.
func TestBitbucketFailedRefreshKeepsCopy(t *testing.T) {
	dir := newRepo(t)
	var fail atomic.Bool
	c := NewBitbucketCache(func(context.Context, string, string) ([]byte, error) {
		if fail.Load() {
			return nil, errors.New("bitbucket 503: down")
		}
		return []byte(`{"development":{"name":"develop","use_mainbranch":false}}`), nil
	})
	first := readRepo(t, dir, c)
	fail.Store(true)
	repo := ReadRepo(context.Background(), dir, ReadOptions{ID: "bitbucket.org/acme/web", Bitbucket: c, Refresh: true})
	if repo.Bitbucket != "ok" || len(repo.Layer.Rules) != 1 || *repo.Layer.Rules[0].Base != "develop" ||
		repo.BitbucketFetchedAt != first.BitbucketFetchedAt || !hasWarning(repo.Warnings, "bitbucket_stale", "503") {
		t.Errorf("after a failed refresh: %+v", repo)
	}
	// Without any good copy the failure is an error.
	c2 := NewBitbucketCache(func(context.Context, string, string) ([]byte, error) { return nil, errors.New("bitbucket 404: gone") })
	if repo := readRepo(t, dir, c2); repo.Bitbucket != "error" || !hasWarning(repo.Warnings, "bitbucket_error", "404") {
		t.Errorf("no copy: %+v", repo)
	}
}

// branch_types lists only the enabled types, so a stock subset was configured and counts.
func TestBitbucketStockSubsetCounts(t *testing.T) {
	isolateGit(t)
	r, counted, err := bitbucketRule([]byte(`{"development":{"name":"main","use_mainbranch":true},
	 "branch_types":[{"kind":"feature","prefix":"feature/"},{"kind":"hotfix","prefix":"hotfix/"}]}`))
	if err != nil || !counted || len(r.Declares) != 2 {
		t.Errorf("subset: counted=%v declares=%v err=%v", counted, r.Declares, err)
	}
	if _, counted, _ := bitbucketRule([]byte(stockModel)); counted {
		t.Error("the stock answer counted")
	}
}

// Removing the connection drops the copy read through it, instead of using it unannounced.
func TestBitbucketDisconnectDropsCopy(t *testing.T) {
	dir := newRepo(t)
	var gone atomic.Bool
	c := NewBitbucketCache(func(context.Context, string, string) ([]byte, error) {
		if gone.Load() {
			return nil, ErrNoConnection
		}
		return []byte(`{"development":{"name":"develop","use_mainbranch":false}}`), nil
	})
	if repo := readRepo(t, dir, c); repo.Bitbucket != "ok" {
		t.Fatalf("first read: %+v", repo)
	}
	gone.Store(true)
	repo := ReadRepo(context.Background(), dir, ReadOptions{ID: "bitbucket.org/acme/web", Bitbucket: c, Refresh: true})
	if repo.Bitbucket != "none" || len(repo.Layer.Rules) != 0 || repo.BitbucketFetchedAt != 0 {
		t.Errorf("after disconnecting: %+v", repo)
	}
}
