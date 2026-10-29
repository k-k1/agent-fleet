package jirax

// Markdown to Atlassian Document Format, for `workspace-agent jira-comment edit`.
//
// jira-cli sends bodies as v2 wiki markup through a converter that glues a list, heading, code
// block or quote onto a plain paragraph before it. An edit is how such a comment gets
// repaired, so its body goes through goldmark and REST v3 instead, and the paragraph-then-list
// case comes out as a paragraph followed by a bulletList.
//
// Anything without an ADF counterpart (raw HTML, an image, a table inside a list or quote) degrades
// to its text rather than failing: a comment with a rough edge beats no repair.

import (
	"strings"

	cjkfriendly "github.com/tats-u/goldmark-cjk-friendly/v2"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// jiraMarkdown parses what jira-cli's own converter accepted: strikethrough, bare URLs and GFM
// tables.
//
// Emphasis and strikethrough follow the CJK-friendly amendment to CommonMark's flanking rules:
// under the plain rules `**「すべて」**です` is not bold, because a delimiter run that touches
// punctuation inside and a letter outside cannot open or close, and Japanese has no spaces to put
// there. ASCII text parses as before.
var jiraMarkdown = goldmark.New(goldmark.WithExtensions(cjkfriendly.CJKFriendlyEmphasisAndStrikethrough, extension.Linkify, extension.Table))

// adfUnescape resolves what the source still holds raw (a backslash escape, a character
// reference): goldmark's renderer does it, but this reads the segments directly.
func adfUnescape(b []byte) string {
	return string(util.ResolveEntityNames(util.ResolveNumericReferences(util.UnescapePunctuations(b))))
}

type adfNode = map[string]any

// jiraMarkdownADF converts a Markdown body to an ADF document. The result is never an empty
// document: Jira answers 400 for one.
func jiraMarkdownADF(md string) adfNode {
	src := []byte(strings.ReplaceAll(md, "\r\n", "\n"))
	root := jiraMarkdown.Parser().Parse(text.NewReader(src))
	content := adfBlocks(root, src, false)
	if len(content) == 0 {
		content = []any{adfBlankParagraph()}
	}
	return adfNode{"version": 1, "type": "doc", "content": content}
}

// adfBlankParagraph stands in for an empty block where ADF requires at least one: an empty
// text node or an empty content array is a 400.
func adfBlankParagraph() adfNode {
	return adfNode{"type": "paragraph", "content": []any{adfNode{"type": "text", "text": " "}}}
}

// adfBlocks converts the block children of parent. nested is true inside a list item or a quote,
// where ADF admits no table.
func adfBlocks(parent ast.Node, src []byte, nested bool) []any {
	out := []any{}
	for c := parent.FirstChild(); c != nil; c = c.NextSibling() {
		if b := adfBlock(c, src, nested); b != nil {
			out = append(out, b)
		}
	}
	return out
}

func adfBlock(n ast.Node, src []byte, nested bool) adfNode {
	switch v := n.(type) {
	case *ast.Paragraph, *ast.TextBlock:
		in := adfInlines(n, src, nil)
		if len(in) == 0 {
			return nil
		}
		return adfNode{"type": "paragraph", "content": in}
	case *ast.Heading:
		in := adfInlines(n, src, nil)
		if len(in) == 0 {
			return nil
		}
		return adfNode{"type": "heading", "attrs": adfNode{"level": v.Level}, "content": in}
	case *ast.List:
		kind := "bulletList"
		node := adfNode{}
		if v.IsOrdered() {
			kind = "orderedList"
			if v.Start != 1 {
				node["attrs"] = adfNode{"order": v.Start}
			}
		}
		items := []any{}
		for c := v.FirstChild(); c != nil; c = c.NextSibling() {
			blocks := adfBlocks(c, src, true)
			if len(blocks) == 0 {
				blocks = []any{adfBlankParagraph()}
			}
			items = append(items, adfNode{"type": "listItem", "content": blocks})
		}
		if len(items) == 0 {
			return nil
		}
		node["type"], node["content"] = kind, items
		return node
	case *ast.FencedCodeBlock, *ast.CodeBlock:
		return adfCodeBlock(n, src)
	case *ast.Blockquote:
		blocks := adfBlocks(v, src, true)
		if len(blocks) == 0 {
			return nil
		}
		return adfNode{"type": "blockquote", "content": blocks}
	case *east.Table:
		if nested {
			return adfTableText(v, src)
		}
		return adfTable(v, src)
	case *ast.ThematicBreak:
		return adfNode{"type": "rule"}
	case *ast.HTMLBlock:
		var sb strings.Builder
		for i := 0; i < v.Lines().Len(); i++ {
			seg := v.Lines().At(i)
			sb.Write(seg.Value(src))
		}
		if t := strings.TrimSpace(sb.String()); t != "" {
			return adfNode{"type": "paragraph", "content": []any{adfNode{"type": "text", "text": t}}}
		}
		return nil
	}
	// A block this converter does not know: its text, if it has any.
	if in := adfInlines(n, src, nil); len(in) > 0 {
		return adfNode{"type": "paragraph", "content": in}
	}
	return nil
}

// adfTable converts a GFM table. Column alignment is dropped: ADF cells carry no alignment.
func adfTable(t *east.Table, src []byte) adfNode {
	rows := []any{}
	for r := t.FirstChild(); r != nil; r = r.NextSibling() {
		cellType := "tableCell"
		if _, ok := r.(*east.TableHeader); ok {
			cellType = "tableHeader"
		}
		cells := []any{}
		for c := r.FirstChild(); c != nil; c = c.NextSibling() {
			para := adfBlankParagraph()
			if in := adfInlines(c, src, nil); len(in) > 0 {
				para = adfNode{"type": "paragraph", "content": in}
			}
			cells = append(cells, adfNode{"type": cellType, "attrs": adfNode{}, "content": []any{para}})
		}
		if len(cells) > 0 {
			rows = append(rows, adfNode{"type": "tableRow", "content": cells})
		}
	}
	if len(rows) == 0 {
		return nil
	}
	return adfNode{"type": "table", "attrs": adfNode{"isNumberColumnEnabled": false, "layout": "default"}, "content": rows}
}

// adfTableText is a table where ADF admits none: one paragraph, a line per row, cells joined by
// " | " with their inline marks kept.
func adfTableText(t *east.Table, src []byte) adfNode {
	out := []any{}
	for r := t.FirstChild(); r != nil; r = r.NextSibling() {
		if len(out) > 0 {
			out = append(out, adfNode{"type": "hardBreak"})
		}
		for c := r.FirstChild(); c != nil; c = c.NextSibling() {
			if c != r.FirstChild() {
				out = append(out, adfNode{"type": "text", "text": " | "})
			}
			out = append(out, adfInlines(c, src, nil)...)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return adfNode{"type": "paragraph", "content": out}
}

func adfCodeBlock(n ast.Node, src []byte) adfNode {
	var sb strings.Builder
	for i := 0; i < n.Lines().Len(); i++ {
		seg := n.Lines().At(i)
		sb.Write(seg.Value(src))
	}
	node := adfNode{"type": "codeBlock"}
	if f, ok := n.(*ast.FencedCodeBlock); ok {
		if lang := string(f.Language(src)); lang != "" {
			node["attrs"] = adfNode{"language": lang}
		}
	}
	if code := strings.TrimSuffix(sb.String(), "\n"); code != "" {
		node["content"] = []any{adfNode{"type": "text", "text": code}}
	}
	return node
}

// adfInlines walks the inline children of n; marks are those of the enclosing emphasis / link.
func adfInlines(n ast.Node, src []byte, marks []adfNode) []any {
	out := []any{}
	textNode := func(s string, ms []adfNode) {
		if s == "" {
			return
		}
		t := adfNode{"type": "text", "text": s}
		if len(ms) > 0 {
			t["marks"] = adfMarks(ms)
		}
		out = append(out, t)
	}
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch v := c.(type) {
		case *ast.Text:
			textNode(adfUnescape(v.Segment.Value(src)), marks)
			if v.HardLineBreak() || v.SoftLineBreak() {
				out = append(out, adfNode{"type": "hardBreak"})
			}
		case *ast.String:
			textNode(string(v.Value), marks)
		case *ast.CodeSpan:
			textNode(adfPlainText(v, src), append(append([]adfNode{}, marks...), adfNode{"type": "code"}))
		case *east.Strikethrough:
			out = append(out, adfInlines(v, src, append(append([]adfNode{}, marks...), adfNode{"type": "strike"}))...)
		case *ast.Emphasis:
			kind := "em"
			if v.Level >= 2 {
				kind = "strong"
			}
			out = append(out, adfInlines(v, src, append(append([]adfNode{}, marks...), adfNode{"type": kind}))...)
		case *ast.Link:
			m := adfNode{"type": "link", "attrs": adfNode{"href": adfUnescape(v.Destination)}}
			out = append(out, adfInlines(v, src, append(append([]adfNode{}, marks...), m))...)
		case *ast.AutoLink:
			u := string(v.URL(src))
			if v.AutoLinkType == ast.AutoLinkEmail && !strings.HasPrefix(strings.ToLower(u), "mailto:") {
				u = "mailto:" + u
			}
			textNode(string(v.Label(src)), append(append([]adfNode{}, marks...), adfNode{"type": "link", "attrs": adfNode{"href": u}}))
		case *ast.Image:
			alt := adfPlainText(v, src)
			if alt == "" {
				alt = adfUnescape(v.Destination)
			}
			textNode(alt, append(append([]adfNode{}, marks...), adfNode{"type": "link", "attrs": adfNode{"href": adfUnescape(v.Destination)}}))
		case *ast.RawHTML:
			for i := 0; i < v.Segments.Len(); i++ {
				seg := v.Segments.At(i)
				textNode(string(seg.Value(src)), marks)
			}
		default:
			out = append(out, adfInlines(c, src, marks)...)
		}
	}
	return out
}

// adfPlainText is the concatenated text below n, for nodes whose ADF form takes no nesting.
func adfPlainText(n ast.Node, src []byte) string {
	var sb strings.Builder
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch v := c.(type) {
		case *ast.Text:
			sb.Write(v.Segment.Value(src))
		case *ast.String:
			sb.Write(v.Value)
		default:
			sb.WriteString(adfPlainText(c, src))
		}
	}
	return sb.String()
}

// adfMarks drops what ADF refuses next to a code mark (everything but a link) and repeats.
func adfMarks(ms []adfNode) []any {
	hasCode := false
	for _, m := range ms {
		if m["type"] == "code" {
			hasCode = true
		}
	}
	seen := map[string]bool{}
	out := []any{}
	for _, m := range ms {
		t, _ := m["type"].(string)
		if hasCode && t != "code" && t != "link" {
			continue
		}
		if t != "link" && seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, m)
	}
	return out
}
