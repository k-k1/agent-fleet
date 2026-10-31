package session

import "testing"

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
			{Provider: "github", Key: "o/r#2", Role: LinkWorking},
			{Provider: "github", Key: "o/r#3", Role: LinkCreated},
			{Provider: "github", Key: "o/r#4", Role: LinkWatching},
		},
	}
	got := InheritedLinks(parent, "now")
	if len(got) != 2 || got[0].Key != "PROJ-1" || got[1].Key != "o/r#2" {
		t.Fatalf("InheritedLinks = %+v, want PROJ-1 and o/r#2", got)
	}
	for _, l := range got {
		if l.Role != LinkWatching || l.At != "now" {
			t.Fatalf("inherited link %+v, want watching at now", l)
		}
	}
}
