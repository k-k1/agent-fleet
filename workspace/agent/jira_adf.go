package main

// Markdown to Atlassian Document Format, for `workspace-agent jira-comment edit`.
//
// jira-cli sends bodies as v2 wiki markup through a converter that glues a list, heading, code
// block or quote onto a plain paragraph before it (#2476). An edit is how such a comment gets
// repaired, so its body goes through goldmark and REST v3 instead, and the paragraph-then-list
// case comes out as a paragraph followed by a bulletList.
//
// Anything without an ADF counterpart (a table, raw HTML, an image) degrades to its text rather
// than failing: a comment with a rough edge beats no repair.

import (
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

type adfNode = map[string]any

// jiraMarkdownADF converts a Markdown body to an ADF document. The result is never an empty
// document: Jira answers 400 for one.
func jiraMarkdownADF(md string) adfNode {
	src := []byte(strings.ReplaceAll(md, "\r\n", "\n"))
	root := goldmark.DefaultParser().Parse(text.NewReader(src))
	content := adfBlocks(root, src)
	if len(content) == 0 {
		content = []any{adfNode{"type": "paragraph", "content": []any{adfNode{"type": "text", "text": " "}}}}
	}
	return adfNode{"version": 1, "type": "doc", "content": content}
}

func adfBlocks(parent ast.Node, src []byte) []any {
	out := []any{}
	for c := parent.FirstChild(); c != nil; c = c.NextSibling() {
		if b := adfBlock(c, src); b != nil {
			out = append(out, b)
		}
	}
	return out
}

func adfBlock(n ast.Node, src []byte) adfNode {
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
			blocks := adfBlocks(c, src)
			if len(blocks) == 0 {
				blocks = []any{adfNode{"type": "paragraph", "content": []any{adfNode{"type": "text", "text": " "}}}}
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
		blocks := adfBlocks(v, src)
		if len(blocks) == 0 {
			return nil
		}
		return adfNode{"type": "blockquote", "content": blocks}
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
			textNode(string(v.Segment.Value(src)), marks)
			if v.HardLineBreak() || v.SoftLineBreak() {
				out = append(out, adfNode{"type": "hardBreak"})
			}
		case *ast.String:
			textNode(string(v.Value), marks)
		case *ast.CodeSpan:
			textNode(adfPlainText(v, src), append(append([]adfNode{}, marks...), adfNode{"type": "code"}))
		case *ast.Emphasis:
			kind := "em"
			if v.Level >= 2 {
				kind = "strong"
			}
			out = append(out, adfInlines(v, src, append(append([]adfNode{}, marks...), adfNode{"type": kind}))...)
		case *ast.Link:
			m := adfNode{"type": "link", "attrs": adfNode{"href": string(v.Destination)}}
			out = append(out, adfInlines(v, src, append(append([]adfNode{}, marks...), m))...)
		case *ast.AutoLink:
			u := string(v.URL(src))
			textNode(string(v.Label(src)), append(append([]adfNode{}, marks...), adfNode{"type": "link", "attrs": adfNode{"href": u}}))
		case *ast.Image:
			alt := adfPlainText(v, src)
			if alt == "" {
				alt = string(v.Destination)
			}
			textNode(alt, append(append([]adfNode{}, marks...), adfNode{"type": "link", "attrs": adfNode{"href": string(v.Destination)}}))
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
