package sessionx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/branchpr"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func postLink(t *testing.T, name, body string) (*httptest.ResponseRecorder, []session.WorkItemLink) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/sessions/"+name+"/links", strings.NewReader(body))
	r.SetPathValue("name", name)
	w := httptest.NewRecorder()
	HandleSessionLink(w, r)
	var out struct {
		Links []session.WorkItemLink `json:"links"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w, out.Links
}

// stubLinkRepo answers linkRepoOf from a table of (dir, provider) → repo.
func stubLinkRepo(t *testing.T, repos map[[2]string]string) {
	t.Helper()
	old := linkRepoOf
	t.Cleanup(func() { linkRepoOf = old })
	linkRepoOf = func(dir, provider string) string { return repos[[2]string{dir, provider}] }
}

func TestSessionLinkQualifiesBareNumbersWithTheSessionsRepository(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stubLinkRepo(t, map[[2]string]string{{"/wt/lk1", "github"}: "o/r"})
	session.WriteMeta(session.Meta{Name: "lk1", Dir: "/wt/lk1", Kind: session.KindClaude})

	w, links := postLink(t, "lk1", `{"provider":"github","key":"#45","title":"fix it"}`)
	if w.Code != http.StatusOK || len(links) != 1 || links[0].Key != "o/r#45" || links[0].Role != session.LinkWorking || links[0].Title != "fix it" {
		t.Fatalf("status=%d links=%+v body=%s, want o/r#45 working", w.Code, links, w.Body.String())
	}
	// A bitbucket number cannot be qualified from a GitHub origin.
	if w, _ := postLink(t, "lk1", `{"provider":"bitbucket","key":"7"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("bitbucket #7 on a GitHub copy: status=%d, want 400", w.Code)
	}
	if w, _ := postLink(t, "lk1", `{"provider":"jira","key":"PROJ-9","role":"started"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("role started: status=%d, want 400 (only a launch records it)", w.Code)
	}
	if _, links := postLink(t, "lk1", `{"provider":"jira","key":"proj-9","role":"reviewing","url":"http://x/y"}`); len(links) != 2 || links[1].Key != "PROJ-9" || links[1].URL != "" {
		t.Fatalf("links=%+v, want PROJ-9 reviewing with the http URL dropped", links)
	}
	if _, links := postLink(t, "lk1", `{"provider":"github","key":"o/r#45","remove":true}`); len(links) != 1 || links[0].Key != "PROJ-9" {
		t.Fatalf("after remove links=%+v, want PROJ-9 only", links)
	}
	m, _ := session.ReadMeta("lk1")
	if len(m.Links) != 1 {
		t.Fatalf("meta links=%+v, want the one left on disk", m.Links)
	}
	if w, _ := postLink(t, "nosuch", `{"provider":"jira","key":"PROJ-1"}`); w.Code != http.StatusNotFound {
		t.Fatalf("unknown session: status=%d, want 404", w.Code)
	}
}

func TestInheritLinksWatchesTheParentsItemsUnlessTheChildHasItsOwn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	session.WriteMeta(session.Meta{
		Name: "par1", Kind: session.KindClaude,
		WorkItem: &session.WorkItemRef{Provider: "jira", Key: "PROJ-1"},
		Links:    []session.WorkItemLink{{Provider: "github", Key: "o/r#3", Role: session.LinkCreated}},
	})
	// CreatedAt is written in local time; a non-UTC offset makes the test fail on a UTC machine too.
	child := session.Meta{Name: "kid1", OriginSession: "par1", CreatedAt: "2026-11-01T09:30:00+09:00"}
	inheritLinks(&child)
	if len(child.Links) == 1 && child.Links[0].At != "2026-11-01T00:30:00Z" {
		t.Fatalf("inherited link At=%q, want the creation instant in UTC", child.Links[0].At)
	}
	if len(child.Links) != 1 || child.Links[0].Key != "PROJ-1" || child.Links[0].Role != session.LinkWatching {
		t.Fatalf("child links=%+v, want PROJ-1 watching (not the parent's created PR)", child.Links)
	}
	own := session.Meta{Name: "kid2", OriginSession: "par1", WorkItem: &session.WorkItemRef{Provider: "jira", Key: "PROJ-2"}}
	inheritLinks(&own)
	if len(own.Links) != 0 {
		t.Fatalf("a child with its own item inherited %+v", own.Links)
	}
	none := session.Meta{Name: "kid3"}
	inheritLinks(&none)
	if len(none.Links) != 0 {
		t.Fatalf("a session with no parent inherited %+v", none.Links)
	}
}

func TestRecordBranchPRsLinksTheBranchPROnceAndOnlyWatchesForASharingChild(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, m := range []session.Meta{
		{Name: "own1", Dir: "/wt/a", Kind: session.KindClaude},
		{Name: "rev1", Dir: "/wt/a", Kind: session.KindClaude, OriginSession: "own1"},
		{Name: "gl1", Dir: "/wt/g", Kind: session.KindClaude},
	} {
		session.WriteMeta(m)
	}
	pr := &branchpr.PR{Number: 7, State: "open", URL: "https://github.com/o/r/pull/7"}
	sessions := []session.Session{
		{Name: "own1", Dir: "/wt/a", PR: pr},
		{Name: "rev1", Dir: "/wt/a", OriginSession: "own1", PR: pr},
		// A URL that is not a github.com PR page records nothing.
		{Name: "gl1", Dir: "/wt/g", PR: &branchpr.PR{Number: 2, State: "open", URL: "https://example.com/o/r/pull/2"}},
	}
	recordBranchPRs(sessions, time.Now())

	if l := sessions[0].Links; len(l) != 1 || l[0].Key != "o/r#7" || l[0].Role != session.LinkWorking || l[0].URL != pr.URL {
		t.Fatalf("own1 links=%+v, want o/r#7 working", l)
	}
	if l := sessions[1].Links; len(l) != 1 || l[0].Role != session.LinkWatching {
		t.Fatalf("rev1 links=%+v, want o/r#7 watching: it shares its parent's copy", l)
	}
	if l := sessions[2].Links; len(l) != 0 {
		t.Fatalf("gl1 links=%+v, want none", l)
	}
	// The next poll finds the link on the row and writes nothing.
	m1, _ := session.ReadMeta("own1")
	sessions[0].Links = session.LinksOf(m1)
	recordBranchPRs(sessions[:1], time.Now().Add(time.Hour))
	m2, _ := session.ReadMeta("own1")
	if len(m2.Links) != 1 || m2.Links[0].At != m1.Links[0].At {
		t.Fatalf("a steady list rewrote the link: %+v -> %+v", m1.Links, m2.Links)
	}
}

func TestUnlinkedBranchPRStaysUnlinkedUntilLinkedByHand(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stubLinkRepo(t, nil)
	session.WriteMeta(session.Meta{Name: "un1", Dir: "/wt/u", Kind: session.KindClaude})
	pr := &branchpr.PR{Number: 7, State: "open", URL: "https://github.com/o/r/pull/7"}
	poll := func() []session.WorkItemLink {
		m, _ := session.ReadMeta("un1")
		rows := []session.Session{{Name: "un1", Dir: "/wt/u", PR: pr, Links: session.LinksOf(m)}}
		recordBranchPRs(rows, time.Now())
		return rows[0].Links
	}
	if l := poll(); len(l) != 1 {
		t.Fatalf("first poll links=%+v, want the branch PR", l)
	}
	if w, l := postLink(t, "un1", `{"provider":"GitHub","key":"o/r#7","remove":true}`); w.Code != http.StatusOK || len(l) != 0 {
		t.Fatalf("remove: status=%d links=%+v", w.Code, l)
	}
	if l := poll(); len(l) != 0 {
		t.Fatalf("the next poll linked the removed PR again: %+v", l)
	}
	if _, l := postLink(t, "un1", `{"provider":"github","key":"o/r#7","role":"Reviewing"}`); len(l) != 1 || l[0].Role != session.LinkReviewing {
		t.Fatalf("relink links=%+v, want reviewing (role case ignored)", l)
	}
	if m, _ := session.ReadMeta("un1"); len(m.Unlinked) != 0 {
		t.Fatalf("relinking left the removal on record: %v", m.Unlinked)
	}
}

func TestRecordBranchPRsRaisesAWatchingLinkOnceTheCopyIsItsOwn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	session.WriteMeta(session.Meta{Name: "mv1", Dir: "/wt/own", Kind: session.KindClaude,
		Links: []session.WorkItemLink{{Provider: "github", Key: "o/r#7", Role: session.LinkWatching}}})
	m, _ := session.ReadMeta("mv1")
	rows := []session.Session{{Name: "mv1", Dir: "/wt/own", PR: &branchpr.PR{Number: 7, State: "open", URL: "https://github.com/O/R/pull/7"}, Links: session.LinksOf(m)}}
	recordBranchPRs(rows, time.Now())
	if l := rows[0].Links; len(l) != 1 || l[0].Role != session.LinkWorking {
		t.Fatalf("links=%+v, want the one link raised to working", l)
	}
}

func TestCreateFingerprintCountsTheWorkItemOnlyWhenPresent(t *testing.T) {
	base := CreateReq{ReportTo: "conv1", Dir: "/repos/app", InitialPrompt: "fix it"}
	plain := createIdempotencyKey(&base)
	a, b := base, base
	a.WorkItem = &session.WorkItemRef{Provider: "jira", Key: "PROJ-1"}
	b.WorkItem = &session.WorkItemRef{Provider: "jira", Key: "PROJ-2"}
	ka, kb := createIdempotencyKey(&a), createIdempotencyKey(&b)
	if ka == plain || ka == kb {
		t.Fatalf("fingerprints plain=%s PROJ-1=%s PROJ-2=%s: a work item must change it", plain, ka, kb)
	}
	empty := base
	empty.WorkItem = &session.WorkItemRef{}
	if createIdempotencyKey(&empty) != plain {
		t.Fatal("an empty work item changed the fingerprint of a launch without one")
	}
}

func TestRecordBranchPRsRaisesReviewingToWorkingButNotWorkingToWatching(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	session.WriteMeta(session.Meta{Name: "rv1", Dir: "/wt/rv", Kind: session.KindClaude,
		Links: []session.WorkItemLink{{Provider: "github", Key: "o/r#7", Role: session.LinkReviewing}}})
	session.WriteMeta(session.Meta{Name: "par9", Dir: "/wt/sh", Kind: session.KindClaude})
	session.WriteMeta(session.Meta{Name: "kid9", Dir: "/wt/sh", Kind: session.KindClaude, OriginSession: "par9",
		Links: []session.WorkItemLink{{Provider: "github", Key: "o/r#8", Role: session.LinkWorking}}})
	m, _ := session.ReadMeta("rv1")
	k, _ := session.ReadMeta("kid9")
	rows := []session.Session{
		{Name: "rv1", Dir: "/wt/rv", PR: &branchpr.PR{Number: 7, State: "open", URL: "https://github.com/o/r/pull/7"}, Links: session.LinksOf(m)},
		{Name: "par9", Dir: "/wt/sh"},
		{Name: "kid9", Dir: "/wt/sh", OriginSession: "par9", PR: &branchpr.PR{Number: 8, State: "open", URL: "https://github.com/o/r/pull/8"}, Links: session.LinksOf(k)},
	}
	recordBranchPRs(rows, time.Now())
	if l := rows[0].Links; len(l) != 1 || l[0].Role != session.LinkWorking {
		t.Fatalf("rv1 links=%+v, want reviewing raised to working", l)
	}
	if l := rows[2].Links; len(l) != 1 || l[0].Role != session.LinkWorking {
		t.Fatalf("kid9 links=%+v, want working kept (never lowered to watching)", l)
	}
}

func TestMoveCarriesTheLaunchItemAndLinks(t *testing.T) {
	src := session.Meta{Name: "src1", Dir: "/wt/a",
		WorkItem: &session.WorkItemRef{Provider: "jira", Key: "PROJ-1"},
		Links:    []session.WorkItemLink{{Provider: "github", Key: "o/r#2", Role: session.LinkReviewing}},
		Unlinked: []string{"o/r#9"}}
	req := moveCreateReq(src, moveRecord{Target: "dst1", Kind: session.KindClaude})
	if req.WorkItem == nil || req.WorkItem.Key != "PROJ-1" || len(req.links) != 1 || len(req.unlinked) != 1 {
		t.Fatalf("move request work_item=%+v links=%+v unlinked=%v, want the source's", req.WorkItem, req.links, req.unlinked)
	}
}

func TestCleanWorkItemRefKeysJiraAsLinksDo(t *testing.T) {
	got := cleanWorkItemRef(&session.WorkItemRef{Provider: " Jira ", Key: "proj-12"})
	if got.Provider != "jira" || got.Key != "PROJ-12" {
		t.Fatalf("cleanWorkItemRef = %+v, want jira PROJ-12", got)
	}
	if got := cleanWorkItemRef(&session.WorkItemRef{Provider: "github", Key: "#45"}); got.Key != "#45" {
		t.Fatalf("a bare GitHub number became %q; {ref} renders from it", got.Key)
	}
}

func TestApplyCreateLinksPrefersAMovedSessionsLinksOverInheritance(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	session.WriteMeta(session.Meta{Name: "par7", Kind: session.KindClaude, WorkItem: &session.WorkItemRef{Provider: "jira", Key: "PROJ-7"}})

	moved := session.Meta{Name: "dst7", OriginSession: "par7"}
	applyCreateLinks(&moved, &CreateReq{links: []session.WorkItemLink{{Provider: "github", Key: "o/r#2", Role: session.LinkReviewing}}, unlinked: []string{"o/r#9"}})
	if len(moved.Links) != 1 || moved.Links[0].Key != "o/r#2" || len(moved.Unlinked) != 1 {
		t.Fatalf("moved links=%+v unlinked=%v, want the carried ones and no inherited PROJ-7", moved.Links, moved.Unlinked)
	}
	child := session.Meta{Name: "kid7", OriginSession: "par7"}
	applyCreateLinks(&child, &CreateReq{})
	if len(child.Links) != 1 || child.Links[0].Key != "PROJ-7" || child.Links[0].Role != session.LinkWatching {
		t.Fatalf("child links=%+v, want PROJ-7 watching inherited", child.Links)
	}
}

// The branch PR's state rides on its link and follows the PR (#3166): a merge seen while the
// session is still on the branch is kept for after it has moved on, the role stays, and a
// steady state writes nothing.
func TestRecordBranchPRsKeepsThePRStateOnTheLink(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	session.WriteMeta(session.Meta{Name: "st1", Dir: "/wt/st", Kind: session.KindClaude})
	poll := func(pr *branchpr.PR) []session.WorkItemLink {
		t.Helper()
		m, _ := session.ReadMeta("st1")
		rows := []session.Session{{Name: "st1", Dir: "/wt/st", PR: pr, Links: session.LinksOf(m)}}
		recordBranchPRs(rows, time.Now())
		m, _ = session.ReadMeta("st1")
		return m.Links
	}
	url := "https://github.com/o/r/pull/7"
	if l := poll(&branchpr.PR{Number: 7, State: "open", Draft: true, URL: url}); len(l) != 1 || l[0].State != "draft" {
		t.Fatalf("links=%+v, want o/r#7 recorded as draft", l)
	}
	if l := poll(&branchpr.PR{Number: 7, State: "merged", URL: url}); len(l) != 1 || l[0].State != "merged" || l[0].Role != session.LinkWorking {
		t.Fatalf("links=%+v, want o/r#7 still working, now merged", l)
	}
	before, _ := session.ReadMeta("st1")
	after := poll(&branchpr.PR{Number: 7, State: "merged", URL: url})
	if after[0].At != before.Links[0].At || after[0].State != "merged" {
		t.Fatalf("a steady state rewrote the link: %+v -> %+v", before.Links, after)
	}
	// Moved to a branch with no PR: the last state seen stays.
	if l := poll(nil); len(l) != 1 || l[0].State != "merged" {
		t.Fatalf("links=%+v, want the merged state kept after leaving the branch", l)
	}
}

// A session launched for the PR it is on keeps its launch item as is: that record carries no
// state, and trying to give it one every poll would read the meta for nothing.
func TestRecordBranchPRsLeavesALaunchPRAlone(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	session.WriteMeta(session.Meta{Name: "lp1", Dir: "/wt/lp", Kind: session.KindClaude,
		WorkItem: &session.WorkItemRef{Provider: "github", Key: "o/r#7"}})
	m, _ := session.ReadMeta("lp1")
	rows := []session.Session{{Name: "lp1", Dir: "/wt/lp", PR: &branchpr.PR{Number: 7, State: "merged", URL: "https://github.com/o/r/pull/7"}, Links: session.LinksOf(m)}}
	recordBranchPRs(rows, time.Now())
	if m, _ := session.ReadMeta("lp1"); len(m.Links) != 0 {
		t.Fatalf("links=%+v, want the launch PR left as the launch item only", m.Links)
	}
}
