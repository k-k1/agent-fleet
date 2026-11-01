package sessionx

import (
	"sync"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/branchpr"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/notice"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// The merge end of a session's PR (#3131): the notification center hears when the PR of a
// session's branch becomes ready to merge, and when it is merged or closed. Read off the same
// session list poll that records the PR link, so it costs no call of its own.

const (
	prReadyNotice  = "pr-ready"
	prMergedNotice = "pr-merged"
	prClosedNotice = "pr-closed"
)

// putPRNotice is replaced in tests: the real one writes the outbox and its once-only marker.
var putPRNotice = notice.PutOnce

// prNoticesSent keeps a key already handed to PutOnce out of it for the life of the process. A
// ready PR stays ready poll after poll, and PutOnce takes a file lock each time it is asked;
// the marker it writes is what stops a repeat across a restart.
var prNoticesSent sync.Map

// prNotice is the notification the PR of s's branch owes, if any. before is the state the link
// held before this poll. Only the session working on the PR is told, not a child that watches
// it from a shared working copy: one PR, one notification.
//
// Merged and closed are told only on a change seen from open or draft. A link first seen
// already merged (a branch checked out after its merge) is history, not news; so is any link
// recorded before states were.
func prNotice(pr *branchpr.PR, role, before string) (kind, onceKey string) {
	if pr == nil || role != session.LinkWorking {
		return "", ""
	}
	open := before == "open" || before == "draft"
	switch {
	case pr.State == "merged" && open:
		return prMergedNotice, pr.ClosedAt
	case pr.State == "closed" && open:
		// Keyed by when it was closed: a PR reopened and closed again is news again.
		return prClosedNotice, pr.ClosedAt
	case pr.Ready() && pr.Head != "":
		// Once per head commit: a push after the notice makes it a different PR to merge. The
		// marker that keeps this across a restart is pruned after 30 days, so a PR left ready on
		// one commit for longer is announced once more after a restart: a fair reminder.
		return prReadyNotice, pr.Head
	}
	return "", ""
}

// sendPRNotice puts one notification for the PR provider/key of session s, and reports whether it
// is in the outbox (now or earlier). An outbox that cannot be written answers false, and the
// caller keeps the state change that prompted it so the next poll tries again.
func sendPRNotice(s *session.Session, kind, provider, key, extra string, pr *branchpr.PR) bool {
	once := kind + "|" + provider + ":" + key
	if extra != "" {
		once += "|" + extra
	}
	if _, sent := prNoticesSent.Load(once); sent {
		return true
	}
	ev := notice.New(kind, s.Name, s.Kind, s.Display)
	// The chat bridge has no wording for these; the notification center does.
	ev.NoBridge = true
	ev.Payload["provider"] = provider
	ev.Payload["key"] = key
	ev.Payload["number"] = pr.Number
	if u := linkURL(pr.URL); u != "" {
		ev.Payload["url"] = u
	}
	if err := putPRNotice(once, ev); err != nil {
		return false
	}
	prNoticesSent.Store(once, true)
	return true
}
