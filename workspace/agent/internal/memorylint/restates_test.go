package memorylint

import (
	"errors"
	"strings"
	"testing"
)

const restateGuide = "# Release\n\n" +
	"Run `scripts/release.sh` from the repository root after the changelog is merged; it tags the\n" +
	"commit, builds the images for both architectures and pushes them to the registry. Never run it\n" +
	"from a worktree: the tag must be made on the default branch.\n\n" +
	"## リリース手順\n\nリリースはリポジトリの最上位で scripts/release.sh を実行する。作業ツリーからは実行しない。タグは既定ブランチで作る。\n"

var restateRepo = fileRepo{
	fakeRepo: fakeRepo{paths: []string{"docs/release.md", "AGENTS.md", "scripts/release.sh", "docs/big.md"}},
	files: map[string]string{
		"docs/release.md":    restateGuide,
		"AGENTS.md":          "# Agents\n\nNothing about releases here at all, only build rules for the console.\n",
		"scripts/release.sh": "#!/bin/sh\n# Run scripts/release.sh from the repository root after the changelog is merged; it tags the commit\n",
		"docs/big.md":        strings.Repeat("x", restateMaxDocBytes+1),
	},
}

// A memory most of whose text an anchored document holds is reported against that document, in
// either script and whatever its wrapping or Markdown; a memory that adds to it, one too short to
// tell, one anchoring a script or an unanchored document, is not.
func TestRestatesAnchoredDocument(t *testing.T) {
	mems := []Memory{
		// Rewrapped, with other punctuation, path in the body.
		{Name: "en", Body: "See docs/release.md. Run scripts/release.sh from the repository root after the changelog is merged;\n" +
			"it tags the commit, builds the images for both architectures and pushes them to the registry."},
		// Japanese, anchored only by a stored anchor.
		{Name: "ja", Body: "リリースはリポジトリの最上位で scripts/release.sh を実行する。作業ツリーからは実行しない。タグは既定ブランチで作る。",
			Anchors: []string{"docs/release.md"}},
		// Same subject, but most of it is not in the document.
		{Name: "adds", Body: "See docs/release.md. Before the release, check the dashboard for open incidents, freeze merges in the\n" +
			"chat channel and ask the on-call engineer to watch the rollout metrics for an hour afterwards."},
		{Name: "short", Body: "docs/release.md: Run scripts/release.sh."},
		// The script holds the sentence, but a script is not a document.
		{Name: "script", Body: "Run scripts/release.sh from the repository root after the changelog is merged; it tags the commit"},
		// A top-level document named by a stored anchor, which does not hold the text.
		{Name: "top", Body: restateGuide, Anchors: []string{"AGENTS.md"}},
	}
	rep := Lint(mems, Options{}, restateRepo)
	for name, want := range map[string][]string{"en": {"docs/release.md"}, "ja": {"docs/release.md"}} {
		if got := subjects(rep, name, CheckRestatesDoc); !sameSet(got, want) {
			t.Errorf("%s: restates = %v, want %v", name, got, want)
		}
	}
	for _, name := range []string{"adds", "short", "script", "top"} {
		if got := subjects(rep, name, CheckRestatesDoc); len(got) != 0 {
			t.Errorf("%s: restates = %v, want none", name, got)
		}
	}
	if rep.Counts[CheckRestatesDoc] != 2 {
		t.Fatalf("counts = %v", rep.Counts)
	}
	// A top-level document anchored by a stored anchor is compared too.
	rep = Lint([]Memory{{Name: "top", Body: "Nothing about releases here at all, only build rules for the console.", Anchors: []string{"./AGENTS.md"}}}, Options{}, restateRepo)
	if got := subjects(rep, "top", CheckRestatesDoc); !sameSet(got, []string{"AGENTS.md"}) {
		t.Fatalf("top-level anchor: %v", got)
	}
}

// A document that cannot be read, or is too large, is not compared, and the report says so: an
// empty result must not read as "nothing is redundant".
func TestRestatesSaysWhatWasNotRead(t *testing.T) {
	rep := Lint([]Memory{{Name: "big", Body: restateGuide, Anchors: []string{"docs/big.md"}}}, Options{}, restateRepo)
	if len(subjects(rep, "big", CheckRestatesDoc)) != 0 || !strings.Contains(strings.Join(rep.Skipped, "\n"), "redundancy check: 1 documents were not read") {
		t.Fatalf("too large: %+v", rep)
	}
	broken := restateRepo
	broken.err = errors.New("boom")
	rep = Lint([]Memory{{Name: "en", Body: "docs/release.md " + restateGuide}}, Options{}, broken)
	if len(subjects(rep, "en", CheckRestatesDoc)) != 0 || !strings.Contains(strings.Join(rep.Skipped, "\n"), "redundancy check") {
		t.Fatalf("unreadable: %+v", rep)
	}
	// Without a FileReader the check does not run at all.
	rep = Lint([]Memory{{Name: "en", Body: "docs/release.md " + restateGuide}}, Options{}, restateRepo.fakeRepo)
	if rep.Counts[CheckRestatesDoc] != 0 {
		t.Fatalf("no reader: %+v", rep)
	}
}
