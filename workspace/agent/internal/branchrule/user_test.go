package branchrule

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestUserStore(t *testing.T) {
	isolateGit(t)
	path := filepath.Join(t.TempDir(), "branch-rules-user.json")
	if u := ReadUser(path); u.Rules == nil || len(u.Rules) != 0 {
		t.Fatalf("missing store = %+v", u)
	}
	err := WriteUser(path, UserRules{Rules: []Rule{{Match: "*", Name: str("{ref}")}}})
	if !errors.Is(err, ErrBareStarName) {
		t.Fatalf("bare * name: err = %v", err)
	}
	for _, bad := range []Rule{
		{Match: ""},
		{Match: "github.com/a*/b"},
		{Match: "*", Base: str("a..b")},
		{Match: "*", Types: map[string]KindRule{"feat": {}}},
		{Match: "*", Types: map[string]KindRule{"bugfix": {Prefix: str("-x/")}}},
	} {
		if err := WriteUser(path, UserRules{Rules: []Rule{bad}}); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	ok := UserRules{Rules: []Rule{
		{Match: "*", Base: str("develop"), Types: map[string]KindRule{"bugfix": {Prefix: str("fix/"), From: []string{"Defect"}}}},
		{Match: "bitbucket.org/acme/*", Name: str("{prefix}{key}")},
	}}
	if err := WriteUser(path, ok); err != nil {
		t.Fatal(err)
	}
	got := ReadUser(path)
	if len(got.Rules) != 2 || *got.Rules[1].Name != "{prefix}{key}" || got.Rules[0].Types["bugfix"].From[0] != "Defect" {
		t.Errorf("round trip = %+v", got)
	}
}

func TestTemplateIsTheUsersStarRule(t *testing.T) {
	stored := []Rule{{Match: "bitbucket.org/acme/*", Name: str("{prefix}{key}")}}
	layers := []Layer{UserLayer(stored, "feature/{key}"), Builtin()}
	item := &Item{Key: "acme/web#45", Title: "Fix the empty list"}
	if got := Name(layers, "github.com/acme/web", Request{Item: item}); got.Name != "feature/issue-45" {
		t.Errorf("template: %q", got.Name)
	}
	if got := Name(layers, "bitbucket.org/acme/web", Request{Item: &Item{Key: "PROJ-1"}}); got.Name != "feature/PROJ-1" {
		t.Errorf("scoped rule beats the template: %q", got.Name)
	}
	// An empty template gets the new built-in with no compatibility shim.
	if got := Name([]Layer{UserLayer(nil, " "), Builtin()}, "", Request{Item: item}); got.Name != "feature/45-fix-the-empty-list" {
		t.Errorf("empty template: %q", got.Name)
	}
}
