package goldset

import (
	"regexp"
	"strings"
)

// A must_survive item is a sentence the member wrote, not a quotation, so
// "lost" cannot be a substring test. The check keeps the item's ASCII anchors
// (identifiers, flags, numbers, paths and code spans: the tokens a paraphrase
// keeps) that occur in the memory before or after the reference patch, and
// calls the item lost when the patched text no longer holds one of them. An
// item with no such anchor cannot be checked and is counted as unverifiable
// rather than passed. The reference patch itself must lose nothing; that
// calibration is part of the report.
var (
	codeSpan = regexp.MustCompile("`([^`]+)`")
	asciiRun = regexp.MustCompile(`[A-Za-z0-9_./#:=<>@+*~\-]+`)
)

// Anchors returns the checkable tokens of one must_survive item.
func Anchors(item string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(t string) {
		t = strings.Trim(t, ".,:;=-/<>*~")
		if len(t) < 3 || seen[t] {
			return
		}
		seen[t] = true
		out = append(out, t)
	}
	for _, m := range codeSpan.FindAllStringSubmatch(item, -1) {
		add(m[1])
	}
	for _, r := range asciiRun.FindAllString(item, -1) {
		add(r)
	}
	return out
}

// FactCheck is the outcome of checking a set of items against a patched text.
type FactCheck struct {
	Lost         []string // items with an anchor of the original memory missing after the patch
	NewMissing   int      // items whose only missing anchors came with the reference patch: a fix not (fully) made
	Verifiable   int      // items with at least one anchor in the original or the reference result
	Unverifiable int      // items with none
}

// CheckFacts checks items against patched. An anchor that occurs in original
// is a fact the patch must keep; one that occurs only in reference (the
// member's patched text) is a fact the fix should bring, and its absence is a
// missing fix, not a lost fact.
func CheckFacts(items []string, patched, original, reference string) FactCheck {
	var fc FactCheck
	for _, it := range items {
		lost, missing, any := false, false, false
		for _, a := range Anchors(it) {
			inOrig := strings.Contains(original, a)
			if !inOrig && !strings.Contains(reference, a) {
				continue
			}
			any = true
			if strings.Contains(patched, a) {
				continue
			}
			if inOrig {
				lost = true
			} else {
				missing = true
			}
		}
		switch {
		case !any:
			fc.Unverifiable++
			continue
		case lost:
			fc.Lost = append(fc.Lost, it)
		case missing:
			fc.NewMissing++
		}
		fc.Verifiable++
	}
	return fc
}
