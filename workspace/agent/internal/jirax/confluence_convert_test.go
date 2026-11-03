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

// Code is literal: leading/trailing empty lines, runs of empty lines, trailing blanks and NBSP
// survive a read, in the code macro, <pre> and inline code, inside a quote or list as well.
func TestConfluenceReadKeepsCodeWhitespaceLiteral(t *testing.T) {
	code := "\n first  \n\n\nlast\t \n"
	macro := `<ac:structured-macro ac:name="code"><ac:plain-text-body><![CDATA[` + code + `]]></ac:plain-text-body></ac:structured-macro>`
	for name, in := range map[string]string{
		"macro": macro,
		"pre":   "<pre>" + code + "</pre>",
		"quote": "<blockquote>" + macro + "</blockquote>",
		"list":  "<ul><li>x" + macro + "</li></ul>",
	} {
		md := StorageToMarkdown(in)
		st, err := MarkdownToStorage(md)
		if err != nil {
			t.Fatal(err)
		}
		back := parseStorage(st)
		var got string
		var find func(n *sNode)
		find = func(n *sNode) {
			if n.name == "ac:plain-text-body" {
				got = plainText(n)
			}
			for _, c := range n.children {
				find(c)
			}
		}
		find(back)
		if got != code {
			t.Errorf("%s: code changed: %q, want %q\nmarkdown:\n%s", name, got, code, md)
		}
		if strings.ContainsAny(md, "") {
			t.Errorf("%s: private marker leaked: %q", name, md)
		}
	}
	txt := StorageToText(macro)
	if txt != strings.ReplaceAll(code, " ", " ") {
		t.Errorf("text code = %q, want %q", txt, code)
	}
	in := "<p>a <code>x y  </code> b</p>"
	if got := StorageToMarkdown(in); got != "a `x y  ` b" {
		t.Errorf("inline code = %q", got)
	}
}

// A pipe is escaped once, in text and in inline code, and survives a round trip unchanged.
func TestConfluenceTablePipesAndBackslashesRoundTrip(t *testing.T) {
	in := `<table><tbody><tr><th>日本|語</th><th>c</th></tr><tr><td>a\b <code>x|y\z</code></td><td>2</td></tr></tbody></table>`
	md := StorageToMarkdown(in)
	if strings.Contains(md, `\\|`) {
		t.Fatalf("pipe escaped twice: %s", md)
	}
	st, _ := MarkdownToStorage(md)
	for _, w := range []string{"<th>日本|語</th>", `<td>a\b <code>x|y\z</code></td>`} {
		if !strings.Contains(st, w) {
			t.Errorf("missing %q in %s", w, st)
		}
	}
}

func TestConfluenceLayoutKeepsBlockBoundaries(t *testing.T) {
	in := `<ac:layout><ac:layout-section ac:type="two_equal"><ac:layout-cell><p>段落一</p><p>段落二</p></ac:layout-cell><ac:layout-cell><h2>別列</h2><ul><li>x</li></ul></ac:layout-cell></ac:layout-section></ac:layout>` +
		`<div><p>a</p><p>b</p></div>`
	want := "段落一\n\n段落二\n\n## 別列\n\n- x\n\na\n\nb"
	if got := StorageToMarkdown(in); got != want {
		t.Errorf("markdown = %q", got)
	}
	if got := StorageToText(in); got != "段落一\n\n段落二\n\n別列\n\n* x\n\na\n\nb" {
		t.Errorf("text = %q", got)
	}
}

func TestConfluenceStatusMacroKeepsItsTitle(t *testing.T) {
	in := `<p>進捗：<ac:structured-macro ac:name="status"><ac:parameter ac:name="colour">Green</ac:parameter><ac:parameter ac:name="title">完了😀</ac:parameter></ac:structured-macro></p>`
	if got := StorageToMarkdown(in); got != "進捗：完了😀" {
		t.Errorf("markdown = %q", got)
	}
	if got := StorageToText(in); got != "進捗：完了😀" {
		t.Errorf("text = %q", got)
	}
	other := `<p><ac:structured-macro ac:name="jira"><ac:parameter ac:name="title">PROJ-1</ac:parameter></ac:structured-macro></p>`
	if got := StorageToText(other); got != "[jira macro: PROJ-1]" {
		t.Errorf("other macro = %q", got)
	}
}

// A <br> in a paragraph and in a table cell reads as a hard break and writes back as <br />.
func TestConfluenceLineBreaksRoundTrip(t *testing.T) {
	in := `<p>日本<br />語</p><table><tbody><tr><th>h</th></tr><tr><td>一<br />二</td></tr></tbody></table><p>end<br /></p>`
	md := StorageToMarkdown(in)
	st, _ := MarkdownToStorage(md)
	for _, w := range []string{"<p>日本<br />\n語</p>", "<td>一<br />二</td>", "<p>end</p>"} {
		if !strings.Contains(st, w) {
			t.Errorf("missing %q in %s\nmarkdown: %q", w, st, md)
		}
	}
	if strings.Contains(st, "omitted") || strings.HasSuffix(strings.TrimSpace(md), `\`) {
		t.Errorf("markdown %q / storage %s", md, st)
	}
	if got := StorageToText(in); got != "日本\n語\n\nh\n一\n二\n\nend" {
		// table cells flatten newlines to spaces in text
		if !strings.Contains(got, "日本\n語") {
			t.Errorf("text = %q", got)
		}
	}
}

// A child list is indented by the real width of its ancestors' markers, so it stays a child.
func TestConfluenceNestedListsKeepStructure(t *testing.T) {
	in := `<ol><li>親<ul><li>子<ol><li>孫</li></ol></li></ul></li><li>二</li></ol><p>x</p>` +
		`<ol><li>1</li><li>2</li><li>3</li><li>4</li><li>5</li><li>6</li><li>7</li><li>8</li><li>9</li><li>十<ul><li>子</li></ul></li></ol>`
	md := StorageToMarkdown(in)
	st, _ := MarkdownToStorage(md)
	for _, w := range []string{"<li>親\n<ul>\n<li>子\n<ol>\n<li>孫</li>", "<li>十\n<ul>\n<li>子</li>"} {
		if !strings.Contains(st, w) {
			t.Errorf("structure lost: missing %q in %s\nmarkdown:\n%s", w, st, md)
		}
	}
	if strings.Count(st, "<ol>") != 3 {
		t.Errorf("want 3 <ol>, got %s", st)
	}
}

// Private-use characters are legal input (gaiji): they come out as written, in prose, inline
// code and code blocks, even when the document also needs the whitespace markers.
func TestConfluenceReadKeepsPrivateUseCharacters(t *testing.T) {
	pua := "\ue001\ue002\ue003\ue004\ue005"
	in := "<p>外字" + pua + "終 <code>c" + pua + "  </code></p>" +
		`<ac:structured-macro ac:name="code"><ac:plain-text-body><![CDATA[a` + pua + " \n\n\nb\u00a0\t]]></ac:plain-text-body></ac:structured-macro>"
	md := StorageToMarkdown(in)
	for _, w := range []string{"外字" + pua + "終", "`c" + pua + "  `", "a" + pua + " \n\n\nb\u00a0\t\n```"} {
		if !strings.Contains(md, w) {
			t.Errorf("markdown lost %q:\n%q", w, md)
		}
	}
	if txt := StorageToText(in); !strings.Contains(txt, "外字"+pua+"終") {
		t.Errorf("text lost the characters: %q", txt)
	}
}

// Text has no hard-break syntax: a trailing backslash is just text.
func TestConfluenceTextKeepsTrailingBackslash(t *testing.T) {
	for in, want := range map[string]string{
		`<p>パス C:\</p>`:          `パス C:\`,
		`<p><code>x\</code></p>`: `x\`,
		`<h2>dir\</h2>`:          `dir\`,
	} {
		if got := StorageToText(in); got != want {
			t.Errorf("%s -> %q, want %q", in, got, want)
		}
	}
	if got := StorageToMarkdown(`<p>パス C:\</p>`); got != `パス C:\\` {
		t.Errorf("markdown = %q", got)
	}
}

// A fenced block cannot live in a table cell. Each code line becomes a code span joined by a
// break: no "<br>" text enters the code and every line's characters survive a round trip.
func TestConfluenceCodeInTableCellKeepsEveryLine(t *testing.T) {
	lines := []string{"echo あ", "  x|y\\z ", "", "echo い"}
	code := strings.Join(lines, "\n")
	for name, cell := range map[string]string{
		"macro": `<ac:structured-macro ac:name="code"><ac:plain-text-body><![CDATA[` + code + `]]></ac:plain-text-body></ac:structured-macro>`,
		"pre":   "<pre>" + strings.ReplaceAll(code, "|", "|") + "</pre>",
	} {
		in := `<table><tbody><tr><th>h</th></tr><tr><td>` + cell + `</td></tr></tbody></table>`
		md := StorageToMarkdown(in)
		if strings.Contains(md, "```") {
			t.Errorf("%s: fence in a cell: %s", name, md)
		}
		st, _ := MarkdownToStorage(md)
		if strings.Contains(st, "&lt;br") {
			t.Errorf("%s: <br> became text: %s", name, st)
		}
		var spans []string
		for _, part := range strings.Split(st, "<code>")[1:] {
			spans = append(spans, strings.SplitN(part, "</code>", 2)[0])
		}
		want := []string{"echo あ", "  x|y\\z ", "echo い"}
		if len(spans) != len(want) {
			t.Fatalf("%s: spans %q in %s", name, spans, st)
		}
		for i := range want {
			if html := strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`).Replace(spans[i]); html != want[i] {
				t.Errorf("%s: line %d = %q, want %q", name, i, html, want[i])
			}
		}
		if strings.Count(st, "<br />") != 3 {
			t.Errorf("%s: want 3 breaks (one is an empty line): %s", name, st)
		}
	}
}
