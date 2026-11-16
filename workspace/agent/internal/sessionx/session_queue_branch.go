package sessionx

// The branch of a session a queue started for one referenced item (ADR 0137 §4). A launch from a
// work item in the Console asks the branch-name resolver for the name and the base and sends
// them; a queue's create sends neither, so without this the session would sit on a throwaway
// temp/<slug> branch although the item that names it is known.

import (
	"context"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
)

// QueueBranchName is the branch-name resolver's seam for a queue create: main wires it to the
// resolver behind the branch_name tool, with the member's naming rules for the working copy at
// dir. name is "" when the rules give no name for the item, and base is the rules' base branch
// ("" for none).
var QueueBranchName func(ctx context.Context, dir, provider, key string) (name, base string)

// queueBranchTimeout bounds the resolver: it may read the repository's branching model over the
// network, and a delivery must not wait on that for long.
const queueBranchTimeout = 10 * time.Second

// afterQueueBranchNamed runs once nameQueueBranch has decided a name and before the worktree is
// made: the window in which a second delivery for the same item can take it. Tests plant that
// rival here; nothing else sets it.
var afterQueueBranchNamed = func() {}

// nameQueueBranch gives a queue delivery's worktree create the branch the member's naming rules
// give for its work item, when the create names none. Anything it cannot decide leaves the
// request as it is, which starts on the throwaway temp/<slug> branch: a delivery is never failed
// for a name.
//
// A branch that already exists (an earlier run for the same item) is checked out instead of
// created, as the Console's launch does when its person picks "work on the existing branch".
// One that another working copy holds cannot be checked out a second time, so that run gets the
// throwaway branch rather than a refusal.
func (c *createRun) nameQueueBranch(parent string) {
	req := c.req
	if req.QueueDelivery == nil || !req.Worktree || req.UseExisting || QueueBranchName == nil ||
		strings.TrimSpace(req.NewBranch) != "" || !gitx.IsGitRepo(parent) {
		return
	}
	item := cleanWorkItemRef(req.WorkItem)
	if item == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), queueBranchTimeout)
	defer cancel()
	name, base := QueueBranchName(ctx, parent, item.Provider, item.Key)
	name = strings.TrimSpace(name)
	if name == "" || !gitx.ValidBranchName(parent, name) {
		return
	}
	if local, remote := gitx.BranchNameStatus(parent, name); local || remote {
		if gitx.WorktreeBranches(parent)[name] != "" {
			return
		}
		req.UseExisting, req.Branch = true, name
		c.queueNamed = true
		afterQueueBranchNamed()
		return
	}
	req.NewBranch = name
	c.queueNamed = true
	if strings.TrimSpace(req.Branch) == "" {
		req.Branch = strings.TrimSpace(base)
	}
	afterQueueBranchNamed()
}
