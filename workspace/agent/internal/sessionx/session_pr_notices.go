package sessionx

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/branchpr"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/notice"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/watch"
)

// The merge end of a session's PR (#3131): the notification center hears when the PR of a
// session's branch becomes ready to merge, and when it is merged or closed. The session list
// poll that records the PR link hands each PR to a built-in subscription of the watch engine
// (ADR 0133 step 3), which keeps what was told and delivers to the member; it costs no call of
// its own. The notifications keep the once keys they had before the move, so a notification
// already in the outbox is not put again.

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

// prNoticesPurpose names the PR notices' built-in subscriptions on the watch engine.
const prNoticesPurpose = "pr-notices"

var prNoticeTypes = []string{"pr.ready", "pr.merged", "pr.closed"}

// prNoticesEngine is the engine the notices live on, replaced in tests.
var prNoticesEngine = watchEngine

func init() { builtinMemberNotice[prNoticesPurpose] = notifyPRNotices }

// prNoticeHold is one session working on the PR of its branch, seen on this poll.
type prNoticeHold struct {
	name          string
	provider, key string
	link          session.WorkItemLink // the link as it was before this poll
	pr            *branchpr.PR
}

// prSubject is the watch subject of a PR link, as a session's own watch names it.
func prSubject(provider, key string) watch.SubjectID {
	return watch.SubjectID(provider + ":" + strings.ToLower(key))
}

// syncPRNotices makes the PR notices' built-in subscriptions the PRs the poll saw, and feeds
// each its state. A PR several sessions work on is one subscription, for the strongest and
// newest holder, so it is told once.
//
// Merged and closed are news only on a change seen from open or draft: a link first seen
// already merged (a branch checked out after its merge) is history, and so is any link recorded
// before states were. A link recorded open or draft is therefore not baselined, so a merge that
// happened while the Agent was down is still told.
func syncPRNotices(holds []prNoticeHold, now time.Time) {
	sort.SliceStable(holds, func(i, j int) bool {
		a, b := holds[i], holds[j]
		if sa, sb := prSubject(a.provider, a.key), prSubject(b.provider, b.key); sa != sb {
			return sa < sb
		}
		if ra, rb := session.LinkRoleRank(a.link.Role), session.LinkRoleRank(b.link.Role); ra != rb {
			return ra > rb
		}
		if a.link.At != b.link.At {
			return a.link.At > b.link.At
		}
		return a.name < b.name
	})
	var specs []watch.BuiltinSpec
	var fed []prNoticeHold
	for _, h := range holds {
		subject := prSubject(h.provider, h.key)
		if len(specs) > 0 && specs[len(specs)-1].Subject == subject {
			continue
		}
		var baseline []string
		if h.link.State != "open" && h.link.State != "draft" {
			baseline = []string{"pr.merged", "pr.closed"}
		}
		specs = append(specs, watch.BuiltinSpec{
			Subject: subject, Owner: h.name, Ref: h.key, URL: linkURL(h.pr.URL), Types: prNoticeTypes,
			Notify: watch.NotifyMember, Baseline: baseline,
		})
		fed = append(fed, h)
	}
	e := prNoticesEngine()
	e.SyncBuiltins(prNoticesPurpose, specs)
	for _, h := range fed {
		subject, pr := prSubject(h.provider, h.key), h.pr
		e.ObserveBuiltin(subject, func(cur json.RawMessage) ([]watch.Event, json.RawMessage, bool) {
			return watch.PRConditions(subject, pr, cur, now)
		})
	}
}

// notifyPRNotices puts the notifications of one PR notices subscription. It is safe to repeat:
// each goes under its once key.
func notifyPRNotices(sub watch.Subscription, evs []watch.Event) error {
	m, _ := session.ReadMeta(sub.Owner)
	s := session.Session{Name: sub.Owner, Kind: m.Kind, Display: session.Display(m)}
	pr := &branchpr.PR{Number: linkNumber(sub.Ref), URL: sub.URL}
	for _, ev := range evs {
		var kind string
		switch ev.Type {
		case "pr.ready":
			kind = prReadyNotice
		case "pr.merged":
			kind = prMergedNotice
		case "pr.closed":
			kind = prClosedNotice
		default:
			continue
		}
		if !sendPRNotice(&s, kind, sub.Source, sub.Ref, ev.Version, pr) {
			return errors.New("the notification outbox could not be written")
		}
	}
	return nil
}

// linkNumber is N of "owner/repo#N", 0 when there is none.
func linkNumber(key string) int {
	if k, ok := prKeyOfLink(key); ok {
		return k.Number
	}
	return 0
}

// prNotice is the notification a held PR link owes, if any, by the rules the built-in
// subscriptions keep; the PR feedback watch still tells a held link's merge with it, under the
// same once keys. before is the state the link held before this poll. Only the session working on the PR is told, not a child that watches
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
// is in the outbox (now or earlier). An outbox that cannot be written answers false: the engine
// offers the notification again on its next tick, and the PR feedback watch keeps the state
// change that prompted it so its next tick tries again.
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
