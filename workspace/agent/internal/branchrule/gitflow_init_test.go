package branchrule

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func readGitflow(t *testing.T, dir string, bb *BitbucketCache) GitflowState {
	t.Helper()
	return ReadGitflow(context.Background(), dir, ReadOptions{ID: "bitbucket.org/acme/web", Bitbucket: bb})
}

func defaultValues() GitflowValues {
	return GitflowValues{Production: "main", Development: "develop", Feature: "feature/", Release: "release/", Hotfix: "hotfix/"}
}

// configKeys is every gitflow.* key of dir's config, as `git config` itself reads them.
func configKeys(t *testing.T, dir string) map[string]string {
	t.Helper()
	return currentGitflow(readCloneGitflow(dir))
}

func TestReadGitflowPrefillFromOrigin(t *testing.T) {
	dir := newRepo(t)
	remoteBranch(t, dir, "develop")
	remoteBranch(t, dir, "master")
	st := readGitflow(t, dir, nil)
	want := GitflowValues{Production: "master", Development: "develop", Feature: "feature/", Release: "release/", Hotfix: "hotfix/"}
	if st.Prefill != want {
		t.Errorf("prefill = %+v, want %+v", st.Prefill, want)
	}
	if len(st.Current) != 0 || st.Native || len(st.Committed) != 0 {
		t.Errorf("state = %+v", st)
	}
	if !reflect.DeepEqual(st.Local, []string{"main"}) || !reflect.DeepEqual(st.Origin, []string{"develop", "master"}) {
		t.Errorf("local = %v origin = %v", st.Local, st.Origin)
	}
}

func TestReadGitflowPrefillKeepsExistingKeys(t *testing.T) {
	dir := newRepo(t)
	git(t, dir, "config", "gitflow.branch.master", "prod")
	git(t, dir, "config", "gitflow.prefix.feature", "feat/")
	git(t, dir, "config", "gitflow.prefix.versiontag", "v")
	git(t, dir, "config", "gitflow.prefix.bugfix", "bug/")
	git(t, dir, "config", "gitflow.version", "1.0")
	commitFile(t, dir, GitflowFile, "[gitflow]\n")
	st := readGitflow(t, dir, nil)
	if st.Prefill.Production != "prod" || st.Prefill.Feature != "feat/" || st.Prefill.VersionTag != "v" || st.Prefill.Bugfix != "bug/" {
		t.Errorf("prefill = %+v", st.Prefill)
	}
	if st.Prefill.Development != "develop" || st.Prefill.Release != "release/" {
		t.Errorf("prefill defaults = %+v", st.Prefill)
	}
	if len(st.Current) != 4 || st.Current["gitflow.prefix.feature"] != "feat/" {
		t.Errorf("current = %v", st.Current)
	}
	if !st.Native || !reflect.DeepEqual(st.Committed, []string{GitflowFile}) {
		t.Errorf("native = %v committed = %v", st.Native, st.Committed)
	}
}

func TestReadGitflowPrefillFromBitbucket(t *testing.T) {
	dir := newRepo(t)
	model := `{"development":{"name":"dev","use_mainbranch":false},
	 "branch_types":[{"kind":"feature","prefix":"feat/"},{"kind":"bugfix","prefix":"fix/"},{"kind":"hotfix","prefix":"hotfix/"}]}`
	st := readGitflow(t, dir, NewBitbucketCache(fixedFetcher(model)))
	if st.Prefill.Development != "dev" || st.Prefill.Feature != "feat/" || st.Prefill.Bugfix != "fix/" || st.Prefill.Release != "release/" {
		t.Errorf("prefill = %+v", st.Prefill)
	}
	// The stock model declares nothing, so it must not prefill a bugfix prefix.
	st = readGitflow(t, newRepo(t), NewBitbucketCache(fixedFetcher(stockModel)))
	if st.Prefill.Bugfix != "" || st.Bitbucket != stateOK {
		t.Errorf("stock prefill = %+v (%s)", st.Prefill, st.Bitbucket)
	}
}

// After Initialize Git Flow the resolver reads the declaration it wrote.
func TestInitGitflowWritesKeysTheResolverReads(t *testing.T) {
	dir := newRepo(t)
	remoteBranch(t, dir, "develop")
	written, err := InitGitflow(dir, nil, defaultValues())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"gitflow.prefix.feature", "gitflow.prefix.release", "gitflow.prefix.hotfix",
		"gitflow.prefix.support", "gitflow.prefix.versiontag", "gitflow.branch.master", "gitflow.branch.develop"}
	if !reflect.DeepEqual(written, want) {
		t.Errorf("written = %v, want the branch keys last: %v", written, want)
	}
	got := configKeys(t, dir)
	if got["gitflow.prefix.support"] != "support/" || got["gitflow.prefix.versiontag"] != "" || got["gitflow.branch.develop"] != "develop" {
		t.Errorf("config = %v", got)
	}
	if _, ok := got["gitflow.prefix.bugfix"]; ok {
		t.Errorf("an empty bugfix prefix was written: %v", got)
	}
	repo := readRepo(t, dir, nil)
	if repo.Gitflow != "declared" {
		t.Fatalf("gitflow = %q", repo.Gitflow)
	}
	res := Name(layersFor(repo), "", Request{Item: &Item{Key: "PROJ-7", Title: "Add login"}})
	if res.Name != "feature/PROJ-7-add-login" || res.Base != "develop" {
		t.Errorf("name = %+v", res)
	}
}

func TestInitGitflowKeepsSupportAndBugfixUnlessGiven(t *testing.T) {
	dir := newRepo(t)
	remoteBranch(t, dir, "develop")
	git(t, dir, "config", "gitflow.prefix.support", "sup/")
	git(t, dir, "config", "gitflow.prefix.bugfix", "bug/")
	st := readGitflow(t, dir, nil)
	if _, err := InitGitflow(dir, st.Current, defaultValues()); err != nil {
		t.Fatal(err)
	}
	got := configKeys(t, dir)
	if got["gitflow.prefix.support"] != "sup/" || got["gitflow.prefix.bugfix"] != "bug/" {
		t.Errorf("config = %v", got)
	}
	v := defaultValues()
	v.Bugfix = "bugfix/"
	if _, err := InitGitflow(dir, configKeys(t, dir), v); err != nil {
		t.Fatal(err)
	}
	if got := configKeys(t, dir)["gitflow.prefix.bugfix"]; got != "bugfix/" {
		t.Errorf("bugfix = %q", got)
	}
}

func TestInitGitflowConflictWritesNothing(t *testing.T) {
	dir := newRepo(t)
	remoteBranch(t, dir, "develop")
	opened := readGitflow(t, dir, nil).Current
	// Someone else initialises meanwhile.
	git(t, dir, "config", "gitflow.prefix.feature", "feat/")
	_, err := InitGitflow(dir, opened, defaultValues())
	if !errors.Is(err, ErrGitflowChanged) {
		t.Fatalf("err = %v, want ErrGitflowChanged", err)
	}
	if got := configKeys(t, dir); len(got) != 1 || got["gitflow.prefix.feature"] != "feat/" {
		t.Errorf("config = %v", got)
	}
	// A dialog opened on an initialised clone that sends nothing back is a conflict too, not
	// a licence to overwrite.
	if _, err := InitGitflow(dir, map[string]string{}, defaultValues()); !errors.Is(err, ErrGitflowChanged) {
		t.Errorf("err = %v", err)
	}
}

func TestInitGitflowRefusesMissingBranches(t *testing.T) {
	dir := newRepo(t)
	var miss *GitflowBranchMissingError
	if _, err := InitGitflow(dir, nil, defaultValues()); !errors.As(err, &miss) || miss.Field != "development" {
		t.Fatalf("err = %v, want development missing", err)
	}
	remoteBranch(t, dir, "develop")
	v := defaultValues()
	v.Production = "prod"
	if _, err := InitGitflow(dir, nil, v); !errors.As(err, &miss) || miss.Field != "production" {
		t.Fatalf("err = %v, want production missing", err)
	}
	if got := configKeys(t, dir); len(got) != 0 {
		t.Errorf("config = %v", got)
	}
	// A local-only development branch is enough.
	git(t, dir, "branch", "dev2")
	v = defaultValues()
	v.Development = "dev2"
	if _, err := InitGitflow(dir, nil, v); err != nil {
		t.Errorf("err = %v", err)
	}
}

func TestInitGitflowValidatesBeforeWriting(t *testing.T) {
	dir := newRepo(t)
	remoteBranch(t, dir, "develop")
	cases := []struct {
		field string
		edit  func(*GitflowValues)
	}{
		{"production", func(v *GitflowValues) { v.Production = "-main" }},
		{"development", func(v *GitflowValues) { v.Development = "a..b" }},
		{"development", func(v *GitflowValues) { v.Development = "main" }},
		{"development", func(v *GitflowValues) { v.Development = "@{-1}" }},
		{"feature", func(v *GitflowValues) { v.Feature = "" }},
		{"release", func(v *GitflowValues) { v.Release = "rel ease/" }},
		{"hotfix", func(v *GitflowValues) { v.Hotfix = "-h/" }},
		{"bugfix", func(v *GitflowValues) { v.Bugfix = "bug:" }},
		{"versiontag", func(v *GitflowValues) { v.VersionTag = "v~" }},
	}
	for _, c := range cases {
		v := defaultValues()
		c.edit(&v)
		var inv *GitflowInvalidError
		if _, err := InitGitflow(dir, nil, v); !errors.As(err, &inv) || inv.Field != c.field {
			t.Errorf("%+v: err = %v, want invalid %s", v, err, c.field)
		}
	}
	if got := configKeys(t, dir); len(got) != 0 {
		t.Errorf("config = %v", got)
	}
}

// A failed write names the keys already written, so the person knows the clone's state.
func TestInitGitflowReportsWrittenKeysOnFailure(t *testing.T) {
	dir := newRepo(t)
	remoteBranch(t, dir, "develop")
	lock := filepath.Join(dir, ".git", "config.lock")
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := InitGitflow(dir, nil, defaultValues())
	var we *GitflowWriteError
	if !errors.As(err, &we) || we.Key != "gitflow.prefix.feature" || len(we.Written) != 0 {
		t.Fatalf("err = %#v", err)
	}
	if !strings.Contains(we.Error(), "gitflow.prefix.feature") {
		t.Errorf("message = %q", we.Error())
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	// Pressing again rewrites them all.
	if _, err := InitGitflow(dir, nil, defaultValues()); err != nil {
		t.Fatal(err)
	}
}

// Worktrees share the clone's config: initialising through one is initialising the clone.
func TestInitGitflowThroughWorktreeWritesTheClone(t *testing.T) {
	dir := newRepo(t)
	remoteBranch(t, dir, "develop")
	wt := filepath.Join(t.TempDir(), "wt")
	git(t, dir, "worktree", "add", "-q", "-b", "topic", wt)
	if CloneLock(wt) != CloneLock(dir) {
		t.Fatal("the worktree and the clone do not share a lock")
	}
	if _, err := InitGitflow(wt, nil, defaultValues()); err != nil {
		t.Fatal(err)
	}
	if got := configKeys(t, dir)["gitflow.branch.develop"]; got != "develop" {
		t.Errorf("clone config develop = %q", got)
	}
	if head := git(t, dir, "symbolic-ref", "--short", "HEAD"); head != "main" {
		t.Errorf("the clone's branch moved to %q", head)
	}
}
