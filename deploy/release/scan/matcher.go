package main

import (
	"crypto/sha256"
	"sort"
	"unicode"
	"unicode/utf8"
)

// Matcher streams bytes through the canonical folding (see Canonical) and slides
// one window per distinct term length over the resulting rune stream.
//
// Cost per rune is a handful of multiply-adds plus one bitmap probe, which is
// what makes it viable to fold every byte of a ~3 GiB expanded image set. A
// window is only hashed with sha256 once the 64-bit Rabin-Karp value survives
// the bitmap, so the expensive path runs a few times per gigabyte at most.
type Matcher struct {
	lens   []int    // distinct window lengths, ascending
	maxLen int      // == lens[len(lens)-1]
	pow    []uint64 // pow[i] = rkBase^(lens[i]-1)
	h      []uint64 // rolling value per length

	bloom [1024]uint64 // 65536-bit prefilter over rk & 0xffff
	byRK  map[uint64][]Entry

	ring    []rune  // last maxLen+1 folded runes
	offRing []int64 // byte offset each ring rune started at
	ringLen int
	n       int64 // folded runes seen so far in this stream

	carry    [4]byte // partial UTF-8 sequence across Write boundaries
	carryN   int
	byteOff  int64 // offset of the next input byte
	pendSep  bool  // a separator run is open, emit one space before the next rune
	emitted  bool  // at least one rune emitted (suppresses a leading space)
	onHit    func(off int64, e Entry)
	hitCount int
}

// NewMatcher builds a matcher for the ledger. onHit is called at most once per
// (offset, entry); the caller decides how to report and whether to keep going.
func NewMatcher(entries []Entry, onHit func(off int64, e Entry)) *Matcher {
	lenSet := map[int]bool{}
	byRK := map[uint64][]Entry{}
	for _, e := range entries {
		lenSet[e.Len] = true
		byRK[e.RK] = append(byRK[e.RK], e)
	}
	lens := make([]int, 0, len(lenSet))
	for l := range lenSet {
		lens = append(lens, l)
	}
	sort.Ints(lens)

	m := &Matcher{
		lens:   lens,
		maxLen: lens[len(lens)-1],
		byRK:   byRK,
		onHit:  onHit,
	}
	m.pow = make([]uint64, len(lens))
	m.h = make([]uint64, len(lens))
	for i, l := range lens {
		p := uint64(1)
		for k := 0; k < l-1; k++ {
			p *= rkBase
		}
		m.pow[i] = p
	}
	for _, e := range entries {
		m.bloom[(e.RK&0xffff)>>6] |= 1 << (e.RK & 63)
	}
	// One slot more than the longest window, so the rune leaving a window is
	// never the slot the new rune overwrites — that lets push do a single pass.
	m.ringLen = m.maxLen + 1
	m.ring = make([]rune, m.ringLen)
	m.offRing = make([]int64, m.ringLen)
	return m
}

// Reset starts a new independent stream. Windows never span two files, so every
// leaf gets a reset — otherwise the tail of one file and the head of the next
// could fabricate a match.
func (m *Matcher) Reset() {
	for i := range m.h {
		m.h[i] = 0
	}
	m.n = 0
	m.carryN = 0
	m.byteOff = 0
	m.pendSep = false
	m.emitted = false
}

// Write implements io.Writer so a matcher can be the sink of any decompressor.
// A rune split across two chunks is carried over; an invalid byte decodes to
// RuneError, which folds to a separator like any other non-letter.
func (m *Matcher) Write(p []byte) (int, error) {
	n := len(p)
	for m.carryN > 0 && len(p) > 0 {
		k := copy(m.carry[m.carryN:], p)
		p = p[k:]
		m.carryN += k
		for m.carryN > 0 && (m.carryN == 4 || utf8.FullRune(m.carry[:m.carryN])) {
			r, sz := utf8.DecodeRune(m.carry[:m.carryN])
			m.feed(r, m.byteOff)
			m.byteOff += int64(sz)
			copy(m.carry[:], m.carry[sz:m.carryN])
			m.carryN -= sz
		}
	}
	for len(p) > 0 {
		if b := p[0]; b < utf8.RuneSelf {
			m.feed(rune(b), m.byteOff)
			m.byteOff++
			p = p[1:]
			continue
		}
		if len(p) < 4 && !utf8.FullRune(p) {
			m.carryN = copy(m.carry[:], p)
			break
		}
		r, sz := utf8.DecodeRune(p)
		m.feed(r, m.byteOff)
		m.byteOff += int64(sz)
		p = p[sz:]
	}
	return n, nil
}

// asciiFold[c] is the folded form of an ASCII byte, or 0 when it is a separator.
// Artifact bytes are overwhelmingly ASCII, so this table keeps the hot path off
// the Unicode range tables.
var asciiFold = func() (t [utf8.RuneSelf]byte) {
	for c := 0; c < utf8.RuneSelf; c++ {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			t[c] = byte(c)
		case c >= 'A' && c <= 'Z':
			t[c] = byte(c - 'A' + 'a')
		}
	}
	return
}()

func (m *Matcher) feed(r rune, off int64) {
	var folded rune
	switch {
	case r < utf8.RuneSelf:
		c := asciiFold[r]
		if c == 0 {
			m.pendSep = true
			return
		}
		folded = rune(c)
	case unicode.IsLetter(r), unicode.IsDigit(r):
		folded = unicode.ToLower(r)
	default:
		m.pendSep = true
		return
	}
	if m.pendSep {
		m.pendSep = false
		if m.emitted {
			m.push(' ', off)
		}
	}
	m.emitted = true
	m.push(folded, off)
}

// push folds one rune into every window. This runs once per letter or digit in
// every byte we ship, so it stays a single pass: roll each hash, probe the
// bitmap, and only on a survivor pay for the map and the sha256.
func (m *Matcher) push(r rune, off int64) {
	rl := int64(m.ringLen)
	idx := int(m.n % rl)
	m.ring[idx] = r
	m.offRing[idx] = off
	m.n++

	for i, l := range m.lens {
		ln := int64(l)
		if m.n <= ln {
			// Window not full yet: keep accumulating.
			m.h[i] = m.h[i]*rkBase + uint64(r)
			if m.n < ln {
				continue
			}
		} else {
			old := m.ring[int((m.n-1-ln)%rl)]
			m.h[i] = (m.h[i]-uint64(old)*m.pow[i])*rkBase + uint64(r)
		}
		h := m.h[i]
		if m.bloom[(h&0xffff)>>6]&(1<<(h&63)) == 0 {
			continue
		}
		cands, ok := m.byRK[h]
		if !ok {
			continue
		}
		var w string
		for _, e := range cands {
			if e.Len != l {
				continue
			}
			if w == "" {
				w = m.window(l)
			}
			if sha256.Sum256([]byte(w)) == e.Sum {
				m.hitCount++
				m.onHit(m.offRing[int((m.n-ln)%rl)], e)
			}
		}
	}
}

// window returns the last l folded runes in order.
func (m *Matcher) window(l int) string {
	out := make([]rune, l)
	start := m.n - int64(l)
	for k := 0; k < l; k++ {
		out[k] = m.ring[int((start+int64(k))%int64(m.ringLen))]
	}
	return string(out)
}
