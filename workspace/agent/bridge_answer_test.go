package main

import (
	"reflect"
	"testing"
)

// TestBuildClaudeSingleSelectKeys pins the Go reproduction of console
// questionKeys.ts buildClaudeSeq (single-select, no free-text): per question
// Down×index + Enter, then a trailing Enter for the review page.
func TestBuildClaudeSingleSelectKeys(t *testing.T) {
	cases := []struct {
		name  string
		n     int
		picks map[int]int
		want  []string
	}{
		{"single q, first option", 1, map[int]int{0: 0}, []string{"Enter", "Enter"}},
		{"single q, third option", 1, map[int]int{0: 2}, []string{"Down", "Down", "Enter", "Enter"}},
		{"two q", 2, map[int]int{0: 1, 1: 0}, []string{"Down", "Enter", "Enter", "Enter"}},
		{"three q", 3, map[int]int{0: 0, 1: 2, 2: 1},
			[]string{"Enter", "Down", "Down", "Enter", "Down", "Enter", "Enter"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := buildClaudeSingleSelectKeys(tc.n, tc.picks); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("keys=%v, want %v", got, tc.want)
			}
		})
	}
}

// TestDecisionKeys pins the permission/plan sequences to exactly what MirrorView
// drives (option order is claude-version-verified there).
func TestDecisionKeys(t *testing.T) {
	if got := permKeys("allow"); !reflect.DeepEqual(got, []string{"Enter"}) {
		t.Errorf("perm allow=%v", got)
	}
	if got := permKeys("deny"); !reflect.DeepEqual(got, []string{"Down", "Down", "Enter"}) {
		t.Errorf("perm deny=%v", got)
	}
	if got := planKeys("approve"); !reflect.DeepEqual(got, []string{"Enter"}) {
		t.Errorf("plan approve=%v", got)
	}
	if got := planKeys("reject"); !reflect.DeepEqual(got, []string{"Down", "Down", "Down", "Enter"}) {
		t.Errorf("plan reject=%v", got)
	}
}
