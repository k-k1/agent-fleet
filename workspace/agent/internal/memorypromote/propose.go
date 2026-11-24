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

// Headings lists the headings of doc as CommonMark reads them: a line that only looks like a
// heading inside a fenced or indented code block, an HTML block or a comment is not one. An empty
// heading (a bare "##") has no position in the parse and is not listed.
func Headings(doc []byte) []Heading {
	root := goldmark.New().Parser().Parse(text.NewReader(doc))
	var out []Heading
	_ = ast.Walk(root, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		h, ok := n.(*ast.Heading)
		if !entering || !ok {
			return ast.WalkContinue, nil
		}
		lines := h.Lines()
		if lines.Len() == 0 {
			return ast.WalkSkipChildren, nil
		}
		var b strings.Builder
		for i := 0; i < lines.Len(); i++ {
			seg := lines.At(i)
			b.Write(seg.Value(doc))
		}
		start := lines.At(0).Start
		out = append(out, Heading{Level: h.Level, Text: strings.TrimSpace(b.String()), Line: bytes.Count(doc[:start], []byte("\n"))})
		return ast.WalkSkipChildren, nil
	})
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

// ErrNotPlaced is returned by Insert when the section, after the insertion, is not a heading of
// the document (an unclosed code fence at the end swallows an appended section).
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
	// The section must still be a heading once the text is in, and the text must follow it.
	if after, found := FindHeading([]byte(res), section); !found || ok && after.Line != h.Line {
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
