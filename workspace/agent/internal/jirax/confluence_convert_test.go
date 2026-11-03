package jirax

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The converter is the encoding-sensitive part of the page shim (Jira's ADF path garbled
// Japanese next to inline markup), so each fixture is pinned three ways: the storage it produces
// (golden file; AF_UPDATE_GOLDEN=1 rewrites it), the Markdown it reads back as (the fixture
// itself, i.e. a lossless round trip), and the plain text.

func goldenCheck(t *testing.T, path, got string) {
	t.Helper()
	if os.Getenv("AF_UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("%s differs.\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
	}
}

func TestConfluenceMarkdownRoundTripGolden(t *testing.T) {
	files, _ := filepath.Glob("testdata/confluence/*.md")
	if len(files) == 0 {
		t.Fatal("no fixtures")
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			md := strings.TrimRight(string(b), "\n")
			st, err := MarkdownToStorage(md)
			if err != nil {
				t.Fatal(err)
			}
			goldenCheck(t, strings.TrimSuffix(f, ".md")+".storage", st)
			if back := StorageToMarkdown(st); back != md {
				t.Errorf("round trip changed the text.\n--- back ---\n%s\n--- original ---\n%s", back, md)
			}
			txt := StorageToText(st)
			for _, leak := range []string{"&#39;", "&amp;", "&quot;", "&lt;", "&gt;", "&nbsp;", "<p>", "**", "<ac:"} {
				if strings.Contains(txt, leak) {
					t.Errorf("text output leaks %q:\n%s", leak, txt)
				}
			}
		})
	}
}

func TestConfluenceStorageHasNoEscapedNonASCII(t *testing.T) {
	st, _ := MarkdownToStorage("日本語😀𠮷 **太**")
	if !strings.Contains(st, "日本語😀𠮷 <strong>太</strong>") {
		t.Fatalf("non-ASCII text re-encoded or emphasis lost: %s", st)
	}
	if strings.Contains(st, "&#") {
		t.Fatalf("numeric entity in %s", st)
	}
}

// Entities in what Confluence stores (it writes &rsquo; &nbsp; &#39; …) must come out as text.
func TestConfluenceReadDecodesEntities(t *testing.T) {
	in := `<p>It&#39;s &amp; &quot;fine&quot; &rsquo;x&nbsp;y &lt;b&gt; &#x1F600; 日本&hellip;</p>`
	want := "It's & \"fine\" ’x y <b> 😀 日本…"
	if got := StorageToText(in); got != want {
		t.Errorf("text = %q, want %q", got, want)
	}
	if got := StorageToMarkdown(in); got != `It's & "fine" ’x y \<b> 😀 日本…` {
		t.Errorf("markdown = %q", got)
	}
}

func TestConfluenceReadMacrosAndLinks(t *testing.T) {
	in := `<ac:structured-macro ac:name="code"><ac:parameter ac:name="language">py</ac:parameter><ac:plain-text-body><![CDATA[print("あ")
x = 1 < 2]]></ac:plain-text-body></ac:structured-macro>` +
		`<ac:structured-macro ac:name="info"><ac:rich-text-body><p>注意</p></ac:rich-text-body></ac:structured-macro>` +
		`<p><ac:link><ri:page ri:content-title="別ページ" /></ac:link> <ac:emoticon ac:name="smile" ac:emoji-fallback="😊" /></p>`
	want := "```py\nprint(\"あ\")\nx = 1 < 2\n```\n\n> **Info**\n>\n> 注意\n\n別ページ 😊"
	if got := StorageToMarkdown(in); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// Markdown syntax characters in stored text are escaped, so a read→update cycle cannot turn
// literal text into markup.
func TestConfluenceReadEscapesMarkdownSyntax(t *testing.T) {
	in := `<p>a*b_c [x] ` + "`t`" + ` | #not</p><p>1. not a list</p>`
	got := StorageToMarkdown(in)
	st, _ := MarkdownToStorage(got)
	if StorageToText(st) != StorageToText(in) {
		t.Errorf("markdown %q does not round-trip: %q vs %q", got, StorageToText(st), StorageToText(in))
	}
}

func TestConfluenceMarkdownToStorageIsWellFormed(t *testing.T) {
	st, _ := MarkdownToStorage("a & b < c\n\nline  \nbreak\n\n---\n\n![x](pic.png)")
	for _, w := range []string{"a &amp; b &lt; c", "<br />", "<hr />", `ri:filename="pic.png"`} {
		if !strings.Contains(st, w) {
			t.Errorf("missing %q in %s", w, st)
		}
	}
}
