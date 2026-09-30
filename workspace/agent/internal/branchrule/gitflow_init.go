package branchrule

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
)

// Initialize Git Flow (ADR 0103 decision 9) writes git-flow's own avh-form keys into the
// clone's config, so the `git flow` CLI, Fork, git-flow-next and the resolver all read the
// same declaration. It writes on a person's press only. The one branch it may create is a
// local branch tracking an origin branch the dialog names, because gitflow-avh counts a repo
// as initialised only when both branches exist locally (decision 9's amendment, #1329).

// GitflowKeys are the keys Initialize Git Flow owns. The 409 check compares exactly these.
var GitflowKeys = []string{
	"gitflow.branch.master", "gitflow.branch.develop",
	"gitflow.prefix.feature", "gitflow.prefix.bugfix", "gitflow.prefix.release",
	"gitflow.prefix.hotfix", "gitflow.prefix.support", "gitflow.prefix.versiontag",
}

// maxBranchList bounds each branch list in GitflowState; the dialog only needs it to say
// whether a typed branch exists and to offer names.
const maxBranchList = 2000

// GitflowValues are the dialog's fields: Fork's six plus the optional bugfix prefix.
type GitflowValues struct {
	Production  string `json:"production"`
	Development string `json:"development"`
	Feature     string `json:"feature"`
	Bugfix      string `json:"bugfix"`
	Release     string `json:"release"`
	Hotfix      string `json:"hotfix"`
	VersionTag  string `json:"versiontag"`
}

// GitflowState is what the dialog opens with.
type GitflowState struct {
	// Current holds the owned keys present now; the save sends it back as Expected.
	Current map[string]string `json:"current"`
	Prefill GitflowValues     `json:"prefill"`
	Local   []string          `json:"local"`
	Origin  []string          `json:"origin"`
	// Native is git-flow-next's native form (gitflow.version set), which wins over the avh
	// keys field by field, so writing them may change nothing the resolver answers.
	Native bool `json:"native"`
	// Committed lists the committed declarations that outrank the clone's keys.
	Committed []string `json:"committed"`
	Bitbucket string   `json:"bitbucket,omitempty"`
}

// ErrGitflowChanged is the 409: the owned keys differ from the ones the dialog opened with.
var ErrGitflowChanged = errors.New("the gitflow keys changed since the dialog was opened")

// GitflowInvalidError names the field that failed the ref-name checks.
type GitflowInvalidError struct {
	Field, Reason string
}

func (e *GitflowInvalidError) Error() string { return e.Field + ": " + e.Reason }

// GitflowBranchMissingError is the refusal for a branch that exists neither locally nor on
// origin: `git flow init` would create it, and this never creates a branch.
type GitflowBranchMissingError struct {
	Field, Branch string
}

func (e *GitflowBranchMissingError) Error() string {
	return fmt.Sprintf("%s branch %q exists neither locally nor on origin", e.Field, e.Branch)
}

// GitflowWriteError is a failed `git config`; Written lists the keys already written and
// Created the tracking branches made before them, so the person knows what state the clone
// is in. Pressing again rewrites the keys and skips the branches that now exist.
type GitflowWriteError struct {
	Key     string
	Written []string
	Created []string
	Err     error
}

func (e *GitflowWriteError) Error() string {
	return "writing " + e.Key + " failed: " + e.Err.Error()
}

func (e *GitflowWriteError) Unwrap() error { return e.Err }

// GitflowBranchError is a failed tracking-branch creation. No key has been written yet;
// Created lists the branches made before it.
type GitflowBranchError struct {
	Branch  string
	Created []string
	Err     error
}

func (e *GitflowBranchError) Error() string {
	return "creating the local branch " + quote(e.Branch) + " failed: " + e.Err.Error()
}

func (e *GitflowBranchError) Unwrap() error { return e.Err }

// GitflowResult is what a successful InitGitflow did.
type GitflowResult struct {
	Written []string
	// Created lists the local branches made to track their origin branch.
	Created []string
}

// currentGitflow is the owned keys' present values, from one --get-regexp read.
func currentGitflow(kvs []kv) map[string]string {
	owned := map[string]bool{}
	for _, k := range GitflowKeys {
		owned[k] = true
	}
	out := map[string]string{}
	for _, e := range kvs {
		if owned[e.key] {
			out[e.key] = e.value
		}
	}
	return out
}

func listRefs(dir, prefix string) []string {
	out, err := gitx.Run(dir, "for-each-ref", "--format=%(refname)", "--count="+fmt.Sprint(maxBranchList+1), prefix)
	if err != nil || out == "" {
		return []string{}
	}
	names := []string{}
	for _, l := range strings.Split(out, "\n") {
		n := strings.TrimPrefix(l, prefix)
		if n == "" || n == "HEAD" {
			continue
		}
		names = append(names, n)
	}
	if len(names) > maxBranchList {
		names = names[:maxBranchList]
	}
	sort.Strings(names)
	return names
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// ReadGitflow is the dialog's opening state (GET …/gitflow). The prefill takes, in order,
// the keys already present, Bitbucket's branching model when it declares something, which of
// main / master / develop exist, and git-flow's own defaults.
func ReadGitflow(ctx context.Context, dir string, opt ReadOptions) GitflowState {
	lock := CloneLock(dir)
	lock.Lock()
	kvs := readCloneGitflow(dir)
	lock.Unlock()

	st := GitflowState{
		Current:   currentGitflow(kvs),
		Local:     listRefs(dir, "refs/heads/"),
		Origin:    listRefs(dir, "refs/remotes/origin/"),
		Committed: []string{},
	}
	for _, e := range kvs {
		if e.key == "gitflow.version" && e.value != "" {
			st.Native = true
		}
	}
	for _, p := range []string{FilePath, GitflowFile} {
		if _, ok, _ := readCommitted(dir, p); ok {
			st.Committed = append(st.Committed, p)
		}
	}

	var bb Rule
	if ws, repo, ok := bitbucketRepo(opt.ID); ok && opt.Bitbucket != nil {
		body, _, state, _ := opt.Bitbucket.Get(ctx, ws+"/"+repo, opt.Refresh)
		st.Bitbucket = state
		if state == stateOK {
			if r, counted, err := bitbucketRule(body); err == nil && counted {
				bb = r
			}
		}
	}
	bbPrefix := func(kind string) string {
		if p := bb.Types[kind].Prefix; p != nil {
			return *p
		}
		return ""
	}
	pick := func(key string, fallbacks ...string) string {
		if v, ok := st.Current[key]; ok {
			return v
		}
		for _, f := range fallbacks {
			if f != "" {
				return f
			}
		}
		return ""
	}
	production := ""
	for _, list := range [][]string{st.Origin, st.Local} {
		for _, n := range []string{"main", "master"} {
			if production == "" && contains(list, n) {
				production = n
			}
		}
	}
	development := ""
	if bb.Base != nil {
		development = *bb.Base
	}
	st.Prefill = GitflowValues{
		Production:  pick("gitflow.branch.master", production),
		Development: pick("gitflow.branch.develop", development, "develop"),
		Feature:     pick("gitflow.prefix.feature", bbPrefix("feature"), "feature/"),
		Bugfix:      pick("gitflow.prefix.bugfix", bbPrefix("bugfix")),
		Release:     pick("gitflow.prefix.release", bbPrefix("release"), "release/"),
		Hotfix:      pick("gitflow.prefix.hotfix", bbPrefix("hotfix"), "hotfix/"),
		VersionTag:  pick("gitflow.prefix.versiontag"),
	}
	return st
}

// validateGitflow applies decision 3's ref-name checks to every value before anything is
// written. The three required prefixes may not be empty: an empty feature prefix would make
// every branch a feature branch to git-flow.
func validateGitflow(v GitflowValues) error {
	for _, f := range []struct{ field, value string }{{"production", v.Production}, {"development", v.Development}} {
		if !ValidBranch(f.value) {
			return &GitflowInvalidError{f.field, quote(f.value) + " is not a branch name"}
		}
	}
	if v.Production == v.Development {
		return &GitflowInvalidError{"development", "the production and development branches must differ"}
	}
	for _, f := range []struct{ field, value string }{{"feature", v.Feature}, {"release", v.Release}, {"hotfix", v.Hotfix}} {
		if f.value == "" || !ValidPrefix(f.value) {
			return &GitflowInvalidError{f.field, quote(f.value) + " is not a branch prefix"}
		}
	}
	if !ValidPrefix(v.Bugfix) {
		return &GitflowInvalidError{"bugfix", quote(v.Bugfix) + " is not a branch prefix"}
	}
	if !validTagPrefix(v.VersionTag) {
		return &GitflowInvalidError{"versiontag", quote(v.VersionTag) + " is not a tag prefix"}
	}
	return nil
}

func sameKeys(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

// unbornHeads is the branches the repository's worktrees have checked out without a commit.
// A branch without a local ref that a worktree names is unborn there; listing every named
// branch is enough, since the caller asks only about branches that have no ref. An error
// means the check could not be made, and the caller must then create nothing.
func unbornHeads(dir string) (map[string]bool, error) {
	out, err := gitx.Run(dir, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	heads := map[string]bool{}
	for _, l := range strings.Split(out, "\n") {
		if ref, ok := strings.CutPrefix(l, "branch "); ok {
			heads[ref] = true
		}
	}
	return heads, nil
}

// createTracking makes refs/heads/<b> at origin/<b>'s commit with origin/<b> as its upstream,
// the result of `git branch --track`, in two steps so a failure leaves nothing half-made:
// `git branch` creates the ref before it writes the upstream, and a retry would then skip the
// branch and leave it untracked. made reports a ref that stayed despite the error.
func createTracking(dir, b string) (made bool, err error) {
	ref := "refs/heads/" + b
	sha, err := gitx.Run(dir, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+b+"^{commit}")
	if err != nil || sha == "" {
		return false, errors.New("it is not on origin")
	}
	// The empty old value makes update-ref refuse a ref that exists, so a branch that appeared
	// since the caller's check is never moved.
	if _, err := gitx.Run(dir, "update-ref", "-m", "branch: Created from origin/"+b, ref, sha, ""); err != nil {
		return false, err
	}
	if _, err := gitx.Run(dir, "branch", "--set-upstream-to=refs/remotes/origin/"+b, "--", b); err != nil {
		// Deleted only while it still points where it was created: someone who moved it since
		// owns it now.
		if _, derr := gitx.Run(dir, "update-ref", "-d", ref, sha); derr != nil {
			return true, fmt.Errorf("%w (the branch was left without an upstream: %v)", err, derr)
		}
		return false, err
	}
	return true, nil
}

// InitGitflow writes git-flow's keys into dir's config (POST …/gitflow/init). expected is
// the Current the dialog opened with; when the keys differ now, nothing is written and the
// answer is ErrGitflowChanged. The production branch is refused like the development branch
// when it exists neither locally nor on origin: it is the hotfix base, and a missing one
// would only resurface as a warning at every hotfix launch.
//
// A production or development branch that is only on origin first gets a local branch
// tracking it, since gitflow-avh refuses a repo whose branches are not local. That is the
// only branch this creates: never one absent from origin, never over an existing local
// branch, and never with a checkout.
//
// The prefixes are written next and the two branch keys last: the resolver ignores the avh
// form until both branch keys exist, so a first initialisation is seen whole or not at all.
// An empty bugfix prefix leaves the bugfix key as it is, and support/ is written only when
// the support key is absent, as `git flow init -d` does.
func InitGitflow(dir string, expected map[string]string, v GitflowValues) (GitflowResult, error) {
	res := GitflowResult{Written: []string{}, Created: []string{}}
	if err := validateGitflow(v); err != nil {
		return res, err
	}
	for _, f := range []struct{ field, branch string }{{"production", v.Production}, {"development", v.Development}} {
		if !refExists(dir, "refs/heads/"+f.branch) && !refExists(dir, "refs/remotes/origin/"+f.branch) {
			return res, &GitflowBranchMissingError{f.field, f.branch}
		}
	}
	lock := CloneLock(dir)
	lock.Lock()
	defer lock.Unlock()
	cur := currentGitflow(readCloneGitflow(dir))
	if expected == nil {
		expected = map[string]string{}
	}
	if !sameKeys(cur, expected) {
		return res, ErrGitflowChanged
	}
	for _, b := range []string{v.Production, v.Development} {
		if refExists(dir, "refs/heads/"+b) {
			continue
		}
		// An unborn HEAD naming this branch, in any worktree, would be born by the ref: a
		// switch in all but name, leaving that index and work tree out of step with the commit.
		unborn, err := unbornHeads(dir)
		if err != nil {
			return res, &GitflowBranchError{Branch: b, Created: res.Created, Err: fmt.Errorf("the worktrees could not be listed: %w", err)}
		}
		if unborn["refs/heads/"+b] {
			return res, &GitflowBranchError{Branch: b, Created: res.Created, Err: errors.New("a worktree has it checked out, not yet born")}
		}
		made, err := createTracking(dir, b)
		if made {
			res.Created = append(res.Created, b)
		}
		if err != nil {
			return res, &GitflowBranchError{Branch: b, Created: res.Created, Err: err}
		}
	}
	writes := []kv{{"gitflow.prefix.feature", v.Feature}}
	if v.Bugfix != "" {
		writes = append(writes, kv{"gitflow.prefix.bugfix", v.Bugfix})
	}
	writes = append(writes,
		kv{"gitflow.prefix.release", v.Release},
		kv{"gitflow.prefix.hotfix", v.Hotfix},
	)
	if _, ok := cur["gitflow.prefix.support"]; !ok {
		writes = append(writes, kv{"gitflow.prefix.support", "support/"})
	}
	writes = append(writes,
		kv{"gitflow.prefix.versiontag", v.VersionTag},
		kv{"gitflow.branch.master", v.Production},
		kv{"gitflow.branch.develop", v.Development},
	)
	for _, w := range writes {
		// --replace-all: a key set twice by hand would otherwise make `git config` refuse.
		if _, err := gitx.Run(dir, "config", "--local", "--replace-all", w.key, w.value); err != nil {
			return res, &GitflowWriteError{Key: w.key, Written: res.Written, Created: res.Created, Err: err}
		}
		res.Written = append(res.Written, w.key)
	}
	return res, nil
}
