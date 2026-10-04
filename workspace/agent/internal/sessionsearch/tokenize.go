package sessionsearch

import (
	"errors"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
	"golang.org/x/text/width"
)

// FTS5's unicode61 tokenizer treats a run of CJK characters as ONE token, and its trigram
// tokenizer never matches a query shorter than three characters — measured on the bundled
// SQLite 3.53.2: a trigram table returns 0 rows for "認証" in "前回の認証エラーを直した".
// Two-character words are the common case in Japanese, so CJK runs are cut into overlapping
// bigrams here, in Go, before either the text or the query reaches unicode61. The same
// function cuts both sides; a query cut differently from the text matches nothing.

// maxQueryTerms bounds what one search can ask FTS5 to intersect.
const maxQueryTerms = 16

// errEmptyQuery is a query with nothing left to search for once it is tokenised.
var errEmptyQuery = errors.New("query has no searchable terms")

// isCJK reports whether r belongs to a script written without spaces between words.
// U+30FC (the katakana-hiragana prolonged sound mark) and U+3005 (the ideographic iteration
// mark) are script Common, but they sit inside words, so a run must not break on them.
func isCJK(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) ||
		r == 'ー' || r == '々'
}

// fold maps fullwidth Latin and halfwidth katakana onto their usual forms, so "ＡＰＩ"
// and "API", or "ﾊﾞｸﾞ" and "バグ", are one word to the index. NFC is not optional: width.Fold
// turns a halfwidth voiced kana into the base kana plus a combining mark, and the mark, which is
// not CJK, would cut the run in two.
func fold(s string) string { return norm.NFC.String(width.Fold.String(s)) }

// IndexText rewrites s into what the FTS column stores: CJK runs become space-separated
// bigrams (a one-character run stays a unigram) and every other character passes through
// for unicode61 to tokenise as usual.
func IndexText(s string) string {
	var b strings.Builder
	b.Grow(len(s) * 2)
	var run []rune
	flush := func() {
		switch len(run) {
		case 0:
			return
		case 1:
			b.WriteByte(' ')
			b.WriteRune(run[0])
		default:
			for i := 0; i+1 < len(run); i++ {
				b.WriteByte(' ')
				b.WriteRune(run[i])
				b.WriteRune(run[i+1])
			}
		}
		b.WriteByte(' ')
		run = run[:0]
	}
	for _, r := range fold(s) {
		if isCJK(r) {
			run = append(run, r)
			continue
		}
		flush()
		b.WriteRune(r)
	}
	flush()
	return b.String()
}

// Terms splits a user's query on whitespace (including the ideographic space) and drops
// duplicates. A trailing '*' on a term asks for a prefix match.
func Terms(q string) []string {
	var out []string
	seen := map[string]bool{}
	for _, f := range strings.FieldsFunc(q, unicode.IsSpace) {
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
		if len(out) == maxQueryTerms {
			break
		}
	}
	return out
}

// MatchQuery builds the FTS5 MATCH expression for q: every term must occur (implicit AND),
// and each term is one quoted phrase of its IndexText form, so a CJK word matches only where its
// bigrams sit next to each other. Splitting inside the quotes is left to unicode61 itself, the
// same tokenizer that split the stored text; a Go copy of its rules drifts (it once dropped
// "Ⅲ" and "①"). User input never reaches FTS5 syntax unquoted — an operator or a stray quote
// would otherwise be a syntax error, or worse, a different query.
func MatchQuery(q string) (string, error) {
	var parts []string
	for _, term := range Terms(q) {
		prefix := strings.HasSuffix(term, "*")
		term = strings.TrimRight(term, "*")
		toks := ftsTokens(term)
		if len(toks) == 0 {
			continue
		}
		phrase := `"` + strings.ReplaceAll(strings.Join(strings.Fields(IndexText(term)), " "), `"`, `""`) + `"`
		// A single CJK character is stored only inside bigrams, so it is asked for as a prefix:
		// it then matches every bigram it starts. It misses the character at the end of a run.
		lone := len(toks) == 1 && len([]rune(toks[0])) == 1 && isCJK([]rune(toks[0])[0])
		if prefix || lone {
			phrase += "*"
		}
		parts = append(parts, phrase)
	}
	if len(parts) == 0 {
		return "", errEmptyQuery
	}
	return strings.Join(parts, " "), nil
}

// ftsTokens approximates how unicode61 splits IndexText's output — its token characters are the
// categories L*, N* and Co — for the two questions MatchQuery asks itself: whether a term has
// anything to search for, and whether it is one lone CJK character. The phrase itself is split
// by SQLite.
func ftsTokens(term string) []string {
	return strings.FieldsFunc(IndexText(term), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r) && !unicode.Is(unicode.Co, r)
	})
}
