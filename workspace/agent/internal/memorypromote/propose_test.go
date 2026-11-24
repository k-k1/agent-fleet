package memorypromote

import (
	"strings"
	"testing"
)

func TestHeadingsSkipFencesAndStripClosers(t *testing.T) {
	doc := "# Title #\n\ntext\n\n```sh\n# not a heading\n```\n\n~~~~\n## nor this\n~~~\n## still fenced\n~~~~\n\n## Real ##\n####### seven\n#nospace\n   ### Indented\n    #### code block\n"
	var got []string
	for _, h := range Headings([]byte(doc)) {
		got = append(got, strings.Repeat("#", h.Level)+" "+h.Text)
	}
	want := "# Title|## Real|### Indented"
	if strings.Join(got, "|") != want {
		t.Fatalf("got %q, want %q", strings.Join(got, "|"), want)
	}
	// A backtick line whose info string has a backtick is not a fence.
	if hs := Headings([]byte("``` a`b\n# Heading\n")); len(hs) != 1 {
		t.Fatalf("%+v", hs)
	}
}

func TestInsertAtTheEndOfTheSectionOrANewOne(t *testing.T) {
	doc := "# Doc\n\n## Build\n\nRun make.\n\n### Sub\n\ns\n\n\n## Other\n\nx\n"
	out, ok := Insert([]byte(doc), "Build", "\nNew text.\n\n")
	want := "# Doc\n\n## Build\n\nRun make.\n\n### Sub\n\ns\n\nNew text.\n\n## Other\n\nx\n"
	if !ok || string(out) != want {
		t.Fatalf("existing section:\n%q\nwant\n%q", out, want)
	}
	// The last section runs to the end of the document.
	out, ok = Insert([]byte(doc), "Other", "New.")
	if !ok || string(out) != "# Doc\n\n## Build\n\nRun make.\n\n### Sub\n\ns\n\n\n## Other\n\nx\n\nNew.\n" {
		t.Fatalf("last section: %q", out)
	}
	// An empty section.
	out, ok = Insert([]byte("## A\n## B\n"), "A", "t")
	if !ok || string(out) != "## A\n\nt\n\n## B\n" {
		t.Fatalf("empty section: %q", out)
	}
	// A heading only inside a fence does not count.
	out, ok = Insert([]byte("```\n## Build\n```\n"), "Build", "t")
	if ok || string(out) != "```\n## Build\n```\n\n## Build\n\nt\n" {
		t.Fatalf("fenced: %q", out)
	}
	// A new section at the end; an empty or missing document gets just the section.
	if out, ok = Insert(nil, "Release", "t"); ok || string(out) != "## Release\n\nt\n" {
		t.Fatalf("new file: %q", out)
	}
}

func TestDefaultSection(t *testing.T) {
	for _, c := range []struct{ desc, name, want string }{
		{"Release steps. Then more.", "n", "Release steps"},
		{"リリース手順。詳細は後で", "n", "リリース手順"},
		{"Build trap — never do X", "n", "Build trap"},
		{"", "the-name", "the-name"},
		{"a\tb", "n", "a b"},
		{strings.Repeat("x", 100), "n", strings.Repeat("x", 80)},
	} {
		if got := DefaultSection(c.desc, c.name); got != c.want {
			t.Errorf("DefaultSection(%q) = %q, want %q", c.desc, got, c.want)
		}
	}
}

func TestLanguageDiffersAndMemoryLinks(t *testing.T) {
	en := []byte(strings.Repeat("The repository documents are written in English. ", 10))
	ja := "このリポジトリではビルドの前に必ずスクリプトを実行すること。理由は記憶に残す。"
	if !LanguageDiffers(ja, en) {
		t.Error("Japanese text into an English document is not flagged")
	}
	if LanguageDiffers("Run the release script before the build, every time.", en) {
		t.Error("English into English is flagged")
	}
	if LanguageDiffers(ja, nil) || LanguageDiffers(ja, []byte("# Short\n")) {
		t.Error("a new or short document is judged")
	}
	if !HasMemoryLinks("see [[other-note]]") || HasMemoryLinks("see [link](x) and [x]") {
		t.Error("memory links")
	}
}
