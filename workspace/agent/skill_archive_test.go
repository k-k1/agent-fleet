package main

import (
	"testing"
)

func TestSkillRoutesRegistered(t *testing.T) {
	h := smokeHandler(t)
	if w := smokeDo(t, h, "GET", "/agents/skills/archive", "smoke-token", ""); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if w := smokeDo(t, h, "GET", "/agents/skills/archive", "", ""); w.Code != 401 {
		t.Fatalf("unauthenticated: %d", w.Code)
	}
}

// skillRoots lists each distinct root once with every kind that shares it.
func TestSkillRootsDedupe(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	roots, kinds := skillRoots()
	if len(roots) == 0 {
		t.Fatal("no skills roots")
	}
	seen := map[string]bool{}
	for _, r := range roots {
		if seen[r] || len(kinds[r]) == 0 {
			t.Fatalf("root %q duplicated or without kinds: %v", r, kinds[r])
		}
		seen[r] = true
	}
}
