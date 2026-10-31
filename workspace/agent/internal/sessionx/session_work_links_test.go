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
	child := session.Meta{Name: "kid1", OriginSession: "par1", CreatedAt: "t0"}
	inheritLinks(&child)
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
