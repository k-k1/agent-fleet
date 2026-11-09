package sessionx

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/branchpr"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/notice"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/watch"
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

// memWatchStore keeps the engine's state in memory, as a copy, the way a disk would.
type memWatchStore struct{ b []byte }

func (m *memWatchStore) Load() (watch.State, bool) {
	var st watch.State
	if m.b == nil || json.Unmarshal(m.b, &st) != nil {
		return watch.State{}, false
	}
	return st, true
}

func (m *memWatchStore) Save(st watch.State) error {
	b, err := json.Marshal(st)
	m.b = b
	return err
}

// prNoticeFixture runs the session list's PR recording against an engine of the test's own, and
// ticks it after each poll as the Agent's engine loop would.
type prNoticeFixture struct {
	t     *testing.T
	store *memWatchStore
	e     *watch.Engine
	sent  *[]sentNotice
}

func newPRNoticeFixture(t *testing.T) *prNoticeFixture {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	f := &prNoticeFixture{t: t, store: &memWatchStore{}, sent: capturePRNotices(t)}
	f.restart()
	old := prNoticesEngine
	prNoticesEngine = func() *watch.Engine { return f.e }
	t.Cleanup(func() { prNoticesEngine = old })
	return f
}

// restart is an Agent restart: the engine reloads its state, the in-process sent map is gone.
func (f *prNoticeFixture) restart() {
	f.e = watch.New(f.store, newWatchSink())
	prNoticesSent.Clear()
}

func (f *prNoticeFixture) poll(rows []session.Session) {
	recordBranchPRs(rows, time.Now())
	f.e.Tick(context.Background())
}

// rowsOf are the list rows of names, each on pr.
func prRowsOf(pr *branchpr.PR, names ...string) []session.Session {
	var out []session.Session
	for _, n := range names {
		m, _ := session.ReadMeta(n)
		out = append(out, session.Session{Name: n, Dir: m.Dir, Kind: m.Kind, OriginSession: m.OriginSession,
			PR: pr, Links: session.LinksOf(m)})
	}
	return out
}

func TestRecordBranchPRsNotifiesOnceForAHeldPR(t *testing.T) {
	f := newPRNoticeFixture(t)
	sent := f.sent
	session.WriteMeta(session.Meta{Name: "own", Dir: "/wt/a", Kind: session.KindClaude, Title: "own-title"})
	session.WriteMeta(session.Meta{Name: "rev", Dir: "/wt/a", Kind: session.KindClaude, OriginSession: "own"})
	url := "https://github.com/o/r/pull/7"
	open := &branchpr.PR{Number: 7, State: "open", URL: url, Checks: "pending", Mergeable: "MERGEABLE", Head: "h1"}
	rows := func(pr *branchpr.PR) []session.Session { return prRowsOf(pr, "own", "rev") }
	// First seen: linked, nothing told.
	f.poll(rows(open))
	if len(*sent) != 0 {
		t.Fatalf("sent %+v on the poll that first linked the PR", *sent)
	}
	// CI turns green: ready, told once however many polls follow.
	green := *open
	green.Checks = "success"
	for i := 0; i < 3; i++ {
		f.poll(rows(&green))
	}
	if len(*sent) != 1 || (*sent)[0].ev.Kind != prReadyNotice || (*sent)[0].once != "pr-ready|github:o/r#7|h1" {
		t.Fatalf("sent %+v, want one pr-ready for head h1", *sent)
	}
	ev := (*sent)[0].ev
	if ev.SessionName != "own" || ev.DisplayName != "own-title" || !ev.NoBridge ||
		ev.Payload["key"] != "o/r#7" || ev.Payload["url"] != url || ev.Payload["number"] != 7 || ev.Payload["provider"] != "github" {
		t.Fatalf("event = %+v, want own's, off the bridge, with the PR's provider, key, number and url", ev)
	}
	// A push: ready again for the new head.
	pushed := green
	pushed.Head = "h2"
	f.poll(rows(&pushed))
	if len(*sent) != 2 || (*sent)[1].once != "pr-ready|github:o/r#7|h2" {
		t.Fatalf("sent %+v, want a second pr-ready for h2", *sent)
	}
	// Merged: told once, to the working session only.
	merged := &branchpr.PR{Number: 7, State: "merged", URL: url, ClosedAt: "t1"}
	f.poll(rows(merged))
	f.poll(rows(merged))
	if len(*sent) != 3 || (*sent)[2].ev.Kind != prMergedNotice || (*sent)[2].ev.SessionName != "own" ||
		(*sent)[2].once != "pr-merged|github:o/r#7|t1" {
		t.Fatalf("sent %+v, want one pr-merged for own", *sent)
	}
}

func TestRecordBranchPRsDoesNotTellAboutAPRRemovedByHand(t *testing.T) {
	f := newPRNoticeFixture(t)
	session.WriteMeta(session.Meta{Name: "a", Dir: "/wt", Kind: session.KindClaude,
		Links: []session.WorkItemLink{{Provider: "github", Key: "o/r#7", Role: "working", State: "open"}}})
	if w, _ := postLink(t, "a", `{"provider":"github","key":"o/r#7","remove":true}`); w.Code != 200 {
		t.Fatalf("remove: %d", w.Code)
	}
	// Ready to merge: told for a held PR whatever it was before, so only the removal keeps it quiet.
	f.poll(prRowsOf(&branchpr.PR{Number: 7, State: "open", Checks: "success", Mergeable: "MERGEABLE", Head: "h",
		URL: "https://github.com/o/r/pull/7"}, "a"))
	if len(*f.sent) != 0 {
		t.Fatalf("sent %+v for a PR removed by hand", *f.sent)
	}
}

func TestAPRFirstSeenMergedIsHistoryButAMergeWhileDownIsNews(t *testing.T) {
	f := newPRNoticeFixture(t)
	merged := &branchpr.PR{Number: 7, State: "merged", URL: "https://github.com/o/r/pull/7", ClosedAt: "t1"}
	// Linked before states were recorded, or already recorded merged: history.
	for _, state := range []string{"", "merged"} {
		session.WriteMeta(session.Meta{Name: "a", Dir: "/wt", Kind: session.KindClaude,
			Links: []session.WorkItemLink{{Provider: "github", Key: "o/r#7", Role: "working", State: state}}})
		f.poll(prRowsOf(merged, "a"))
	}
	if len(*f.sent) != 0 {
		t.Fatalf("sent %+v for a PR first seen merged", *f.sent)
	}
	// Recorded open, merged while the Agent was down: the engine has never seen it, and it is news.
	session.WriteMeta(session.Meta{Name: "b", Dir: "/wt2", Kind: session.KindClaude,
		Links: []session.WorkItemLink{{Provider: "github", Key: "o/r#8", Role: "working", State: "open"}}})
	m8 := *merged
	m8.Number, m8.URL = 8, "https://github.com/o/r/pull/8"
	f.poll(prRowsOf(&m8, "b"))
	if len(*f.sent) != 1 || (*f.sent)[0].once != "pr-merged|github:o/r#8|t1" {
		t.Fatalf("sent %+v, want the merge of a PR recorded open", *f.sent)
	}
}

func TestAPRSeveralSessionsWorkOnIsToldOnceAndAcrossARestart(t *testing.T) {
	f := newPRNoticeFixture(t)
	for _, n := range []string{"a", "b"} {
		session.WriteMeta(session.Meta{Name: n, Dir: "/wt/" + n, Kind: session.KindClaude,
			Links: []session.WorkItemLink{{Provider: "github", Key: "o/r#7", Role: "working", State: "open", At: "2026-11-0" + map[string]string{"a": "1", "b": "2"}[n]}}})
	}
	ready := &branchpr.PR{Number: 7, State: "open", URL: "https://github.com/o/r/pull/7", Checks: "success", Mergeable: "MERGEABLE", Head: "h1"}
	f.poll(prRowsOf(ready, "a", "b"))
	f.restart()
	f.poll(prRowsOf(ready, "a", "b"))
	f.poll(prRowsOf(ready, "b"))
	if len(*f.sent) != 1 || (*f.sent)[0].ev.SessionName != "b" {
		t.Fatalf("sent %+v, want one pr-ready, for the newer holder", *f.sent)
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

func TestAMergeIsToldAfterTheOutboxRecovers(t *testing.T) {
	f := newPRNoticeFixture(t)
	session.WriteMeta(session.Meta{Name: "a", Dir: "/wt", Kind: session.KindClaude,
		Links: []session.WorkItemLink{{Provider: "github", Key: "o/r#7", Role: "working", State: "open"}}})
	merged := &branchpr.PR{Number: 7, State: "merged", URL: "https://github.com/o/r/pull/7", ClosedAt: "2026-11-01T00:00:00Z"}
	ok := putPRNotice
	putPRNotice = func(string, notice.Event) error { return errTest{} }
	f.poll(prRowsOf(merged, "a"))
	// The engine keeps the merge owed, so the link may record it at once.
	if m, _ := session.ReadMeta("a"); m.Links[0].State != "merged" {
		t.Fatalf("state = %q, want merged recorded", m.Links[0].State)
	}
	putPRNotice = ok
	f.poll(prRowsOf(merged, "a"))
	f.poll(prRowsOf(merged, "a"))
	if len(*f.sent) != 1 || (*f.sent)[0].ev.Kind != prMergedNotice {
		t.Fatalf("sent %+v, want the merge told once the outbox works", *f.sent)
	}
}

func TestASessionLaunchedFromThePRIsToldOfItsMerge(t *testing.T) {
	f := newPRNoticeFixture(t)
	sent := f.sent
	session.WriteMeta(session.Meta{Name: "lp", Dir: "/wt", Kind: session.KindClaude,
		WorkItem: &session.WorkItemRef{Provider: "github", Key: "o/r#7"}})
	row := func(pr *branchpr.PR) []session.Session {
		m, _ := session.ReadMeta("lp")
		return []session.Session{{Name: "lp", Dir: "/wt", PR: pr, Links: session.LinksOf(m)}}
	}
	url := "https://github.com/o/r/pull/7"
	f.poll(row(&branchpr.PR{Number: 7, State: "open", URL: url}))
	f.poll(row(&branchpr.PR{Number: 7, State: "merged", URL: url, ClosedAt: "t1"}))
	if len(*sent) != 1 || (*sent)[0].ev.Kind != prMergedNotice || (*sent)[0].ev.SessionName != "lp" {
		t.Fatalf("sent %+v, want pr-merged for the session launched from the PR", *sent)
	}
}

func TestAPRClosedAgainAfterAReopenIsToldAgain(t *testing.T) {
	f := newPRNoticeFixture(t)
	sent := f.sent
	session.WriteMeta(session.Meta{Name: "a", Dir: "/wt", Kind: session.KindClaude,
		Links: []session.WorkItemLink{{Provider: "github", Key: "o/r#7", Role: "working", State: "open"}}})
	row := func(pr *branchpr.PR) []session.Session {
		m, _ := session.ReadMeta("a")
		return []session.Session{{Name: "a", Dir: "/wt", PR: pr, Links: session.LinksOf(m)}}
	}
	url := "https://github.com/o/r/pull/7"
	f.poll(row(&branchpr.PR{Number: 7, State: "closed", URL: url, ClosedAt: "t1"}))
	f.poll(row(&branchpr.PR{Number: 7, State: "open", URL: url}))
	f.poll(row(&branchpr.PR{Number: 7, State: "closed", URL: url, ClosedAt: "t2"}))
	if len(*sent) != 2 || (*sent)[0].once == (*sent)[1].once {
		t.Fatalf("sent %+v, want two pr-closed, one per close", *sent)
	}
}

func TestAPRIsToldFromThePollAfterItWasLinkedAndNotToAWatchingChild(t *testing.T) {
	f := newPRNoticeFixture(t)
	session.WriteMeta(session.Meta{Name: "own", Dir: "/wt/a", Kind: session.KindClaude})
	session.WriteMeta(session.Meta{Name: "rev", Dir: "/wt/a", Kind: session.KindClaude, OriginSession: "own"})
	ready := &branchpr.PR{Number: 7, State: "open", URL: "https://github.com/o/r/pull/7", Checks: "success", Mergeable: "MERGEABLE", Head: "h1"}
	f.poll(prRowsOf(ready, "own", "rev"))
	if len(*f.sent) != 0 {
		t.Fatalf("sent %+v on the poll that first linked the PR", *f.sent)
	}
	f.poll(prRowsOf(ready, "own", "rev"))
	if len(*f.sent) != 1 || (*f.sent)[0].ev.SessionName != "own" {
		t.Fatalf("sent %+v, want one pr-ready for own and none for its watching child", *f.sent)
	}
}

func TestAWatchingChildIsNotToldWhenItsParentLetThePRGo(t *testing.T) {
	f := newPRNoticeFixture(t)
	session.WriteMeta(session.Meta{Name: "own", Dir: "/wt/a", Kind: session.KindClaude,
		Unlinked: []string{"o/r#7"}})
	session.WriteMeta(session.Meta{Name: "rev", Dir: "/wt/a", Kind: session.KindClaude, OriginSession: "own",
		Links: []session.WorkItemLink{{Provider: "github", Key: "o/r#7", Role: "watching", State: "open"}}})
	ready := &branchpr.PR{Number: 7, State: "open", URL: "https://github.com/o/r/pull/7", Checks: "success", Mergeable: "MERGEABLE", Head: "h1"}
	f.poll(prRowsOf(ready, "own", "rev"))
	f.poll(prRowsOf(ready, "own", "rev"))
	if len(*f.sent) != 0 {
		t.Fatalf("sent %+v to a child that only watches", *f.sent)
	}
}
