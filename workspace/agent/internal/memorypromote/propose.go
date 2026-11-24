package memorypromote

// The text side of a promotion proposal (ADR 0130 decision 11): where in a Markdown document a
// memory's text would go, the document as it would read with it, and the warnings a member needs
// before opening the pull request. Pure: the caller reads the document and writes nothing back.

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
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

// Heading is one ATX heading of a Markdown document.
type Heading struct {
	Level int
	Text  string
	Line  int // 0-based index into the document's lines
}

var (
	atxHeading = regexp.MustCompile(`^ {0,3}(#{1,6})(?:[ \t]+(.*?))?[ \t]*$`)
	atxClose   = regexp.MustCompile(`(?:^|[ \t]+)#+$`)
	fenceOpen  = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})")
)

// Headings lists the ATX headings of doc outside fenced code blocks, in order. Setext headings
// are not read: a proposal always writes ATX, and a target is named by an ATX heading.
func Headings(doc []byte) []Heading {
	var out []Heading
	fence := ""
	for i, ln := range strings.Split(string(doc), "\n") {
		ln = strings.TrimSuffix(ln, "\r")
		if fence != "" {
			// A closing fence is the same character, at least as long, and nothing after it.
			t := strings.TrimLeft(ln, " ")
			if len(ln)-len(t) <= 3 && strings.HasPrefix(t, fence) && strings.Trim(t, fence[:1]+" \t") == "" {
				fence = ""
			}
			continue
		}
		if m := fenceOpen.FindStringSubmatch(ln); m != nil {
			// A backtick fence's info string may not contain a backtick; such a line is not a fence.
			if m[1][0] != '`' || !strings.Contains(ln[strings.Index(ln, m[1])+len(m[1]):], "`") {
				fence = m[1]
				continue
			}
		}
		m := atxHeading.FindStringSubmatch(ln)
		if m == nil {
			continue
		}
		text := atxClose.ReplaceAllString(m[2], "")
		out = append(out, Heading{Level: len(m[1]), Text: strings.TrimSpace(text), Line: i})
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

// Insert returns doc with text added to the section named section: at the end of that section
// when the heading exists (before the next heading of the same or a higher level), otherwise
// under a new level-2 heading at the end of the document. text is trimmed and ends in one newline.
func Insert(doc []byte, section, text string) (out []byte, existed bool) {
	section = strings.TrimSpace(section)
	text = strings.Trim(text, "\n") + "\n"
	s := string(doc)
	h, ok := FindHeading(doc, section)
	if !ok {
		s = strings.TrimRight(s, "\n")
		if s != "" {
			s += "\n\n"
		}
		return []byte(s + "## " + section + "\n\n" + text), false
	}
	lines := strings.Split(s, "\n")
	end := len(lines)
	for _, n := range Headings(doc) {
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
	b.WriteString(text)
	if end < len(lines) {
		b.WriteString("\n")
		b.WriteString(strings.Join(lines[end:], "\n"))
	}
	return []byte(b.String()), true
}

// DefaultSection is the heading a proposal suggests when the member names none: the memory's
// description up to its first sentence or dash, cut to fit one heading line.
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
	return strings.TrimSpace(d)
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
