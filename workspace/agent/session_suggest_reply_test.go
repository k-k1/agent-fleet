package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestCleanSuggestedReplies(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"plain lines", "進めて\nそれでOK\nいったん待って", []string{"進めて", "それでOK", "いったん待って"}},
		{"strips numbering and bullets", "1. 進めて\n- それでOK\n・待って", []string{"進めて", "それでOK", "待って"}},
		{"strips quotes", "「進めて」\n\"OK\"", []string{"進めて", "OK"}},
		{"dedupes case-insensitively", "OK\nok\n進めて", []string{"OK", "進めて"}},
		{"drops blanks", "\n進めて\n\n\nOK\n", []string{"進めて", "OK"}},
		{"caps at three", "a\nb\nc\nd\ne", []string{"a", "b", "c"}},
		{"drops overlong lines", strings.Repeat("x", 60) + "\nOK", []string{"OK"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := cleanSuggestedReplies(c.in)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("cleanSuggestedReplies(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}
