package session

import (
	"strings"
	"testing"
	"time"
)

func TestNormalizeLinkKey(t *testing.T) {
	cases := []struct {
		provider, key, repo, want string
		ok                        bool
	}{
		{"github", "o/r#45", "", "o/r#45", true},
		{"GitHub", "#45", "o/r", "o/r#45", true},
		{"github", "45", "o/r", "o/r#45", true},
		// A bare number with no repository would name the same item in every repository.
		{"github", "45", "", "", false},
		{"bitbucket", "7", "ws/app", "ws/app#7", true},
		{"jira", "proj-12", "", "PROJ-12", true},
		{"jira", "45", "o/r", "45", false},
		{"gitlab", "o/r#1", "", "", false},
		{"github", "o/r#0", "", "o/r#0", false},
	}
	for _, c := range cases {
		got, ok := NormalizeLinkKey(c.provider, c.key, c.repo)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("NormalizeLinkKey(%q, %q, %q) = %q, %v; want %q, %v", c.provider, c.key, c.repo, got, ok, c.want, c.ok)
		}
	}
}

func TestAddLinkKeepsOneLinkPerItemAndTheStrongerRole(t *testing.T) {
	m := Meta{WorkItem: &WorkItemRef{Provider: "jira", Key: "PROJ-1", Title: "launch"}}
	if AddLink(&m, WorkItemLink{Provider: "jira", Key: "PROJ-1", Role: LinkWorking}) {
		t.Fatal("the launch item was linked a second time")
	}
	if !AddLink(&m, WorkItemLink{Provider: "github", Key: "o/r#2", Role: LinkWatching}) {
		t.Fatal("a new item was not linked")
	}
	if !AddLink(&m, WorkItemLink{Provider: "github", Key: "o/r#2", Role: LinkWorking}) || m.Links[0].Role != LinkWorking {
		t.Fatalf("working did not replace watching: %+v", m.Links)
	}
	if AddLink(&m, WorkItemLink{Provider: "GITHUB", Key: "o/r#2", Role: LinkWatching}) || m.Links[0].Role != LinkWorking {
		t.Fatalf("watching downgraded working: %+v", m.Links)
	}
	if !AddLink(&m, WorkItemLink{Provider: "github", Key: "o/r#2", Role: LinkWatching, Title: "t"}) || m.Links[0].Title != "t" {
		t.Fatalf("a missing title was not filled: %+v", m.Links)
	}
	got := LinksOf(m)
	if len(got) != 2 || got[0].Role != LinkStarted || got[0].Key != "PROJ-1" || got[1].Key != "o/r#2" {
		t.Fatalf("LinksOf = %+v, want the launch item first as started", got)
	}
	if !RemoveLink(&m, "github", "o/r#2") || len(m.Links) != 0 || RemoveLink(&m, "jira", "PROJ-1") {
		t.Fatalf("RemoveLink: %+v (the launch item must not be removable)", m.Links)
	}
}

func TestAddLinkIsBounded(t *testing.T) {
	var m Meta
	for i := 0; i < MaxLinks+5; i++ {
		AddLink(&m, WorkItemLink{Provider: "jira", Key: "P-" + string(rune('A'+i%26)) + string(rune('a'+i/26)), Role: LinkWatching})
	}
	if len(m.Links) != MaxLinks {
		t.Fatalf("len(Links) = %d, want the cap %d", len(m.Links), MaxLinks)
	}
}

func TestInheritedLinksTakesStartedAndWorkingOnly(t *testing.T) {
	parent := Meta{
		WorkItem: &WorkItemRef{Provider: "jira", Key: "PROJ-1"},
		Links: []WorkItemLink{
			{Provider: "github", Key: "o/r#2", Role: LinkWorking, State: "merged"},
			{Provider: "github", Key: "o/r#3", Role: LinkCreated},
			{Provider: "github", Key: "o/r#4", Role: LinkWatching},
		},
	}
	got := InheritedLinks(parent, "now")
	if len(got) != 2 || got[0].Key != "PROJ-1" || got[1].Key != "o/r#2" {
		t.Fatalf("InheritedLinks = %+v, want PROJ-1 and o/r#2", got)
	}
	// The child watches the PR as it was last seen: a merged one is history on its row too.
	if got[1].State != "merged" {
		t.Fatalf("inherited o/r#2 state=%q, want merged", got[1].State)
	}
	for _, l := range got {
		if l.Role != LinkWatching || l.At != "now" {
			t.Fatalf("inherited link %+v, want watching at now", l)
		}
	}
}

func TestLinksCompareKeysCaseInsensitivelyAndBoundTheirLength(t *testing.T) {
	var m Meta
	AddLink(&m, WorkItemLink{Provider: "github", Key: "O/R#45", Role: LinkWatching})
	if AddLink(&m, WorkItemLink{Provider: "github", Key: "o/r#45", Role: LinkWatching}) || len(m.Links) != 1 {
		t.Fatalf("O/R#45 and o/r#45 became two links: %+v", m.Links)
	}
	long := "o/" + strings.Repeat("r", 200) + "#1"
	if _, ok := NormalizeLinkKey("github", long, ""); ok {
		t.Fatal("a key longer than 200 bytes was accepted")
	}
}

func TestRemovingALinkIsRememberedPerProviderUntilLinkedAgain(t *testing.T) {
	m := Meta{Links: []WorkItemLink{
		{Provider: "github", Key: "o/r#7", Role: LinkWorking},
		{Provider: "bitbucket", Key: "o/r#7", Role: LinkWorking},
	}}
	if !RemoveLink(&m, "github", "O/R#7") || len(m.Links) != 1 || !IsUnlinked(m, "github", "o/r#7") {
		t.Fatalf("after remove: links=%+v unlinked=%v", m.Links, m.Unlinked)
	}
	// The same owner/repo#N on the other host is another item.
	if IsUnlinked(m, "bitbucket", "o/r#7") {
		t.Fatalf("unlinked=%v: removing GitHub's o/r#7 hid Bitbucket's", m.Unlinked)
	}
	if !RemoveLink(&m, "Bitbucket", "O/R#7") || len(m.Links) != 0 || !IsUnlinked(m, "bitbucket", "o/r#7") {
		t.Fatalf("after the Bitbucket remove: links=%+v unlinked=%v", m.Links, m.Unlinked)
	}
	// Removing what is not linked still records it: the branch's PR may only be linked later.
	if !RemoveLink(&m, "github", "o/r#8") || !IsUnlinked(m, "github", "o/r#8") {
		t.Fatalf("unlinked=%v, want o/r#8 remembered", m.Unlinked)
	}
	// A GitHub key stays bare, as metas written before other providers were recorded hold it.
	if m.Unlinked[0] != "O/R#7" || m.Unlinked[1] != "bitbucket:O/R#7" {
		t.Fatalf("unlinked=%v, want a bare GitHub key and a prefixed Bitbucket one", m.Unlinked)
	}
	if !ForgetUnlinked(&m, "github", "o/r#7") || IsUnlinked(m, "github", "o/r#7") || !IsUnlinked(m, "bitbucket", "o/r#7") {
		t.Fatalf("unlinked=%v, want only GitHub's o/r#7 forgotten", m.Unlinked)
	}
}

func TestRemovingTheLaunchItemChangesNothing(t *testing.T) {
	m := Meta{WorkItem: &WorkItemRef{Provider: "github", Key: "o/r#45"}}
	if RemoveLink(&m, "GitHub", "O/R#45") || len(m.Unlinked) != 0 {
		t.Fatalf("removing the launch item reported a change: unlinked=%v", m.Unlinked)
	}
}

func TestSetLinkStateRecordsOnlyAKnownStateOnAHeldLink(t *testing.T) {
	m := Meta{WorkItem: &WorkItemRef{Provider: "github", Key: "o/r#1"},
		Links: []WorkItemLink{{Provider: "github", Key: "o/r#2", Role: LinkWorking}}}
	if !SetLinkState(&m, "GitHub", "O/R#2", "merged") || m.Links[0].State != "merged" {
		t.Fatalf("links=%+v, want o/r#2 merged", m.Links)
	}
	if SetLinkState(&m, "github", "o/r#2", "merged") {
		t.Fatal("the same state reported a change")
	}
	if SetLinkState(&m, "github", "o/r#2", "weird") || m.Links[0].State != "merged" {
		t.Fatalf("an unknown state was recorded: %+v", m.Links)
	}
	if SetLinkState(&m, "github", "o/r#9", "closed") {
		t.Fatal("a state was recorded for an unheld item")
	}
	// The launch item's state goes to the meta, and shows on its started link.
	if !SetLinkState(&m, "github", "O/R#1", "merged") || m.WorkItemState != "merged" || SetLinkState(&m, "github", "o/r#1", "merged") {
		t.Fatalf("workItemState=%q, want merged recorded once", m.WorkItemState)
	}
	if l := LinksOf(m); l[0].Role != LinkStarted || l[0].State != "merged" {
		t.Fatalf("launch link = %+v, want started and merged", l[0])
	}
}

func TestPRsAllMergedCountsOnlyTheSessionsOwnPRs(t *testing.T) {
	pr := func(key, role, state string) WorkItemLink {
		return WorkItemLink{Provider: "github", Key: key, Role: role, State: state}
	}
	for _, c := range []struct {
		name   string
		m      Meta
		merged bool
	}{
		{"one merged", Meta{Links: []WorkItemLink{pr("o/r#1", LinkWorking, "merged")}}, true},
		{"merged and closed", Meta{Links: []WorkItemLink{pr("o/r#1", LinkWorking, "merged"), pr("o/r#2", LinkCreated, "closed")}}, true},
		{"one still open", Meta{Links: []WorkItemLink{pr("o/r#1", LinkWorking, "merged"), pr("o/r#2", LinkWorking, "open")}}, false},
		{"a draft", Meta{Links: []WorkItemLink{pr("o/r#1", LinkWorking, "merged"), pr("o/r#2", LinkWorking, "draft")}}, false},
		{"only closed", Meta{Links: []WorkItemLink{pr("o/r#2", LinkWorking, "closed")}}, false},
		{"an open PR it only watches", Meta{Links: []WorkItemLink{pr("o/r#1", LinkWorking, "merged"), pr("o/r#2", LinkWatching, "open")}}, true},
		{"merged PR it only reviews", Meta{Links: []WorkItemLink{pr("o/r#2", LinkReviewing, "merged")}}, false},
		{"issues without state", Meta{Links: []WorkItemLink{{Provider: "jira", Key: "P-1", Role: LinkWorking}, pr("o/r#1", LinkWorking, "merged")}}, true},
		{"launch PR merged", Meta{WorkItem: &WorkItemRef{Provider: "github", Key: "o/r#5"}, WorkItemState: "merged"}, true},
		{"no PR", Meta{}, false},
		{"a PR linked before states were", Meta{Links: []WorkItemLink{pr("o/r#1", LinkWorking, "merged"),
			{Provider: "github", Key: "o/r#2", Role: LinkWorking, URL: "https://github.com/o/r/pull/2"}}}, false},
		{"an issue with a URL", Meta{Links: []WorkItemLink{pr("o/r#1", LinkWorking, "merged"),
			{Provider: "github", Key: "o/r#3", Role: LinkWorking, URL: "https://github.com/o/r/issues/3"}}}, true},
		{"a Bitbucket PR without state", Meta{Links: []WorkItemLink{pr("o/r#1", LinkWorking, "merged"),
			{Provider: "bitbucket", Key: "w/r#4", Role: LinkCreated, URL: "https://bitbucket.org/w/r/pull-requests/4"}}}, false},
	} {
		if got := PRsAllMerged(c.m); got != c.merged {
			t.Errorf("%s: PRsAllMerged = %v, want %v", c.name, got, c.merged)
		}
	}
}

func TestPRFactsDueOnlyForANewerOrChangedObservation(t *testing.T) {
	at := func(min int) string {
		return time.Date(2026, 11, 27, 10, min, 0, 0, time.UTC).Format(time.RFC3339)
	}
	held := &PRFacts{Checks: "pending", ObservedAt: at(0)}
	for _, tc := range []struct {
		name string
		held *PRFacts
		f    PRFacts
		want bool
	}{
		{"first observation", nil, PRFacts{Checks: "pending", ObservedAt: at(0)}, true},
		{"same facts, read again soon", held, PRFacts{Checks: "pending", ObservedAt: at(2)}, false},
		{"same facts, refresh due", held, PRFacts{Checks: "pending", ObservedAt: at(10)}, true},
		{"checks moved", held, PRFacts{Checks: "success", ObservedAt: at(2)}, true},
		{"review moved", held, PRFacts{Checks: "pending", ChangesRequested: true, ObservedAt: at(2)}, true},
		{"conflict moved", held, PRFacts{Checks: "pending", Conflict: true, ObservedAt: at(2)}, true},
		{"ready moved", held, PRFacts{Checks: "pending", Ready: true, ObservedAt: at(2)}, true},
		{"same read time, other facts", held, PRFacts{Checks: "success", ObservedAt: at(0)}, false},
		{"older read never wins", &PRFacts{Checks: "success", ObservedAt: at(5)}, PRFacts{Checks: "pending", ObservedAt: at(4)}, false},
		{"unknown checks value", nil, PRFacts{Checks: "green", ObservedAt: at(0)}, false},
		{"no observation time", nil, PRFacts{Checks: "success"}, false},
		{"held time unreadable", &PRFacts{Checks: "pending", ObservedAt: "x"}, PRFacts{Checks: "pending", ObservedAt: at(0)}, true},
	} {
		if got := PRFactsDue(tc.held, tc.f); got != tc.want {
			t.Errorf("%s: PRFactsDue=%v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestRecordPRReadRecordsOnTheHeldLinkOrTheLaunchItem(t *testing.T) {
	now := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC).Format(time.RFC3339)
	m := Meta{WorkItem: &WorkItemRef{Provider: "github", Key: "o/r#1"},
		Links: []WorkItemLink{{Provider: "github", Key: "o/r#2", Role: LinkWorking, State: "open"}}}
	f := PRFacts{Checks: "success", Ready: true, ObservedAt: now}
	if !RecordPRRead(&m, "github", "O/R#2", "open", &f) || m.Links[0].Facts == nil || !m.Links[0].Facts.Ready {
		t.Fatalf("links=%+v, want o/r#2's facts recorded", m.Links)
	}
	if RecordPRRead(&m, "github", "o/r#2", "open", &f) {
		t.Fatal("the same observation was recorded twice")
	}
	if !RecordPRRead(&m, "github", "o/r#1", "open", &PRFacts{Checks: "failure", ObservedAt: now}) || m.WorkItemFacts == nil ||
		m.WorkItemFacts.Checks != "failure" || m.WorkItemState != "open" {
		t.Fatalf("workItem state=%q facts=%+v, want the launch PR's read on the meta", m.WorkItemState, m.WorkItemFacts)
	}
	if l := LinksOf(m); l[0].Facts == nil || l[0].Facts.Checks != "failure" || l[1].Facts == nil || l[1].Facts.Checks != "success" {
		t.Fatalf("LinksOf=%+v, want each link with its own facts", l)
	}
	if RecordPRRead(&m, "github", "o/r#3", "open", &f) {
		t.Fatal("a read was recorded for an item the session does not hold")
	}
	// A read with no time records its state alone.
	if !RecordPRRead(&m, "github", "o/r#2", "draft", nil) || m.Links[0].State != "draft" || m.Links[0].Facts.ObservedAt != now {
		t.Fatalf("link=%+v, want the state recorded and the facts kept", m.Links[0])
	}
	// Replaced, never edited in place: a child holding the same pointer keeps its own copy.
	child := InheritedLinks(Meta{Links: m.Links}, now)
	later := time.Date(2026, 11, 27, 10, 1, 0, 0, time.UTC).Format(time.RFC3339)
	RecordPRRead(&m, "github", "o/r#2", "draft", &PRFacts{Checks: "failure", ObservedAt: later})
	if child[0].Facts == nil || child[0].Facts.Checks != "success" {
		t.Fatalf("inherited facts=%+v, want the parent's later observation not to reach the child", child[0].Facts)
	}
}

// State and facts are one observation: a late answer from an older call writes neither, and a
// read that moves the state renews the facts even inside PRFactsRefresh.
func TestRecordPRReadKeepsStateAndFactsFromOneRead(t *testing.T) {
	at := func(ms int) string {
		return time.Date(2026, 11, 27, 10, 0, 0, ms*int(time.Millisecond), time.UTC).Format(time.RFC3339Nano)
	}
	m := Meta{Links: []WorkItemLink{{Provider: "github", Key: "o/r#2", Role: LinkCreated}}}
	// Reopened and green, read at .700; the read sent at .100, while it was still closed, arrives later.
	if !RecordPRRead(&m, "github", "o/r#2", "open", &PRFacts{Checks: "success", Ready: true, ObservedAt: at(700)}) {
		t.Fatal("the first read was not recorded")
	}
	if RecordPRRead(&m, "github", "o/r#2", "closed", &PRFacts{Checks: "pending", ObservedAt: at(100)}) ||
		m.Links[0].State != "open" || m.Links[0].Facts.Checks != "success" || !m.Links[0].Facts.Ready {
		t.Fatalf("link=%+v facts=%+v, want the older read to change nothing", m.Links[0], m.Links[0].Facts)
	}
	// open -> draft with the same four facts, a minute later: the facts carry the draft read's time.
	if !RecordPRRead(&m, "github", "o/r#2", "draft", &PRFacts{Checks: "success", Ready: true, ObservedAt: at(60_000)}) ||
		m.Links[0].State != "draft" || m.Links[0].Facts.ObservedAt != at(60_000) {
		t.Fatalf("link=%+v facts=%+v, want the draft state labelled with its own read", m.Links[0], m.Links[0].Facts)
	}
	if !PRReadStale(&PRFacts{ObservedAt: at(700)}, &PRFacts{ObservedAt: at(100)}) || PRReadStale(&PRFacts{ObservedAt: at(100)}, &PRFacts{ObservedAt: at(100)}) ||
		PRReadStale(nil, &PRFacts{ObservedAt: at(100)}) || PRReadStale(&PRFacts{ObservedAt: at(100)}, nil) {
		t.Fatal("PRReadStale: want only a strictly older read stale")
	}
}

func TestCarryWorkItemsTakesTheItemsWithTheirStatesAndFacts(t *testing.T) {
	src := Meta{WorkItem: &WorkItemRef{Provider: "github", Key: "o/r#1"}, WorkItemState: "merged",
		WorkItemFacts: &PRFacts{Checks: "success", ObservedAt: "2026-11-27T10:00:00Z"},
		Links:         []WorkItemLink{{Provider: "github", Key: "o/r#2", Role: LinkCreated, State: "open", Facts: &PRFacts{Conflict: true, ObservedAt: "2026-11-27T10:00:00Z"}}},
		Unlinked:      []string{"o/r#9"}}
	var dst Meta
	CarryWorkItems(&dst, src)
	l := LinksOf(dst)
	if len(l) != 2 || l[0].State != "merged" || l[0].Facts == nil || l[0].Facts.Checks != "success" ||
		l[1].Facts == nil || !l[1].Facts.Conflict || len(dst.Unlinked) != 1 {
		t.Fatalf("carried links=%+v unlinked=%v, want the source's items with states and facts", l, dst.Unlinked)
	}
}
