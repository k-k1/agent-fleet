package main

import (
	"strconv"
	"testing"
)

func TestParseMemBytes(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"", 0, false},
		{"nonsense", 0, false},
		{"1024", 1024, true},        // bare = bytes
		{"512m", 512 * mib, true},   //
		{"8g", 8 * gib, true},       //
		{"2G", 2 * gib, true},       // case-insensitive
		{"2 GiB", 2 * gib, true},    // spacing + ib suffix
		{"1t", gib * 1024, true},    //
		{"1536m", 1536 * mib, true}, // non-power-of-two
		{"1.5g", 3 * gib / 2, true}, // fractional
		{"-4g", 0, false},           // negative rejected
	}
	for _, c := range cases {
		got, ok := parseMemBytes(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("parseMemBytes(%q) = %d,%v; want %d,%v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestFormatMemHuman(t *testing.T) {
	cases := map[int64]string{
		8 * gib:    "8g",
		512 * mib:  "512m",
		1536 * mib: "1536m",
		1024:       "1k",
		1000:       "1000",
	}
	for in, want := range cases {
		if got := formatMemHuman(in); got != want {
			t.Errorf("formatMemHuman(%d) = %q; want %q", in, got, want)
		}
	}
}
