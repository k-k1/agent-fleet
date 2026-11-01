package session

import (
	"strings"
	"testing"
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
	if SetLinkState(&m, "github", "o/r#1", "closed") || SetLinkState(&m, "github", "o/r#9", "closed") {
		t.Fatal("a state was recorded for the launch item or an unheld item")
	}
}
