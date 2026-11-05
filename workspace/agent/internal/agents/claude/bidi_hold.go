package claude

import (
	"strings"
	"unicode"
)

// StripHeldBidi removes the bidirectional controls claude would remove on submit, before the
// prompt is typed.
//
// claude (2.1.282+) strips invisible characters from input, and when one of them is a bidi
// control it does not submit: the Enter only shows "Removed 1 invisible character · review
// and press Enter to send" and a second Enter is needed (the 2.1.318 bundle holds whenever
// the removed set counts a "bidi" class character). The Agent sends one Enter, so a prompt
// pasted from a document with an LRM mark stayed in the input box, never answered, and the
// mirror showed it as Pending. Removing them first leaves claude nothing to hold for, and
// changes nothing it would have kept:
//
//   - The embeddings, overrides and isolates (U+202A–U+202E, U+2066–U+2069) are always
//     removed by claude.
//   - LRM, RLM and ALM (U+200E, U+200F, U+061C) are kept by claude in some right-to-left
//     contexts. They are removed here only on a line with no right-to-left script at all,
//     where claude always removes them; on any other line they are left to claude.
//
// Other invisible characters (a zero-width space, a BOM) are stripped by claude without a
// hold and are left alone. Idempotent.
func StripHeldBidi(text string) string {
	if !strings.ContainsFunc(text, isHeldBidi) {
		return text
	}
	var b strings.Builder
	b.Grow(len(text))
	for line := range strings.SplitAfterSeq(text, "\n") {
		rtl := strings.ContainsFunc(line, rightToLeftScript)
		for _, r := range line {
			if isHeldBidi(r) && (!isDirectionMark(r) || !rtl) {
				continue
			}
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ShapePrompt is the form a prompt is typed into claude in: held bidi controls removed, then
// the attachment line's image paths quoted. Whatever compares the typed text against the
// transcript or the pane compares this form.
func ShapePrompt(text string) string {
	return QuoteImagePaths(StripHeldBidi(text))
}

// isHeldBidi reports the characters claude's stripper counts as "bidi", the class that makes
// it hold the submit.
func isHeldBidi(r rune) bool {
	return isDirectionMark(r) || r >= 0x202a && r <= 0x202e || r >= 0x2066 && r <= 0x2069
}

func isDirectionMark(r rune) bool {
	return r == 0x200e || r == 0x200f || r == 0x061c
}

// rightToLeftScript reports a letter or mark in the blocks claude scans before keeping a
// direction mark (Hebrew through Arabic Extended, the presentation forms, the supplementary
// RTL blocks). Only letters and marks count, as in claude: ALM itself and a BOM sit in those
// blocks too, and counting them would keep every mark on their line. It is still wider than
// claude's own letter sets on purpose: a false hit only leaves a mark to claude.
func rightToLeftScript(r rune) bool {
	inBlock := r >= 0x0590 && r <= 0x08ff || r >= 0xfb1d && r <= 0xfdff || r >= 0xfe70 && r <= 0xfeff ||
		r >= 0x10800 && r <= 0x10fff || r >= 0x1e800 && r <= 0x1efff
	return inBlock && unicode.In(r, unicode.L, unicode.M)
}

// WithoutInvisible removes every character claude 2.1.318's input stripper may remove (the set
// console/src/lib/pastedImages.ts CLAUDE_INVISIBLE_RE carries; keep the two in step): C0/C1
// controls but tab and line feed, soft hyphen, zero-width and bidi controls, variation selectors,
// BOM, tag characters and the blank fillers. It drops the ones claude keeps in context (a joiner
// inside an emoji) as well, so it is for comparing a text with claude's copy of it, never for
// rewriting what is sent.
func WithoutInvisible(s string) string {
	return strings.Map(func(r rune) rune {
		if claudeInvisible(r) {
			return -1
		}
		return r
	}, s)
}

func claudeInvisible(r rune) bool {
	switch {
	case r <= 0x08, r >= 0x0b && r <= 0x1f, r >= 0x7f && r <= 0x9f:
		return true
	case r == 0x00ad, r == 0x034f, r == 0x061c, r == 0x115f, r == 0x1160, r == 0x17b4, r == 0x17b5,
		r >= 0x180b && r <= 0x180f, r >= 0x200b && r <= 0x200f, r >= 0x2028 && r <= 0x202e,
		r >= 0x2060 && r <= 0x206f, r == 0x3164, r >= 0xfe00 && r <= 0xfe0f, r == 0xfeff, r == 0xffa0,
		r >= 0xfff0 && r <= 0xfffb:
		return true
	case r == 0x1107f, r >= 0x13430 && r <= 0x1343f, r == 0x16fe4, r >= 0x1bca0 && r <= 0x1bca3,
		r >= 0x1d173 && r <= 0x1d17a, r >= 0xe0000 && r <= 0xe0fff:
		return true
	}
	return false
}
