package branchrule

import (
	"testing"
)

func TestRepoID(t *testing.T) {
	cases := map[string]string{
		"git@bitbucket.org:Acme/Web.git":            "bitbucket.org/acme/web",
		"https://github.com/k-k1/agent-fleet.git":   "github.com/k-k1/agent-fleet",
		"https://user@github.com/k-k1/agent-fleet/": "github.com/k-k1/agent-fleet",
		"ssh://git@gitlab.example:2222/g/sub/r.git": "gitlab.example/g/sub/r",
		"/srv/git/local.git":                        "",
		"":                                          "",
	}
	for in, want := range cases {
		if got := RepoID(in); got != want {
			t.Errorf("RepoID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMatchSpecificityAndTies(t *testing.T) {
	id := "bitbucket.org/acme/web"
	user := Layer{Name: "user", Rules: []Rule{
		{Match: "*", Base: str("star")},
		{Match: "bitbucket.org/acme/*", Base: str("owner"), Source: "first"},
		{Match: "bitbucket.org/acme/*", Base: str("owner-second")},
		{Match: "bitbucket.org/acme/web", Name: str("{ref}")},
		{Match: "github.com/acme/web", Base: str("other-host")},
		{Match: "bitbucket.org/*", Base: str("wrong-segment-count")},
	}}
	e := Effect([]Layer{user, Builtin()}, id)
	if e.Base != "owner" {
		t.Errorf("base = %q, want the first of the two owner/* rules", e.Base)
	}
	// The exact rule sets only name and the owner rule only base: both apply (per field).
	if e.Name != "{ref}" {
		t.Errorf("name = %q, want the exact rule's", e.Name)
	}
	if e.Sources["base"] != "user: first" {
		t.Errorf("sources.base = %q", e.Sources["base"])
	}
}

func TestPlaceholders(t *testing.T) {
	gh := placeholders(&Item{Key: "acme/web#1120", Title: "Work item PR status"})
	jira := placeholders(&Item{Key: "PROJ-123", Title: "ログイン後に一覧が空になる"})
	want := []struct{ got, want string }{
		{gh["ref"], "1120"}, {gh["num"], "1120"}, {gh["key"], "issue-1120"}, {gh["project"], ""},
		{gh["slug"], "work-item-pr-status"},
		{jira["ref"], "PROJ-123"}, {jira["num"], "123"}, {jira["key"], "PROJ-123"}, {jira["project"], "PROJ"},
		{jira["slug"], ""},
	}
	for i, w := range want {
		if w.got != w.want {
			t.Errorf("#%d = %q, want %q", i, w.got, w.want)
		}
	}
}

func TestBuiltinNames(t *testing.T) {
	layers := []Layer{Builtin()}
	cases := []struct {
		item *Item
		want string
	}{
		{&Item{Key: "k-k1/agent-fleet#1113", Title: "Work item PR status"}, "feature/1113-work-item-pr-status"},
		{&Item{Key: "k-k1/agent-fleet#1120", Title: "Branch naming", Labels: []string{"enhancement", "Bug"}}, "fix/1120-branch-naming"},
		{&Item{Key: "PROJ-123", Title: "ログイン後に一覧が空になる"}, "feature/PROJ-123"},
	}
	for _, c := range cases {
		if got := Name(layers, "github.com/k-k1/agent-fleet", Request{Item: c.item}); got.Name != c.want || got.Base != "head" {
			t.Errorf("%s: got %q base %q, want %q base head", c.item.Key, got.Name, got.Base, c.want)
		}
	}
}

func TestKindFromTypeThenLabelsThenFrom(t *testing.T) {
	user := Layer{Name: "user", Rules: []Rule{{Match: "*", Types: map[string]KindRule{
		"hotfix": {From: []string{"Incident"}},
	}}}}
	layers := []Layer{user, Builtin()}
	cases := []struct {
		item Item
		kind string
	}{
		{Item{Key: "X-1", Type: "incident"}, "hotfix"},                          // from, case-insensitive
		{Item{Key: "X-1", Type: "Bug", Labels: []string{"incident"}}, "bugfix"}, // tracker type first
		{Item{Key: "X-1", Type: "Story", Labels: []string{"docs"}}, "docs"},     // an unmapped type falls to labels
		{Item{Key: "X-1", Labels: []string{"question"}}, "feature"},
	}
	for _, c := range cases {
		if got := Name(layers, "", Request{Item: &c.item}); got.Kind != c.kind {
			t.Errorf("%+v: kind %q, want %q", c.item, got.Kind, c.kind)
		}
	}
	if got := Name(layers, "", Request{Item: &Item{Key: "X-1", Type: "Bug"}, Kind: "chore"}); got.Kind != "chore" {
		t.Errorf("explicit kind lost: %q", got.Kind)
	}
	got := Name(layers, "", Request{Kind: "feat"})
	if got.Kind != "feature" || !hasWarning(got.Warnings, "unknown_kind", "feat") {
		t.Errorf("unknown explicit kind: %+v", got)
	}
}

func TestPrefixOnlyCheckedBeforeSanitising(t *testing.T) {
	user := Layer{Name: "user", Rules: []Rule{{Match: "*", Source: "t", Name: str("{prefix}{key}")}}}
	layers := []Layer{user, Builtin()}
	if got := Name(layers, "", Request{Slug: "Tidy up README"}); got.Name != "feature/tidy-up-readme" || got.NameEmpty {
		t.Errorf("slug appended: %+v", got)
	}
	// Sanitising alone would have produced a bare "feature".
	if got := Name(layers, "", Request{}); !got.NameEmpty || got.Name != "" {
		t.Errorf("no item, no slug: want name_empty, got %+v", got)
	}
	if got := Name([]Layer{Builtin()}, "", Request{Kind: "bugfix", Slug: "crash on start"}); got.Name != "fix/crash-on-start" {
		t.Errorf("temp-session rename renders {prefix}<slug>: %q", got.Name)
	}
}

func TestUnknownPlaceholderWarns(t *testing.T) {
	user := Layer{Name: "user", Rules: []Rule{{Match: "*", Name: str("{prefix}{ticket}-{slug}")}}}
	got := Name([]Layer{user, Builtin()}, "", Request{Slug: "x"})
	if got.Name != "feature/x" || !hasWarning(got.Warnings, "unknown_placeholder", "{ticket}") {
		t.Errorf("%+v", got)
	}
}

func TestBaseKindBeforeRuleWithinALayerAndLayersFirst(t *testing.T) {
	repo := Layer{Name: "repository", Repository: true, Rules: []Rule{
		{Source: FilePath, Base: str("develop")},
	}}
	user := Layer{Name: "user", Rules: []Rule{{Match: "*", Types: map[string]KindRule{"bugfix": {Base: str("main")}}}}}
	// decision 5's example: the repository's naming.base beats a user kind base.
	if got := Name([]Layer{repo, user, Builtin()}, "", Request{Kind: "bugfix", Slug: "x"}); got.Base != "develop" {
		t.Errorf("base %q, want develop", got.Base)
	}
	// Inside one layer the kind base comes first, even from a weaker source.
	repo.Rules = append(repo.Rules, Rule{Source: "git config", Types: map[string]KindRule{"hotfix": {Base: str("main")}}})
	if got := Name([]Layer{repo, Builtin()}, "", Request{Kind: "hotfix", Slug: "x"}); got.Base != "main" {
		t.Errorf("hotfix base %q, want main", got.Base)
	}
	if got := Name([]Layer{Builtin()}, "", Request{}); got.Base != "head" {
		t.Errorf("builtin base %q", got.Base)
	}
}

func TestKindSetBlocksWeakerLayers(t *testing.T) {
	repo := Layer{Name: "repository", Repository: true, Rules: []Rule{{
		Source:   GitflowFile,
		Declares: []string{"feature", "release", "hotfix"},
		Types:    map[string]KindRule{"feature": {Prefix: str("feature/")}},
	}}}
	user := Layer{Name: "user", Rules: []Rule{{Match: "*", Name: str("{prefix}{key}"), Types: map[string]KindRule{"bugfix": {Prefix: str("fix/")}}}}}
	got := Name([]Layer{repo, user, Builtin()}, "", Request{Item: &Item{Key: "PROJ-7", Type: "Bug"}})
	if got.Kind != "feature" || got.Name != "feature/PROJ-7" {
		t.Errorf("got %+v, want feature/PROJ-7", got)
	}
	e := Effect([]Layer{repo, user, Builtin()}, "")
	if len(e.Kinds) != 3 || e.Kinds[1].Kind != "hotfix" || e.Kinds[1].Prefix != "hotfix/" {
		t.Errorf("kinds = %+v; weaker layers still fill kinds inside the set", e.Kinds)
	}
}

func TestSanitize(t *testing.T) {
	cases := map[string]string{
		"feature/issue-45-":  "feature/issue-45",
		"feature//x":         "feature/x",
		"feat ure/a..b.lock": "feat-ure/a.b",
		"feature/":           "feature",
		"/-./":               "",
	}
	for in, want := range cases {
		if got := Sanitize(in); got != want {
			t.Errorf("Sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCheckPrefix(t *testing.T) {
	kinds := []KindView{{Kind: "feature", Prefix: "feature/"}, {Kind: "bugfix", Prefix: "fix/"}}
	if w := CheckPrefix("temp/abc", kinds); w != nil {
		t.Errorf("temp/ is exempt: %v", w)
	}
	if w := CheckPrefix("fix/1-x", kinds); w != nil {
		t.Errorf("fix/ matches: %v", w)
	}
	if w := CheckPrefix("feat/1-x", kinds); len(w) != 1 || w[0].Code != "prefix_mismatch" {
		t.Errorf("feat/ should warn: %v", w)
	}
	if w := CheckPrefix("anything", append(kinds, KindView{Kind: "support", Prefix: ""})); w != nil {
		t.Errorf("an empty prefix accepts every name: %v", w)
	}
}
