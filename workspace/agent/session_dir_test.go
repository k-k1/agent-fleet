package main

import (
	"reflect"
	"testing"
)

// TestSessionsInDir covers the branch-switch guard's core: which running sessions
// count as "occupying" a working copy. dir equality and strict-subdir match; siblings
// with a shared prefix, archived, stopped, and parent-dir sessions must not.
func TestSessionsInDir(t *testing.T) {
	metas := []sessionMeta{
		{Name: "a", Dir: "/repos/foo", Title: "root"},           // exact match, live
		{Name: "b", Dir: "/repos/foo/sub", Title: "subdir"},     // strict subdir, live
		{Name: "c", Dir: "/repos/foobar", Title: "sibling"},     // shared prefix, NOT under foo
		{Name: "d", Dir: "/repos/foo", Title: "stopped"},        // under foo but not live
		{Name: "e", Dir: "/repos/foo", Title: "archived", Archived: true},
		{Name: "f", Dir: "/repos", Title: "parent"},             // parent dir, not under foo
	}
	live := map[string]bool{"a": true, "b": true, "c": true, "e": true, "f": true} // "d" stopped

	got := sessionsInDir(metas, live, "/repos/foo")
	want := []string{"root", "subdir"} // sorted display names
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sessionsInDir = %v, want %v", got, want)
	}

	// A clean working copy (no live sessions under it) must return empty so the
	// checkout guard lets the switch through.
	if got := sessionsInDir(metas, live, "/repos/baz"); len(got) != 0 {
		t.Fatalf("sessionsInDir(clean) = %v, want empty", got)
	}
}
