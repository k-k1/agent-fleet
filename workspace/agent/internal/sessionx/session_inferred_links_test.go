package sessionx

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/branchpr"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func stubJiraProjects(t *testing.T, keys ...string) {
	t.Helper()
	old := jiraProjectsOf
	t.Cleanup(func() { jiraProjectsOf = old })
	set := map[string]bool{}
	for _, k := range keys {
		set[k] = true
	}
	jiraProjectsOf = func(time.Time) map[string]bool { return set }
}

func TestJiraKeysInTakesOnlyKnownProjectsWithClearBoundaries(t *testing.T) {
	projects := map[string]bool{"PROJ": true, "AB": true}
	for in, want := range map[string][]string{
		"feature/PROJ-123-add-thing":           {"PROJ-123"},
		"PROJ-1 and AB-22: fix (PROJ-1 again)": {"PROJ-1", "AB-22"},
		"UTF-8 SHA-256 ISO-8601":               nil, // not projects of this Jira
		"XPROJ-1 PROJ-12a proj-3 PROJ-0":       nil, // glued, lower case, zero
		"fix/PROJ-7_x":                         {"PROJ-7"},
		"PROJ-1 PROJ-2 PROJ-3 PROJ-4":          {"PROJ-1", "PROJ-2", "PROJ-3"},
		"":                                     nil,
	} {
		if got := jiraKeysIn(in, projects); !reflect.DeepEqual(got, want) {
			t.Errorf("jiraKeysIn(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestRecordInferredLinksLinksBranchTitleAndClosedIssues(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stubJiraProjects(t, "PROJ")
	for _, m := range []session.Meta{
		{Name: "own", Dir: "/wt/a", Kind: session.KindClaude},
		{Name: "rev", Dir: "/wt/a", Kind: session.KindClaude, OriginSession: "own"},
		{Name: "launch", Dir: "/wt/b", Kind: session.KindClaude, WorkItem: &session.WorkItemRef{Provider: "jira", Key: "PROJ-9"}},
		{Name: "drift", Dir: "/wt/c", Kind: session.KindClaude},
	} {
		session.WriteMeta(m)
	}
	pr := &branchpr.PR{Number: 7, State: "open", URL: "https://github.com/o/r/pull/7",
		Title: "PROJ-12: tidy (UTF-8)", Closes: []string{"o/r#45"}}
	sessions := []session.Session{
		{Name: "own", Dir: "/wt/a", Branch: "feature/PROJ-11-x", PR: pr},
		{Name: "rev", Dir: "/wt/a", OriginSession: "own", Branch: "feature/PROJ-11-x", PR: pr},
		// The launch item is already held as started: nothing to add for it.
		{Name: "launch", Dir: "/wt/b", Branch: "start", CurrentBranch: "feature/PROJ-9-y",
			Links: []session.WorkItemLink{{Provider: "jira", Key: "PROJ-9", Role: session.LinkStarted}}},
		// Switched to another branch: the branch it is on now names the ticket, not the start one.
		{Name: "drift", Dir: "/wt/c", Branch: "PROJ-1-start", CurrentBranch: "PROJ-5-now", BranchDrift: true},
	}
	recordInferredLinks(sessions, time.Now())

	keys := func(ls []session.WorkItemLink) map[string]string {
		out := map[string]string{}
		for _, l := range ls {
			out[l.Provider+":"+l.Key] = l.Role
		}
		return out
	}
	if got, want := keys(sessions[0].Links), map[string]string{
		"jira:PROJ-11": "working", "jira:PROJ-12": "working", "github:o/r#45": "working",
	}; !reflect.DeepEqual(got, want) {
		t.Errorf("own links = %v, want %v", got, want)
	}
	if got := keys(sessions[1].Links); got["jira:PROJ-11"] != "watching" || got["github:o/r#45"] != "watching" {
		t.Errorf("rev links = %v, want watching: it shares its parent's copy", got)
	}
	if m, _ := session.ReadMeta("launch"); len(m.Links) != 0 {
		t.Errorf("launch meta links = %+v, want none: its key is the launch item", m.Links)
	}
	if got := keys(sessions[3].Links); len(got) != 1 || got["jira:PROJ-5"] != "working" {
		t.Errorf("drift links = %v, want only PROJ-5 from the current branch", got)
	}
	// A steady list writes nothing.
	m1, _ := session.ReadMeta("own")
	sessions[0].Links = session.LinksOf(m1)
	recordInferredLinks(sessions[:1], time.Now().Add(time.Hour))
	if m2, _ := session.ReadMeta("own"); !reflect.DeepEqual(m1.Links, m2.Links) {
		t.Errorf("a steady list rewrote the links: %+v -> %+v", m1.Links, m2.Links)
	}
}

func TestRecordInferredLinksKeepsAnItemRemovedByHandRemoved(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stubJiraProjects(t, "PROJ")
	session.WriteMeta(session.Meta{Name: "a", Dir: "/wt", Kind: session.KindClaude})
	sessions := []session.Session{{Name: "a", Dir: "/wt", Branch: "PROJ-3-x"}}
	recordInferredLinks(sessions, time.Now())
	if w, links := postLink(t, "a", `{"provider":"jira","key":"PROJ-3","remove":true}`); w.Code != 200 || len(links) != 0 {
		t.Fatalf("remove: %d %+v", w.Code, links)
	}
	sessions[0].Links = nil
	recordInferredLinks(sessions, time.Now())
	if m, _ := session.ReadMeta("a"); len(m.Links) != 0 {
		t.Fatalf("links = %+v, want PROJ-3 to stay removed", m.Links)
	}
}

func TestRecordInferredLinksWithoutJiraLinksOnlyClosedIssues(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stubJiraProjects(t)
	session.WriteMeta(session.Meta{Name: "a", Dir: "/wt", Kind: session.KindClaude})
	sessions := []session.Session{{Name: "a", Dir: "/wt", Branch: "PROJ-3-x",
		PR: &branchpr.PR{Title: "PROJ-3", Closes: []string{"o/r#1", "bad key"}}}}
	recordInferredLinks(sessions, time.Now())
	if l := sessions[0].Links; len(l) != 1 || l[0].Key != "o/r#1" {
		t.Fatalf("links = %+v, want only o/r#1", l)
	}
}

func TestJiraProjectCacheNeverWaitsAndKeepsKeysThroughAFailure(t *testing.T) {
	var calls int
	answer := func() ([]string, bool, error) { return []string{"PROJ"}, true, nil }
	clock := time.Unix(1000, 0)
	var queued func()
	starts := 0
	c := &jiraProjectCache{
		read:  func() ([]string, bool, error) { calls++; return answer() },
		async: func(f func()) { starts++; queued = f },
		now:   func() time.Time { return clock },
	}
	run := func(what string) {
		t.Helper()
		if queued == nil {
			t.Fatalf("%s: no refresh was started", what)
		}
		f := queued
		queued = nil
		f()
	}
	if got := c.known(clock); got != nil {
		t.Fatalf("first known = %v, want nothing before the read finishes", got)
	}
	c.known(clock) // in flight: no second read
	if starts != 1 {
		t.Fatalf("refreshes started = %d while one was in flight, want 1", starts)
	}
	run("first")
	if calls != 1 || !c.known(clock)["PROJ"] {
		t.Fatalf("calls=%d keys=%v", calls, c.keys)
	}
	// Due after an hour; a failing read keeps the keys and is retried sooner.
	answer = func() ([]string, bool, error) { return nil, false, errors.New("down") }
	clock = clock.Add(jiraProjectsTTL)
	c.known(clock)
	run("after the TTL")
	if !c.known(clock)["PROJ"] {
		t.Fatalf("keys = %v after a failed read, want PROJ kept", c.keys)
	}
	answer = func() ([]string, bool, error) { return nil, false, nil }
	clock = clock.Add(jiraProjectsRetry)
	c.known(clock)
	run("a retry after the failure")
	if got := c.known(clock); got != nil {
		t.Fatalf("keys = %v after Jira was disconnected, want none", got)
	}
}
