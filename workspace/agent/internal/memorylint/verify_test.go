package memorylint

import (
	"errors"
	"reflect"
	"testing"
)

// Each rule accepts only what it finds exactly; the anchors lint lets pass without a check (a
// root-level file, a command, an ignored path, an ADR without an ADR directory) are unverified.
func TestVerifyAnchorsNeedsAPositiveMatch(t *testing.T) {
	repo := fakeRepo{
		paths:   []string{"README.md", "scripts/release.sh", "docs/decisions/0130-cultivated-memory.md", ".gitignore", "docs/v1..v2.md"},
		symbols: map[string]bool{"agentMemSave": true, "TestRealOne": true},
	}
	mems := []Memory{
		{Name: "ok", Anchors: []string{"README.md", "./scripts/release.sh", "scripts/", "docs/decisions", "[[other]]", "ADR 0130", "agentMemSave", "`agentMemSave()`", "TestRealOne"}},
		{Name: "bad", Anchors: []string{"GONE.md", "scripts/gone.sh", "build/", "[[ghost]]", "ADR 4242", "agentMemGone", "TestGoneAway", "make release", "  ", "/etc/passwd", "../x", "plain",
			// A real test name inside a command or a missing file's name is not that symbol.
			"go test -run TestRealOne", "TestRealOne.go", "`agentMemSave` twice", "docs/../README.md"}},
		{Name: "dots", Anchors: []string{"docs/v1..v2.md", "pkg.agentMemSave"}},
		{Name: "other"},
	}
	got, err := VerifyAnchors(mems, Options{}, repo)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{"bad": {"GONE.md", "scripts/gone.sh", "build/", "[[ghost]]", "ADR 4242", "make release", "  ", "/etc/passwd", "../x", "plain", "go test -run TestRealOne", "TestRealOne.go", "`agentMemSave` twice", "docs/../README.md", "agentMemGone", "TestGoneAway"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("unverified = %#v\nwant %#v", got, want)
	}

	// Without an ADR directory an ADR number cannot be found.
	got, err = VerifyAnchors([]Memory{{Name: "m", Anchors: []string{"ADR 130"}}}, Options{}, fakeRepo{paths: []string{"README.md"}})
	if err != nil || !reflect.DeepEqual(got, map[string][]string{"m": {"ADR 130"}}) {
		t.Errorf("no ADR dir: %v %v", got, err)
	}
}

func TestVerifyAnchorsFailsWhenTheRevisionCannotBeRead(t *testing.T) {
	mems := []Memory{{Name: "m", Anchors: []string{"README.md", "agentMemSave"}}}
	if _, err := VerifyAnchors(mems, Options{}, fakeRepo{pathErr: errors.New("ls-tree failed")}); err == nil {
		t.Error("a failed file list verified anchors")
	}
	if _, err := VerifyAnchors(mems, Options{}, fakeRepo{paths: []string{"README.md"}, symErr: errors.New("grep failed")}); err == nil {
		t.Error("a failed symbol search verified anchors")
	}
	// No symbol to search: the search is not needed and cannot fail the run.
	if got, err := VerifyAnchors([]Memory{{Name: "m", Anchors: []string{"README.md"}}}, Options{}, fakeRepo{paths: []string{"README.md"}, symErr: errors.New("x")}); err != nil || len(got) != 0 {
		t.Errorf("paths only: %v %v", got, err)
	}
}
