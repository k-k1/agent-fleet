package sessionx

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// stubQueueBranch plants the resolver and records how often it was asked.
func stubQueueBranch(t *testing.T, name, base string) *int {
	t.Helper()
	old := QueueBranchName
	t.Cleanup(func() { QueueBranchName = old })
	asked := 0
	QueueBranchName = func(_ context.Context, dir, provider, key string) (string, string) {
		asked++
		if provider != "github" || key != "acme/app#12" {
			t.Errorf("the resolver was asked for %q %q", provider, key)
		}
		return name, base
	}
	return &asked
}

func queueWorktreeBody(p *createProbe, workItem bool) map[string]any {
	body := queueBody(p, []map[string]any{{"id": "m-1", "kind": "text", "body": "", "provenance": "member",
		"reference": map[string]any{"provider": "github", "key": "acme/app#12", "url": "https://github.com/acme/app/issues/12"}}})
	body["worktree"] = true
	if workItem {
		body["work_item"] = map[string]any{"provider": "github", "key": "acme/app#12"}
	}
	return body
}

// branchOf is the branch checked out where the created session runs.
func branchOf(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "branch", "--show-current").CombinedOutput()
	if err != nil {
		t.Fatalf("git branch: %v: %s", err, out)
	}
	return strings.TrimSpace(string(out))
}

// A queue create that carries a work item, asks for a worktree and names no branch takes the
// branch the member's naming rules give for the item, with the rules' base.
func TestQueueCreateNamesTheBranchFromTheWorkItem(t *testing.T) {
	p := newCreateProbe(t)
	asked := stubQueueBranch(t, "item/issue-12", "main")
	rec, _ := queueCreate(t, p, queueWorktreeBody(p, true))
	if rec.Code >= 300 {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	m := p.createdMeta()
	if got := branchOf(t, m.Dir); got != "item/issue-12" {
		t.Errorf("the session's branch is %q, want item/issue-12", got)
	}
	if *asked != 1 || m.WorkItem == nil || m.WorkItem.Key != "acme/app#12" {
		t.Errorf("resolver asked %d times, work item %+v", *asked, m.WorkItem)
	}
	if filepath.Dir(m.Dir) != filepath.Dir(p.repo) {
		t.Errorf("the worktree is at %s, not beside %s", m.Dir, p.repo)
	}
}

// Without a work item, or with a branch the sender named, nothing changes: the throwaway branch,
// or the sender's.
func TestQueueCreateKeepsItsBranchWithoutAWorkItem(t *testing.T) {
	p := newCreateProbe(t)
	asked := stubQueueBranch(t, "item/issue-12", "main")
	rec, _ := queueCreate(t, p, queueWorktreeBody(p, false))
	if rec.Code >= 300 {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	if got := branchOf(t, p.createdMeta().Dir); !strings.HasPrefix(got, "temp/") {
		t.Errorf("without a work item the branch is %q, want temp/<slug>", got)
	}
	if *asked != 0 {
		t.Errorf("the resolver was asked %d times for a create without a work item", *asked)
	}

	p = newCreateProbe(t)
	asked = stubQueueBranch(t, "item/issue-12", "main")
	body := queueWorktreeBody(p, true)
	body["new_branch"] = "mine/named"
	if rec, _ := queueCreate(t, p, body); rec.Code >= 300 {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	if got := branchOf(t, p.createdMeta().Dir); got != "mine/named" || *asked != 0 {
		t.Errorf("a branch the sender named became %q (resolver asked %d times)", got, *asked)
	}
}

// A create that is not a queue's keeps what it had: the Console names its own branch, and the
// resolver is not consulted for it.
func TestNonQueueCreateWithAWorkItemIsNotRenamed(t *testing.T) {
	p := newCreateProbe(t)
	asked := stubQueueBranch(t, "item/issue-12", "main")
	rec := p.post(map[string]any{"kind": "claude", "dir": p.repo, "worktree": true,
		"work_item": map[string]any{"provider": "github", "key": "acme/app#12"}}, "")
	if rec.Code >= 300 {
		t.Skipf("create = %d %s", rec.Code, rec.Body)
	}
	if *asked != 0 {
		t.Errorf("the resolver was asked for a create that is not a queue's")
	}
}

// An earlier run for the same item left its branch behind. The Console's launch offers "work on
// the existing branch" for that clash, and the queue does it without asking: the branch is checked
// out, not forked, and the delivery is not refused.
func TestQueueCreateChecksOutAnExistingItemBranch(t *testing.T) {
	p := newCreateProbe(t)
	runIntegrationGit(t, p.repo, "branch", "item/issue-12")
	stubQueueBranch(t, "item/issue-12", "main")
	rec, _ := queueCreate(t, p, queueWorktreeBody(p, true))
	if rec.Code >= 300 {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	if got := branchOf(t, p.createdMeta().Dir); got != "item/issue-12" {
		t.Errorf("the session's branch is %q, want the existing item/issue-12", got)
	}
}

// The same when the earlier branch is only on the remote.
func TestQueueCreateChecksOutARemoteItemBranch(t *testing.T) {
	p := newCreateProbe(t)
	plantRemoteBranch(p)
	runIntegrationGit(t, p.repo, "push", "-q", "origin", "main:item/issue-12")
	runIntegrationGit(t, p.repo, "fetch", "-q", "origin")
	stubQueueBranch(t, "item/issue-12", "main")
	rec, _ := queueCreate(t, p, queueWorktreeBody(p, true))
	if rec.Code >= 300 {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	if got := branchOf(t, p.createdMeta().Dir); got != "item/issue-12" {
		t.Errorf("the session's branch is %q, want item/issue-12", got)
	}
}

// git holds a branch in one working copy at a time: when another copy has the item's branch (the
// earlier run is still there), this run starts on its own throwaway branch. A name never fails a
// delivery.
func TestQueueCreateFallsBackWhenTheItemBranchIsInUse(t *testing.T) {
	p := newCreateProbe(t)
	runIntegrationGit(t, p.repo, "worktree", "add", "-b", "item/issue-12", filepath.Join(p.home, "repos", "app@earlier"))
	stubQueueBranch(t, "feature/issue-12", "main")
	rec, _ := queueCreate(t, p, queueWorktreeBody(p, true))
	if rec.Code >= 300 {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	if got := branchOf(t, p.createdMeta().Dir); !strings.HasPrefix(got, "temp/") {
		t.Errorf("the session's branch is %q, want temp/<slug>", got)
	}
}

// A name the rules cannot give, or git refuses, leaves the throwaway branch.
func TestQueueCreateKeepsTheThrowawayBranchWhenThereIsNoName(t *testing.T) {
	for _, name := range []string{"", "bad..name", "-x"} {
		p := newCreateProbe(t)
		stubQueueBranch(t, name, "main")
		rec, _ := queueCreate(t, p, queueWorktreeBody(p, true))
		if rec.Code >= 300 {
			t.Fatalf("%q: create = %d %s", name, rec.Code, rec.Body)
		}
		if got := branchOf(t, p.createdMeta().Dir); !strings.HasPrefix(got, "temp/") {
			t.Errorf("%q: the session's branch is %q, want temp/<slug>", name, got)
		}
	}
}

// A branch name that git cannot create next to the repository's refs ("feature" exists, so
// "feature/issue-12" cannot) starts the session on its throwaway branch too.
func TestQueueCreateFallsBackWhenGitRefusesTheName(t *testing.T) {
	p := newCreateProbe(t) // its repository has a branch named "feature"
	stubQueueBranch(t, "feature/issue-12", "main")
	rec, _ := queueCreate(t, p, queueWorktreeBody(p, true))
	if rec.Code >= 300 {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	if got := branchOf(t, p.createdMeta().Dir); !strings.HasPrefix(got, "temp/") {
		t.Errorf("the session's branch is %q, want temp/<slug>", got)
	}
}
