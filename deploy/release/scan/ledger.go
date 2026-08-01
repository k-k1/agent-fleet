package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The ledger stores one forbidden term per line, as hashes only — never as
// plaintext. This repository is public, so writing the terms down would publish
// exactly what the gate exists to keep out of the artifacts.
//
// Line format (fields separated by whitespace):
//
//	<sha256-hex>  <rune-length>  <rk-hex>  <id>
//
//	sha256-hex   sha256 of the canonical form (see Canonical), lowercase hex
//	rune-length  length of the canonical form in runes — the window size the
//	             scanner slides over the artifact
//	rk-hex       Rabin-Karp value of the canonical form (16 hex digits). This is
//	             the cheap prefilter; a match is only reported after the sha256
//	             confirms it
//	id           short human label used in gate output ("corp-1", "name-2", …)
//
// Generate a line with `scan --add` (reads the term from stdin so it never
// reaches a file or the shell history).

// rkBase is the Rabin-Karp base. Public and fixed: the prefilter value must be
// reproducible from the term alone.
const rkBase uint64 = 0x100000001b3

// Entry is one forbidden term, known only by its hashes.
type Entry struct {
	Sum  [32]byte
	Len  int
	RK   uint64
	ID   string
	Line int
}

// Canonical folds a term (or a window of artifact text) into the form the
// ledger hashes: every letter/digit lowercased, every run of anything else
// collapsed to a single space, ends trimmed.
//
// This is what makes the gate robust to how the term is spelled in a build
// artifact: "Example Inc.", "Example-Inc", "example_inc" and "Example\n * Inc"
// all fold to the same thing, and because the scanner slides a window over the
// same folding, a term also matches inside a longer word ("ExampleInc" folds to
// one run that contains the window "example").
func Canonical(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	sep := false
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if sep && b.Len() > 0 {
				b.WriteRune(' ')
			}
			sep = false
			b.WriteRune(unicode.ToLower(r))
			continue
		}
		sep = true
	}
	return b.String()
}

// NewEntry derives a ledger entry from a plaintext term.
func NewEntry(term, id string) (Entry, error) {
	c := Canonical(term)
	n := utf8.RuneCountInString(c)
	if n < minTermLen {
		return Entry{}, fmt.Errorf("term is too short after folding (%d runes, need >= %d): a term this generic would match unrelated text", n, minTermLen)
	}
	return Entry{
		Sum: sha256.Sum256([]byte(c)),
		Len: n,
		RK:  rkOf(c),
		ID:  id,
	}, nil
}

// minTermLen guards against a term so generic that the gate would fire on
// unrelated third-party text in the images.
const minTermLen = 5

func rkOf(canonical string) uint64 {
	var h uint64
	for _, r := range canonical {
		h = h*rkBase + uint64(r)
	}
	return h
}

// String renders the entry as a ledger line.
func (e Entry) String() string {
	return fmt.Sprintf("%s  %d  %016x  %s", hex.EncodeToString(e.Sum[:]), e.Len, e.RK, e.ID)
}

// ParseLedger reads a ledger. It fails on an empty ledger: a gate that silently
// checks nothing is worse than no gate.
func ParseLedger(r io.Reader) ([]Entry, error) {
	var out []Entry
	sc := bufio.NewScanner(r)
	ln := 0
	for sc.Scan() {
		ln++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 4 {
			return nil, fmt.Errorf("line %d: want 4 fields (sha256 len rk id), got %d", ln, len(f))
		}
		sum, err := hex.DecodeString(f[0])
		if err != nil || len(sum) != 32 {
			return nil, fmt.Errorf("line %d: field 1 is not a sha256 hex digest", ln)
		}
		n, err := strconv.Atoi(f[1])
		if err != nil || n < minTermLen {
			return nil, fmt.Errorf("line %d: field 2 is not a rune length >= %d", ln, minTermLen)
		}
		rk, err := strconv.ParseUint(f[2], 16, 64)
		if err != nil {
			return nil, fmt.Errorf("line %d: field 3 is not a 64-bit hex value", ln)
		}
		e := Entry{Len: n, RK: rk, ID: f[3], Line: ln}
		copy(e.Sum[:], sum)
		out = append(out, e)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("ledger has no entries — refusing to run a check that can never fail")
	}
	return out, nil
}
