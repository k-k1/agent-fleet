package sessionx

import (
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/branchpr"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/notice"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

type sentNotice struct {
	once string
	ev   notice.Event
}

// capturePRNotices records what recordBranchPRs would put, and forgets what earlier tests sent.
func capturePRNotices(t *testing.T) *[]sentNotice {
	t.Helper()
	old := putPRNotice
	t.Cleanup(func() { putPRNotice = old })
	prNoticesSent.Range(func(k, _ any) bool { prNoticesSent.Delete(k); return true })
	var got []sentNotice
	putPRNotice = func(once string, ev notice.Event) error {
		got = append(got, sentNotice{once, ev})
		return nil
	}
	return &got
}

func TestPRNoticeTellsAChangeSeenFromOpenAndReadinessPerHead(t *testing.T) {
	ready := &branchpr.PR{State: "open", Checks: "success", Mergeable: "MERGEABLE", Head: "abc"}
	for _, c := range []struct {
		name         string
		pr           *branchpr.PR
		role, before string
		kind, extra  string
	}{
		{"merged from open", &branchpr.PR{State: "merged"}, "working", "open", prMergedNotice, ""},
		{"merged from draft", &branchpr.PR{State: "merged"}, "working", "draft", prMergedNotice, ""},
		{"closed from open", &branchpr.PR{State: "closed"}, "working", "open", prClosedNotice, ""},
		{"first seen merged", &branchpr.PR{State: "merged"}, "working", "", "", ""},
		{"merged and known merged", &branchpr.PR{State: "merged"}, "working", "merged", "", ""},
		{"watching child", &branchpr.PR{State: "merged"}, "watching", "open", "", ""},
		{"ready", ready, "working", "open", prReadyNotice, "abc"},
		{"ready, approved", &branchpr.PR{State: "open", Checks: "success", Mergeable: "MERGEABLE", ReviewDecision: "APPROVED", Head: "h"}, "working", "open", prReadyNotice, "h"},
		{"review required", &branchpr.PR{State: "open", Checks: "success", Mergeable: "MERGEABLE", ReviewDecision: "REVIEW_REQUIRED", Head: "h"}, "working", "open", "", ""},
		{"changes requested", &branchpr.PR{State: "open", Checks: "success", Mergeable: "MERGEABLE", ReviewDecision: "CHANGES_REQUESTED", Head: "h"}, "working", "open", "", ""},
		{"draft", &branchpr.PR{State: "open", Draft: true, Checks: "success", Mergeable: "MERGEABLE", Head: "h"}, "working", "draft", "", ""},
		{"ci pending", &branchpr.PR{State: "open", Checks: "pending", Mergeable: "MERGEABLE", Head: "h"}, "working", "open", "", ""},
		{"no checks", &branchpr.PR{State: "open", Mergeable: "MERGEABLE", Head: "h"}, "working", "open", "", ""},
		{"conflicting", &branchpr.PR{State: "open", Checks: "success", Mergeable: "CONFLICTING", Head: "h"}, "working", "open", "", ""},
		{"mergeability unknown", &branchpr.PR{State: "open", Checks: "success", Mergeable: "UNKNOWN", Head: "h"}, "working", "open", "", ""},
		{"no head (Bitbucket)", &branchpr.PR{State: "open", Checks: "success", Mergeable: "MERGEABLE"}, "working", "open", "", ""},
	} {
		kind, extra := prNotice(c.pr, c.role, c.before)
		if kind != c.kind || extra != c.extra {
			t.Errorf("%s: prNotice = %q %q, want %q %q", c.name, kind, extra, c.kind, c.extra)
		}
	}
}

func TestRecordBranchPRsNotifiesOnceForAHeldPR(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sent := capturePRNotices(t)
	session.WriteMeta(session.Meta{Name: "own", Dir: "/wt/a", Kind: session.KindClaude})
	session.WriteMeta(session.Meta{Name: "rev", Dir: "/wt/a", Kind: session.KindClaude, OriginSession: "own"})
	url := "https://github.com/o/r/pull/7"
	open := &branchpr.PR{Number: 7, State: "open", URL: url, Checks: "pending", Mergeable: "MERGEABLE", Head: "h1"}
	rows := func(pr *branchpr.PR) []session.Session {
		var out []session.Session
		for _, n := range []string{"own", "rev"} {
			m, _ := session.ReadMeta(n)
			out = append(out, session.Session{Name: n, Dir: m.Dir, Kind: m.Kind, Display: n + "-title",
				OriginSession: m.OriginSession, PR: pr, Links: session.LinksOf(m)})
		}
		return out
	}
	now := time.Now()
	// First seen: linked, nothing told.
	recordBranchPRs(rows(open), now)
	if len(*sent) != 0 {
		t.Fatalf("sent %+v on the poll that first linked the PR", *sent)
	}
	// CI turns green: ready, told once however many polls follow.
	green := *open
	green.Checks = "success"
	for i := 0; i < 3; i++ {
		recordBranchPRs(rows(&green), now)
	}
	if len(*sent) != 1 || (*sent)[0].ev.Kind != prReadyNotice || (*sent)[0].once != "pr-ready|github:o/r#7|h1" {
		t.Fatalf("sent %+v, want one pr-ready for head h1", *sent)
	}
	ev := (*sent)[0].ev
	if ev.SessionName != "own" || ev.DisplayName != "own-title" || !ev.NoBridge ||
		ev.Payload["key"] != "o/r#7" || ev.Payload["url"] != url || ev.Payload["number"] != 7 {
		t.Fatalf("event = %+v, want own's, off the bridge, with the PR's key, number and url", ev)
	}
	// A push: ready again for the new head.
	pushed := green
	pushed.Head = "h2"
	recordBranchPRs(rows(&pushed), now)
	if len(*sent) != 2 || (*sent)[1].once != "pr-ready|github:o/r#7|h2" {
		t.Fatalf("sent %+v, want a second pr-ready for h2", *sent)
	}
	// Merged: told once, to the working session only.
	merged := &branchpr.PR{Number: 7, State: "merged", URL: url}
	recordBranchPRs(rows(merged), now)
	recordBranchPRs(rows(merged), now)
	if len(*sent) != 3 || (*sent)[2].ev.Kind != prMergedNotice || (*sent)[2].ev.SessionName != "own" {
		t.Fatalf("sent %+v, want one pr-merged for own", *sent)
	}
}

func TestRecordBranchPRsDoesNotTellAboutAPRRemovedByHand(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sent := capturePRNotices(t)
	session.WriteMeta(session.Meta{Name: "a", Dir: "/wt", Kind: session.KindClaude,
		Links: []session.WorkItemLink{{Provider: "github", Key: "o/r#7", Role: "working", State: "open"}}})
	if w, _ := postLink(t, "a", `{"provider":"github","key":"o/r#7","remove":true}`); w.Code != 200 {
		t.Fatalf("remove: %d", w.Code)
	}
	m, _ := session.ReadMeta("a")
	// Ready to merge: told for a held PR whatever it was before, so only the removal keeps it quiet.
	recordBranchPRs([]session.Session{{Name: "a", Dir: "/wt", PR: &branchpr.PR{Number: 7, State: "open",
		Checks: "success", Mergeable: "MERGEABLE", Head: "h", URL: "https://github.com/o/r/pull/7"},
		Links: session.LinksOf(m)}}, time.Now())
	if len(*sent) != 0 {
		t.Fatalf("sent %+v for a PR removed by hand", *sent)
	}
}

func TestAFailedOutboxWriteIsRetriedOnTheNextPoll(t *testing.T) {
	sent := capturePRNotices(t)
	fail := true
	putPRNotice = func(once string, ev notice.Event) error {
		if fail {
			return errTest{}
		}
		*sent = append(*sent, sentNotice{once, ev})
		return nil
	}
	s := &session.Session{Name: "a"}
	pr := &branchpr.PR{Number: 1, State: "merged", URL: "https://github.com/o/r/pull/1"}
	sendPRNotice(s, prMergedNotice, "github", "o/r#1", "", pr)
	fail = false
	sendPRNotice(s, prMergedNotice, "github", "o/r#1", "", pr)
	sendPRNotice(s, prMergedNotice, "github", "o/r#1", "", pr)
	if len(*sent) != 1 {
		t.Fatalf("sent %d, want the failed one retried once and then held", len(*sent))
	}
}
