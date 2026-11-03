package jirax

// Markdown <-> Confluence storage format for the page shim (confluence_page.go).
//
// Why Go and not confluence-cli's own converter piped through node: that converter is private
// to the CLI's lib/ (deep requires break on any bump of the pinned version), is v1-shaped, and
// would put a node start (~0.3 s) and a second process boundary between the text and the golden
// tests. goldmark is already a dependency (jira_adf.go), and its CJK-friendly emphasis rule is
// what keeps `**太字**` and `` `code` `` working directly next to Japanese text, which is the
// failure Jira's wiki markup had.
//
// Writing: goldmark renders XHTML, so the result is well-formed XML (storage format is XML, not
// HTML). Code blocks become the `code` macro and images the `ac:image` element. Non-ASCII text
// is emitted as UTF-8, never as numeric entities, so nothing is re-encoded on the way.
//
// Reading: storage is parsed with encoding/xml in non-strict mode (undeclared `ac:` / `ri:`
// prefixes, HTML named entities) and rendered to Markdown or plain text. Entities are decoded by
// the parser, so none leaks into the output; U+00A0 becomes a plain space.

import (
	"bytes"
	"encoding/xml"
	"html"
	"io"
	"strconv"
	"strings"
	"unicode"

	cjkfriendly "github.com/tats-u/goldmark-cjk-friendly/v2"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer"
	ghtml "github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/util"
)

var confluenceMarkdown = goldmark.New(
	goldmark.WithExtensions(cjkfriendly.CJKFriendlyEmphasisAndStrikethrough, extension.Linkify, extension.Table),
	goldmark.WithRendererOptions(
		ghtml.WithXHTML(),
		renderer.WithNodeRenderers(util.Prioritized(storageRenderer{}, 100)),
	),
)

// MarkdownToStorage converts Markdown to Confluence storage format.
func MarkdownToStorage(md string) (string, error) {
	var b bytes.Buffer
	if err := confluenceMarkdown.Convert([]byte(md), &b); err != nil {
		return "", err
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// storageRenderer overrides the nodes whose HTML is not valid storage.
type storageRenderer struct{}

func (storageRenderer) RegisterFuncs(r renderer.NodeRendererFuncRegisterer) {
	r.Register(ast.KindFencedCodeBlock, renderCodeMacro)
	r.Register(ast.KindCodeBlock, renderCodeMacro)
	r.Register(ast.KindImage, renderStorageImage)
	r.Register(ast.KindRawHTML, renderRawHTML)
}

// cdata wraps s in a CDATA section; a literal "]]>" is split across two sections.
func cdata(s string) string {
	return "<![CDATA[" + strings.ReplaceAll(s, "]]>", "]]]]><![CDATA[>") + "]]>"
}

func renderCodeMacro(w util.BufWriter, src []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	var lines bytes.Buffer
	for i := 0; i < n.Lines().Len(); i++ {
		l := n.Lines().At(i)
		lines.Write(l.Value(src))
	}
	lang := ""
	if f, ok := n.(*ast.FencedCodeBlock); ok {
		lang = string(f.Language(src))
	}
	code := strings.TrimSuffix(lines.String(), "\n")
	_, _ = w.WriteString(`<ac:structured-macro ac:name="code" ac:schema-version="1">`)
	if lang != "" {
		_, _ = w.WriteString(`<ac:parameter ac:name="language">` + html.EscapeString(lang) + `</ac:parameter>`)
	}
	_, _ = w.WriteString(`<ac:plain-text-body>` + cdata(code) + `</ac:plain-text-body></ac:structured-macro>` + "\n")
	return ast.WalkSkipChildren, nil
}

// renderRawHTML lets a literal <br> through (how a table cell holds a line break) and omits the
// rest, as goldmark's safe mode does: storage must stay well-formed XML.
func renderRawHTML(w util.BufWriter, src []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkSkipChildren, nil
	}
	r := n.(*ast.RawHTML)
	var b bytes.Buffer
	for i := 0; i < r.Segments.Len(); i++ {
		seg := r.Segments.At(i)
		b.Write(seg.Value(src))
	}
	switch strings.ToLower(strings.ReplaceAll(b.String(), " ", "")) {
	case "<br>", "<br/>":
		_, _ = w.WriteString("<br />")
	default:
		_, _ = w.WriteString("<!-- raw HTML omitted -->")
	}
	return ast.WalkSkipChildren, nil
}

func renderStorageImage(w util.BufWriter, src []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	img := n.(*ast.Image)
	dest := string(img.Destination)
	if strings.Contains(dest, "://") {
		_, _ = w.WriteString(`<ac:image><ri:url ri:value="` + html.EscapeString(dest) + `" /></ac:image>`)
	} else {
		_, _ = w.WriteString(`<ac:image><ri:attachment ri:filename="` + html.EscapeString(dest) + `" /></ac:image>`)
	}
	return ast.WalkSkipChildren, nil
}

// ---------------------------------------------------------------------------------------------
// storage -> Markdown / text

// sNode is one element of a parsed storage document; a text node has name "".
type sNode struct {
	name     string // "ac:structured-macro", "p", … ; "" for text
	attr     map[string]string
	text     string
	children []*sNode
}

func (n *sNode) a(k string) string { return n.attr[k] }

func qname(n xml.Name) string {
	if n.Space != "" {
		return n.Space + ":" + n.Local
	}
	return n.Local
}

// parseStorage parses a storage fragment. A fragment is not a document (several roots), so it is
// wrapped; a parse error part-way keeps what was read, since a read must not fail on one bad tag.
func parseStorage(s string) *sNode {
	d := xml.NewDecoder(strings.NewReader("<root>" + s + "</root>"))
	d.Strict = false
	d.Entity = xml.HTMLEntity
	// No AutoClose: storage is XHTML (`<br />`), and the HTML void list would swallow the
	// children of <ac:link> (its Local name is "link").
	d.CharsetReader = func(_ string, in io.Reader) (io.Reader, error) { return in, nil }
	root := &sNode{name: "root"}
	stack := []*sNode{root}
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		top := stack[len(stack)-1]
		switch t := tok.(type) {
		case xml.StartElement:
			n := &sNode{name: qname(t.Name), attr: map[string]string{}}
			for _, at := range t.Attr {
				n.attr[qname(at.Name)] = at.Value
			}
			top.children = append(top.children, n)
			stack = append(stack, n)
		case xml.EndElement:
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			top.children = append(top.children, &sNode{text: string(t)})
		}
	}
	if len(root.children) == 0 {
		return root
	}
	return root.children[0] // the wrapper element
}

// StorageToMarkdown renders storage format as Markdown.
func StorageToMarkdown(storage string) string {
	r := &storageWriter{md: true}
	return r.finish(r.blocks(parseStorage(storage).children))
}

// StorageToText renders storage format as plain text.
func StorageToText(storage string) string {
	r := &storageWriter{}
	return r.finish(r.blocks(parseStorage(storage).children))
}

type storageWriter struct {
	md      bool
	inTable bool // inside a table cell: a literal pipe must be written as \|
}

// Code content is literal: finish normalises prose (NBSP, trailing blanks, runs of empty lines),
// which would silently rewrite whitespace-significant code. protect swaps what finish touches for
// private-use runes (a code line that is empty becomes one holding only protEmpty, so the
// block-quote / list prefixes still apply to it), and finish swaps them back at the very end.
const (
	protNBSP  = '\ue001'
	protSpace = '\ue002'
	protTab   = '\ue003'
	protEmpty = '\ue004'
)

func protect(code string) string {
	lines := strings.Split(strings.ReplaceAll(code, "\r\n", "\n"), "\n")
	for i, l := range lines {
		l = strings.ReplaceAll(l, "\u00a0", string(protNBSP))
		t := strings.TrimRight(l, " \t")
		var tail strings.Builder
		for _, r := range l[len(t):] {
			if r == ' ' {
				tail.WriteRune(protSpace)
			} else {
				tail.WriteRune(protTab)
			}
		}
		l = t + tail.String()
		if l == "" {
			l = string(protEmpty)
		}
		lines[i] = l
	}
	return strings.Join(lines, "\n")
}

var unprotect = strings.NewReplacer(string(protNBSP), "\u00a0", string(protSpace), " ", string(protTab), "\t", string(protEmpty), "")

func (w *storageWriter) finish(s string) string {
	s = strings.ReplaceAll(s, "\u00a0", " ")
	var out []string
	blank := 0
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimRight(l, " \t")
		if l == "" {
			blank++
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, l)
	}
	return unprotect.Replace(strings.Trim(strings.Join(out, "\n"), "\n"))
}

var blockNames = map[string]bool{
	"p": true, "h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"ul": true, "ol": true, "table": true, "pre": true, "blockquote": true, "hr": true,
	"ac:structured-macro": true, "ac:task-list": true, "div": true,
	"ac:layout": true, "ac:layout-section": true, "ac:layout-cell": true,
}

// blocks renders a run of children as blocks separated by blank lines; loose inline content
// between blocks (a bare text directly under the root or a <li>) forms a paragraph of its own.
func (w *storageWriter) blocks(ns []*sNode) string {
	var parts []string
	var inline []*sNode
	flush := func() {
		if len(inline) > 0 {
			if s := strings.TrimSpace(w.inlines(inline)); s != "" {
				parts = append(parts, s)
			}
			inline = nil
		}
	}
	for _, n := range ns {
		if n.name != "" && blockNames[n.name] {
			flush()
			if s := w.block(n); strings.TrimSpace(s) != "" {
				parts = append(parts, strings.TrimRight(s, "\n"))
			}
			continue
		}
		inline = append(inline, n)
	}
	flush()
	return strings.Join(parts, "\n\n")
}

func (w *storageWriter) block(n *sNode) string {
	switch n.name {
	case "p":
		return trimBreaks(w.inlines(n.children))
	case "div", "ac:layout", "ac:layout-section", "ac:layout-cell":
		return w.blocks(n.children)
	case "h1", "h2", "h3", "h4", "h5", "h6":
		t := trimBreaks(w.inlines(n.children))
		if w.md {
			return strings.Repeat("#", int(n.name[1]-'0')) + " " + t
		}
		return t
	case "ul", "ol":
		return w.list(n, "")
	case "hr":
		if w.md {
			return "---"
		}
		return "----"
	case "pre":
		code := protect(plainText(n))
		if w.md {
			return fence(code, "")
		}
		return code
	case "blockquote":
		inner := w.blocks(n.children)
		if !w.md {
			return inner
		}
		return prefixLines(inner, "> ")
	case "table":
		return w.table(n)
	case "ac:task-list":
		var l []string
		for _, t := range n.children {
			if t.name != "ac:task" {
				continue
			}
			box := "[ ] "
			var body string
			for _, c := range t.children {
				switch c.name {
				case "ac:task-status":
					if strings.TrimSpace(plainText(c)) == "complete" {
						box = "[x] "
					}
				case "ac:task-body":
					body = strings.TrimSpace(w.inlines(c.children))
				}
			}
			l = append(l, "- "+box+body)
		}
		return strings.Join(l, "\n")
	case "ac:structured-macro":
		return w.macro(n)
	}
	return strings.TrimSpace(w.inlines(n.children))
}

func (w *storageWriter) macro(n *sNode) string {
	name := n.a("ac:name")
	var body *sNode
	lang := ""
	for _, c := range n.children {
		switch c.name {
		case "ac:plain-text-body":
			body = c
		case "ac:rich-text-body":
			body = c
		case "ac:parameter":
			if c.a("ac:name") == "language" {
				lang = strings.TrimSpace(plainText(c))
			}
		}
	}
	switch name {
	case "code", "noformat":
		code := ""
		if body != nil {
			code = plainText(body)
		}
		code = protect(code)
		if w.md {
			return fence(code, lang)
		}
		return code
	case "info", "note", "warning", "tip", "panel", "expand", "excerpt":
		if body == nil {
			return ""
		}
		inner := w.blocks(body.children)
		if !w.md {
			return inner
		}
		label := ""
		if name != "panel" && name != "expand" && name != "excerpt" {
			label = "**" + strings.ToUpper(name[:1]) + name[1:] + "**\n\n"
		}
		return prefixLines(label+inner, "> ")
	case "toc", "children":
		return ""
	}
	if body != nil {
		return w.blocks(body.children)
	}
	// Body-less macros keep what they show: a status lozenge is its title parameter.
	var titles []string
	for _, c := range n.children {
		if c.name == "ac:parameter" && c.a("ac:name") == "title" {
			if t := strings.TrimSpace(plainText(c)); t != "" {
				titles = append(titles, t)
			}
		}
	}
	if name == "status" && len(titles) > 0 {
		if w.md {
			return escapeMD(titles[0])
		}
		return titles[0]
	}
	if len(titles) > 0 {
		return "[" + name + " macro: " + strings.Join(titles, ", ") + "]"
	}
	return "[" + name + " macro]"
}

// list renders one list; indent is the width of every ancestor marker, so a child list sits
// under its parent item's text (an ordered parent needs 3+ columns, a bullet 2).
func (w *storageWriter) list(n *sNode, indent string) string {
	var lines []string
	i := 0
	for _, li := range n.children {
		if li.name != "li" {
			continue
		}
		i++
		marker := "- "
		if n.name == "ol" {
			marker = strconv.Itoa(i) + ". "
		} else if !w.md {
			marker = "* "
		}
		pad := strings.Repeat(" ", len(marker))
		var head []*sNode
		var subs []*sNode
		for _, c := range li.children {
			if c.name == "ul" || c.name == "ol" {
				subs = append(subs, c)
			} else {
				head = append(head, c)
			}
		}
		text := strings.TrimSpace(w.blocks(head))
		hl := strings.Split(text, "\n")
		lines = append(lines, indent+marker+hl[0])
		for _, l := range hl[1:] {
			if l == "" {
				lines = append(lines, "")
			} else {
				lines = append(lines, indent+pad+l)
			}
		}
		for _, s := range subs {
			lines = append(lines, w.list(s, indent+pad))
		}
	}
	return strings.Join(lines, "\n")
}

func (w *storageWriter) table(n *sNode) string {
	var rows [][]string
	header := false
	var walk func(*sNode)
	walk = func(x *sNode) {
		for _, c := range x.children {
			switch c.name {
			case "thead", "tbody", "colgroup":
				if c.name != "colgroup" {
					walk(c)
				}
			case "tr":
				var cells []string
				for i, td := range c.children {
					if td.name != "td" && td.name != "th" {
						continue
					}
					if td.name == "th" && len(rows) == 0 {
						header = true
					}
					_ = i
					w.inTable = true
					s := strings.TrimSpace(w.blocks(td.children))
					w.inTable = false
					if w.md {
						// Pipes are already escaped where they were written (text and code);
						// a cell cannot hold a newline, so it becomes <br>, which
						// MarkdownToStorage reads back as a line break.
						s = strings.ReplaceAll(strings.ReplaceAll(s, "\n\n", "<br>"), "\n", "<br>")
					} else {
						s = strings.ReplaceAll(s, "\n", " ")
					}
					cells = append(cells, s)
				}
				rows = append(rows, cells)
			}
		}
	}
	walk(n)
	if len(rows) == 0 {
		return ""
	}
	cols := 0
	for _, r := range rows {
		if len(r) > cols {
			cols = len(r)
		}
	}
	for i := range rows {
		for len(rows[i]) < cols {
			rows[i] = append(rows[i], "")
		}
	}
	if !w.md {
		var l []string
		for _, r := range rows {
			l = append(l, strings.Join(r, " | "))
		}
		return strings.Join(l, "\n")
	}
	// Markdown tables need a header row; without <th> the first row stands in for it.
	_ = header
	var l []string
	l = append(l, "| "+strings.Join(rows[0], " | ")+" |")
	sep := make([]string, cols)
	for i := range sep {
		sep[i] = "---"
	}
	l = append(l, "| "+strings.Join(sep, " | ")+" |")
	for _, r := range rows[1:] {
		l = append(l, "| "+strings.Join(r, " | ")+" |")
	}
	return strings.Join(l, "\n")
}

// inlines renders phrasing content. Whitespace is kept as written (the callers trim), and a
// marker pair opened over leading/trailing spaces is moved outside them so the Markdown stays
// valid (`** x **` is not bold).
func (w *storageWriter) inlines(ns []*sNode) string {
	var b strings.Builder
	for _, n := range ns {
		b.WriteString(w.inline(n))
	}
	return b.String()
}

func wrapMark(inner, open, closeM string) string {
	t := strings.TrimSpace(inner)
	if t == "" {
		return inner
	}
	lead := inner[:len(inner)-len(strings.TrimLeft(inner, " \t\n"))]
	trail := inner[len(strings.TrimRight(inner, " \t\n")):]
	return lead + open + t + closeM + trail
}

func (w *storageWriter) inline(n *sNode) string {
	if n.name == "" {
		t := collapseSpace(n.text)
		if w.md {
			return escapeMD(t)
		}
		return t
	}
	switch n.name {
	case "br":
		if w.md {
			if w.inTable {
				return "<br>"
			}
			return "\\\n" // a hard break; trailing blanks would be trimmed
		}
		return "\n"
	case "strong", "b":
		if w.md {
			return wrapMark(w.inlines(n.children), "**", "**")
		}
	case "em", "i":
		if w.md {
			return wrapMark(w.inlines(n.children), "*", "*")
		}
	case "del", "s", "strike":
		if w.md {
			return wrapMark(w.inlines(n.children), "~~", "~~")
		}
	case "code":
		c := plainText(n)
		if !w.md {
			return protect(c)
		}
		if w.inTable {
			c = strings.ReplaceAll(c, "|", `\|`)
		}
		tick := "`"
		for strings.Contains(c, tick) {
			tick += "`"
		}
		if strings.HasPrefix(c, "`") || strings.HasSuffix(c, "`") ||
			(strings.HasPrefix(c, " ") && strings.HasSuffix(c, " ") && strings.Trim(c, " ") != "") {
			c = " " + c + " "
		}
		return tick + protect(c) + tick
	case "a":
		inner := w.inlines(n.children)
		href := n.a("href")
		if !w.md || href == "" {
			if href != "" && strings.TrimSpace(inner) != href {
				return inner + " (" + href + ")"
			}
			return inner
		}
		return "[" + strings.TrimSpace(inner) + "](" + mdDest(href) + ")"
	case "time":
		return n.a("datetime")
	case "img":
		if w.md {
			return "![" + n.a("alt") + "](" + mdDest(n.a("src")) + ")"
		}
		return n.a("alt")
	case "ac:emoticon":
		if f := n.a("ac:emoji-fallback"); f != "" {
			return f
		}
		return ":" + strings.TrimPrefix(n.a("ac:name"), "emoji-") + ":"
	case "ac:image":
		for _, c := range n.children {
			switch c.name {
			case "ri:url":
				return w.imageRef(c.a("ri:value"))
			case "ri:attachment":
				return w.imageRef(c.a("ri:filename"))
			}
		}
		return ""
	case "ac:link":
		return w.acLink(n)
	case "ac:structured-macro":
		return strings.TrimSpace(w.macro(n))
	case "ac:inline-comment-marker", "span", "u", "sub", "sup", "small", "big", "font", "ac:rich-text-body", "ac:link-body":
		return w.inlines(n.children)
	case "ac:placeholder", "ac:parameter", "ri:user":
		return ""
	}
	return w.inlines(n.children)
}

func (w *storageWriter) imageRef(ref string) string {
	if w.md {
		return "![](" + mdDest(ref) + ")"
	}
	return ref
}

func (w *storageWriter) acLink(n *sNode) string {
	var label, target string
	for _, c := range n.children {
		switch c.name {
		case "ac:link-body", "ac:plain-text-link-body":
			label = strings.TrimSpace(w.inlines(c.children))
		case "ri:page":
			target = c.a("ri:content-title")
		case "ri:attachment":
			target = c.a("ri:filename")
		}
	}
	if label == "" {
		label = target
		if w.md {
			label = escapeMD(label)
		}
	}
	return label
}

// plainText is the literal text below n, with no escaping and no collapsing (code content).
func plainText(n *sNode) string {
	if n.name == "" {
		return n.text
	}
	var b strings.Builder
	for _, c := range n.children {
		if c.name == "br" {
			b.WriteString("\n")
			continue
		}
		b.WriteString(plainText(c))
	}
	return b.String()
}

// collapseSpace folds runs of whitespace (storage is pretty-printed HTML, where a newline is a
// space), but keeps U+00A0, which finish turns into a plain space after the structure is known.
func collapseSpace(s string) string {
	var b strings.Builder
	sp := false
	for _, r := range s {
		if r == ' ' || r == '\n' || r == '\t' || r == '\r' {
			if !sp {
				b.WriteByte(' ')
			}
			sp = true
			continue
		}
		sp = false
		b.WriteRune(r)
	}
	return b.String()
}

// escapeMD escapes what would turn literal text into Markdown syntax. Underscore is escaped only
// where it could open or close emphasis (not inside a word), and line-start markers only when
// the text actually begins that way.
func escapeMD(s string) string {
	var b strings.Builder
	rs := []rune(s)
	for i, r := range rs {
		switch r {
		case '\\', '*', '`', '[', ']', '<', '|', '~':
			b.WriteByte('\\')
		case '_':
			prevAlnum := i > 0 && (unicode.IsLetter(rs[i-1]) || unicode.IsDigit(rs[i-1]))
			nextAlnum := i+1 < len(rs) && (unicode.IsLetter(rs[i+1]) || unicode.IsDigit(rs[i+1]))
			if !(prevAlnum && nextAlnum) {
				b.WriteByte('\\')
			}
		}
		b.WriteRune(r)
	}
	out := b.String()
	t := strings.TrimLeft(out, " ")
	if t != "" {
		switch {
		case strings.HasPrefix(t, "#"), strings.HasPrefix(t, ">"), strings.HasPrefix(t, "+ "), strings.HasPrefix(t, "- "):
			out = out[:len(out)-len(t)] + `\` + t
		default:
			j := 0
			for j < len(t) && t[j] >= '0' && t[j] <= '9' {
				j++
			}
			if j > 0 && j+1 < len(t) && (t[j] == '.' || t[j] == ')') && t[j+1] == ' ' {
				out = out[:len(out)-len(t)] + t[:j] + `\` + t[j:]
			}
		}
	}
	return out
}

// mdDest makes a link destination safe inside (…): spaces and parentheses are percent-encoded.
func mdDest(u string) string {
	if u == "" {
		return u
	}
	r := strings.NewReplacer(" ", "%20", "(", "%28", ")", "%29", "<", "%3C", ">", "%3E")
	return r.Replace(u)
}

func fence(code, lang string) string {
	f := "```"
	for strings.Contains(code, f) {
		f += "`"
	}
	if code == "" {
		return f + lang + "\n" + f
	}
	return f + lang + "\n" + code + "\n" + f
}

// trimBreaks trims a paragraph and drops a hard break at either end, which Markdown cannot
// express (a trailing backslash would be a literal one).
func trimBreaks(s string) string {
	s = strings.TrimSpace(s)
	for strings.HasPrefix(s, "\\\n") {
		s = strings.TrimSpace(s[2:])
	}
	for {
		n := 0
		for n < len(s) && s[len(s)-1-n] == '\\' {
			n++
		}
		if n%2 == 0 {
			return s
		}
		s = strings.TrimSpace(s[:len(s)-1])
	}
}

func prefixLines(s, p string) string {
	ls := strings.Split(s, "\n")
	for i, l := range ls {
		if l == "" {
			ls[i] = strings.TrimRight(p, " ")
		} else {
			ls[i] = p + l
		}
	}
	return strings.Join(ls, "\n")
}
