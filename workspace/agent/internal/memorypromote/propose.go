package memorypromote

// The text side of a promotion proposal (ADR 0130 decision 11): where in a Markdown document a
// memory's text would go, the document as it would read with it, and the warnings a member needs
// before opening the pull request. Pure: the caller reads the document and writes nothing back.

import (
	"bytes"
	"errors"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// MaxSection bounds a section heading, in runes: it is shown on one line and stored as the
// heading of a document target.
const MaxSection = 200

// Warnings a proposal carries. None stops the proposal: each is something the member (or the
// session asked to open the pull request) has to look at before committing.
const (
	// WarnLanguage: the memory is written in another script than the document, so the text has to
	// be rewritten in the repository's language first.
	WarnLanguage = "language-differs"
	// WarnMemoryLinks: the text links other memories with [[name]], which no reader of the
	// repository can follow.
	WarnMemoryLinks = "memory-links"
	// WarnNewFile: the document does not exist on the default branch; the proposal creates it.
	WarnNewFile = "new-file"
)

// Heading is one heading of a Markdown document.
type Heading struct {
	Level int
	// Text is the heading's raw content: for an ATX heading without the opening and closing #
	// sequences, trimmed. Inline markup is kept as written.
	Text string
	Line int // 0-based index of the heading's first line
}

// lineIndex answers the 0-based line of a byte offset, by a binary search over the line starts
// computed once.
type lineIndex []int

func newLineIndex(doc []byte) lineIndex {
	idx := lineIndex{0}
	for i, c := range doc {
		if c == '\n' {
			idx = append(idx, i+1)
		}
	}
	return idx
}

func (l lineIndex) line(off int) int {
	lo, hi := 0, len(l)
	for lo+1 < hi {
		m := (lo + hi) / 2
		if l[m] <= off {
			lo = m
		} else {
			hi = m
		}
	}
	return lo
}

// block is one top-level block of a document: its kind and the line it starts on.
type block struct {
	heading *Heading
	start   int
}

// topBlocks parses doc and lists its top-level blocks that have a position, in order. A heading
// inside a list or a block quote is not a top-level block, so it is never a target: text inserted
// "after" it by line would land outside the container.
func topBlocks(doc []byte) []block {
	root := goldmark.New().Parser().Parse(text.NewReader(doc))
	idx := newLineIndex(doc)
	var out []block
	for n := root.FirstChild(); n != nil; n = n.NextSibling() {
		first := n
		for first != nil && first.Type() != ast.TypeBlock || first != nil && first.Lines().Len() == 0 && first.FirstChild() != nil {
			// A container (list, block quote) starts where its first leaf block does.
			first = first.FirstChild()
		}
		if first == nil || first.Lines().Len() == 0 {
			continue
		}
		start := idx.line(first.Lines().At(0).Start)
		if n.Kind() == ast.KindFencedCodeBlock {
			start-- // the opening fence is the line before the first content line
		}
		bl := block{start: start}
		if h, ok := n.(*ast.Heading); ok {
			var b strings.Builder
			for i := 0; i < h.Lines().Len(); i++ {
				seg := h.Lines().At(i)
				b.Write(seg.Value(doc))
			}
			bl.heading = &Heading{Level: h.Level, Text: strings.TrimSpace(b.String()), Line: start}
		}
		out = append(out, bl)
	}
	return out
}

// Headings lists the top-level headings of doc as CommonMark reads them: a line that only looks
// like a heading inside a code block, an HTML block, a comment, a list or a block quote is not
// one. An empty heading (a bare "##") has no position in the parse and is not listed.
func Headings(doc []byte) []Heading {
	var out []Heading
	for _, b := range topBlocks(doc) {
		if b.heading != nil {
			out = append(out, *b.heading)
		}
	}
	return out
}

// FindHeading returns the first heading whose text is section, compared after trimming.
func FindHeading(doc []byte, section string) (Heading, bool) {
	section = strings.TrimSpace(section)
	for _, h := range Headings(doc) {
		if h.Text == section {
			return h, true
		}
	}
	return Heading{}, false
}

// SectionOK says whether section is a heading text that survives the round trip: written as an
// ATX heading, it is read back as exactly that text. "Release #" is not (the # closes the
// heading), nor is text that starts a block of its own.
func SectionOK(section string) bool {
	if section == "" || section != strings.TrimSpace(section) || strings.ContainsAny(section, "\r\n") ||
		utf8.RuneCountInString(section) > MaxSection {
		return false
	}
	hs := Headings([]byte("## " + section + "\n"))
	return len(hs) == 1 && hs[0].Level == 2 && hs[0].Text == section
}

// ErrNotPlaced is returned by Insert when the text would not stand as its own blocks in the
// section: an unclosed code fence before it would swallow it, or one in it would swallow the
// headings after it.
var ErrNotPlaced = errors.New("the section would not be a heading of the document")

// Insert returns doc with text added to the section named section: at the end of that section
// when the heading exists (before the next heading of the same or a higher level), otherwise
// under a new level-2 heading at the end of the document. Only blank lines around text are
// removed; its indentation is kept. A document whose every line ends in CRLF gets CRLF lines.
func Insert(doc []byte, section, body string) (out []byte, existed bool, err error) {
	section = strings.TrimSpace(section)
	crlf := bytes.Contains(doc, []byte("\r\n")) && bytes.Count(doc, []byte("\r\n")) == bytes.Count(doc, []byte("\n"))
	src := doc
	if crlf {
		src = bytes.ReplaceAll(doc, []byte("\r\n"), []byte("\n"))
	}
	body = TrimBlankLines(strings.ReplaceAll(body, "\r\n", "\n")) + "\n"
	var res string
	h, ok := FindHeading(src, section)
	if !ok {
		s := strings.TrimRight(string(src), "\n")
		if s != "" {
			s += "\n\n"
		}
		res = s + "## " + section + "\n\n" + body
	} else {
		lines := strings.Split(string(src), "\n")
		end := len(lines)
		for _, n := range Headings(src) {
			if n.Line > h.Line && n.Level <= h.Level {
				end = n.Line
				break
			}
		}
		// The section's trailing blank lines are replaced by exactly one before the next heading.
		last := end
		for last > h.Line+1 && strings.TrimSpace(lines[last-1]) == "" {
			last--
		}
		var b strings.Builder
		b.WriteString(strings.Join(lines[:last], "\n"))
		b.WriteString("\n\n")
		b.WriteString(body)
		if end < len(lines) {
			b.WriteString("\n")
			b.WriteString(strings.Join(lines[end:], "\n"))
		}
		res = b.String()
	}
	if !placed(src, []byte(res), section, ok, bodyLine(res, body)) {
		return nil, ok, ErrNotPlaced
	}
	if crlf {
		res = strings.ReplaceAll(res, "\n", "\r\n")
	}
	return []byte(res), ok, nil
}

// TrimBlankLines removes the blank lines before and after s, and trailing whitespace at its end;
// the indentation of its first line is kept (it may open an indented code block).
func TrimBlankLines(s string) string {
	lines := strings.Split(s, "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.TrimRight(strings.Join(lines, "\n"), " \t")
}

// DefaultSection is the heading a proposal suggests when the member names none: the memory's
// description up to its first sentence or dash, cut to fit one heading line, or the memory's name
// when that would not read back as the same heading.
func DefaultSection(description, name string) string {
	d := strings.TrimSpace(description)
	for _, sep := range []string{"。", ". ", " — ", " – ", ": ", "："} {
		if i := strings.Index(d, sep); i > 0 {
			d = d[:i]
		}
	}
	d = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, d))
	if d == "" {
		d = name
	}
	if utf8.RuneCountInString(d) > 80 {
		d = string([]rune(d)[:80])
	}
	// What the member is shown must be what the merged document will carry.
	if d = strings.TrimSpace(strings.Trim(d, "# \t")); !SectionOK(d) {
		d = name
	}
	return d
}

// cjkShare is the share of letters in s that are Han, kana or Hangul, and how many letters there
// are.
func cjkShare(s string) (float64, int) {
	letters, cjk := 0, 0
	for _, r := range s {
		if !unicode.IsLetter(r) {
			continue
		}
		letters++
		if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) {
			cjk++
		}
	}
	if letters == 0 {
		return 0, 0
	}
	return float64(cjk) / float64(letters), letters
}

// LanguageDiffers says whether text and doc are written in visibly different scripts: one mostly
// CJK, the other almost not at all. A short or empty document is not judged.
func LanguageDiffers(text string, doc []byte) bool {
	ts, tn := cjkShare(text)
	ds, dn := cjkShare(string(doc))
	if tn < 20 || dn < 200 {
		return false
	}
	return ts >= 0.2 && ds < 0.05 || ts < 0.05 && ds >= 0.2
}

var memoryLink = regexp.MustCompile(`\[\[[^\[\]\n]+\]\]`)

// HasMemoryLinks says whether text links a memory with [[name]].
func HasMemoryLinks(text string) bool { return memoryLink.MatchString(text) }

// bodyLine is the line the inserted body starts on in res: the body is followed by what came
// after the section, so its last occurrence before that is the one inserted.
func bodyLine(res, body string) int {
	i := strings.LastIndex(res, "\n"+strings.TrimRight(body, "\n")+"\n")
	if i < 0 {
		return -1
	}
	return strings.Count(res[:i+1], "\n")
}

// placed checks an insertion on the parsed result: the headings are the old ones (plus the new
// section) in the same order, the section is one of them, and a top-level block starts exactly on
// the body's first line, after the section's heading and before the next heading of its level.
func placed(old, res []byte, section string, existed bool, line int) bool {
	if line < 0 {
		return false
	}
	before, after := Headings(old), Headings(res)
	if !existed {
		before = append(before, Heading{Level: 2, Text: section})
	}
	if len(before) != len(after) {
		return false
	}
	at := -1
	for i := range before {
		if before[i].Text != after[i].Text || before[i].Level != after[i].Level {
			return false
		}
		if after[i].Text == section && at < 0 {
			at = i
		}
	}
	if at < 0 || after[at].Line >= line {
		return false
	}
	for _, n := range after[at+1:] {
		if n.Level <= after[at].Level {
			if n.Line <= line {
				return false
			}
			break
		}
	}
	for _, b := range topBlocks(res) {
		if b.start == line {
			return true
		}
	}
	return false
}
