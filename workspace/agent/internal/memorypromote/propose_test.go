package memorypromote

import (
	"errors"
	"strings"
	"testing"
)

func headingList(doc string) string {
	var got []string
	for _, h := range Headings([]byte(doc)) {
		got = append(got, strings.Repeat("#", h.Level)+" "+h.Text)
	}
	return strings.Join(got, "|")
}

// Headings are what CommonMark reads: nothing inside a fence (also one whose would-be closer has
// text after it), an HTML comment, an indented code block or an unclosed fence; closing # run
// stripped; a setext heading counts.
func TestHeadingsFollowCommonMark(t *testing.T) {
	doc := "# Title #\n\n```sh\n# not a heading\n``` `\n## nor this\n```\n\n<!--\n## in a comment\n-->\n\n" +
		"## Real ##  \nSetext\n===\n   ### Indented\n    #### code block\n\n```\n## unclosed\n"
	if got, want := headingList(doc+"\n- ## in a list\n\n> ## in a quote\n"), "# Title|## Real|# Setext|### Indented"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got := headingList("## CRLF\r\nx\r\n"); got != "## CRLF" {
		t.Fatalf("crlf: %q", got)
	}
}

func TestInsertAtTheEndOfTheSectionOrANewOne(t *testing.T) {
	doc := "# Doc\n\n## Build\n\nRun make.\n\n### Sub\n\ns\n\n\n## Other\n\nx\n"
	out, ok, err := Insert([]byte(doc), "Build", "\nNew text.\n\n")
	want := "# Doc\n\n## Build\n\nRun make.\n\n### Sub\n\ns\n\nNew text.\n\n## Other\n\nx\n"
	if err != nil || !ok || string(out) != want {
		t.Fatalf("existing section:\n%q\nwant\n%q (%v)", out, want, err)
	}
	// The last section runs to the end of the document.
	out, ok, _ = Insert([]byte(doc), "Other", "New.")
	if !ok || string(out) != "# Doc\n\n## Build\n\nRun make.\n\n### Sub\n\ns\n\n\n## Other\n\nx\n\nNew.\n" {
		t.Fatalf("last section: %q", out)
	}
	// An empty section.
	out, ok, _ = Insert([]byte("## A\n## B\n"), "A", "t")
	if !ok || string(out) != "## A\n\nt\n\n## B\n" {
		t.Fatalf("empty section: %q", out)
	}
	// A heading only inside a closed fence does not count: a new section is added.
	out, ok, err = Insert([]byte("```\n## Build\n```\n"), "Build", "t")
	if err != nil || ok || string(out) != "```\n## Build\n```\n\n## Build\n\nt\n" {
		t.Fatalf("fenced: %q %v", out, err)
	}
	// A document that ends inside an unclosed fence would swallow the new section.
	if _, _, err := Insert([]byte("x\n\n```\ncode\n"), "Build", "t"); !errors.Is(err, ErrNotPlaced) {
		t.Fatalf("unclosed fence: %v", err)
	}
	// The section's own last block is an unclosed fence: the text would be code.
	if _, _, err := Insert([]byte("## A\n\n```\ncode\n"), "A", "t"); !errors.Is(err, ErrNotPlaced) {
		t.Fatalf("section ends in an unclosed fence: %v", err)
	}
	// The text opens a fence it never closes: the sections after it would be swallowed.
	if _, _, err := Insert([]byte("## A\n\nx\n\n## B\n\ny\n"), "A", "```\nunclosed"); !errors.Is(err, ErrNotPlaced) {
		t.Fatalf("text with an unclosed fence: %v", err)
	}
	// A heading inside a list or a block quote is not a target: a new section is added.
	out, ok, err = Insert([]byte("- ## Item\n\n> ## Quote\n"), "Item", "t")
	if err != nil || ok || !strings.HasSuffix(string(out), "\n\n## Item\n\nt\n") {
		t.Fatalf("container heading: %q %v %v", out, ok, err)
	}
	// Text that is a list or a quote is placed as its own block.
	if _, _, err := Insert([]byte("## A\n\nx\n\n## B\n"), "A", "- one\n- two"); err != nil {
		t.Fatalf("list text: %v", err)
	}
	// A new or empty document gets just the section.
	if out, ok, _ = Insert(nil, "Release", "t"); ok || string(out) != "## Release\n\nt\n" {
		t.Fatalf("new file: %q", out)
	}
	// Indentation is meaning (an indented code block): only blank lines are trimmed.
	if out, _, _ = Insert(nil, "R", "\n\n    code line\n    more\n\n"); string(out) != "## R\n\n    code line\n    more\n" {
		t.Fatalf("indent: %q", out)
	}
	// A CRLF document stays CRLF throughout.
	out, ok, _ = Insert([]byte("## A\r\n\r\nx\r\n\r\n## B\r\n"), "A", "one\ntwo")
	if !ok || string(out) != "## A\r\n\r\nx\r\n\r\none\r\ntwo\r\n\r\n## B\r\n" {
		t.Fatalf("crlf: %q", out)
	}
}

// A section is accepted only when it reads back as itself, so the heading a proposal returns is
// the one a supersede finds after the merge.
func TestSectionRoundTrip(t *testing.T) {
	for _, s := range []string{"Release", "Use `make`", "リリース手順", "C# notes"} {
		if !SectionOK(s) {
			t.Errorf("%q refused", s)
			continue
		}
		out, _, err := Insert([]byte("# Doc\n"), s, "t")
		if _, found := FindHeading(out, s); err != nil || !found {
			t.Errorf("%q does not round-trip: %q %v", s, out, err)
		}
	}
	for _, s := range []string{"", "Release #", " pad", "a\nb", "a\rb", strings.Repeat("x", MaxSection+1)} {
		if SectionOK(s) {
			t.Errorf("%q accepted", s)
		}
	}
}

func TestDefaultSection(t *testing.T) {
	for _, c := range []struct{ desc, name, want string }{
		{"Release steps. Then more.", "n", "Release steps"},
		{"リリース手順。詳細は後で", "n", "リリース手順"},
		{"Build trap — never do X", "n", "Build trap"},
		{"", "the-name", "the-name"},
		{"a\tb", "n", "a b"},
		{"Release #", "n", "Release"},
		{"###", "the-name", "the-name"},
		{strings.Repeat("x", 100), "n", strings.Repeat("x", 80)},
	} {
		got := DefaultSection(c.desc, c.name)
		if got != c.want || !SectionOK(got) {
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
