package main

import (
	"reflect"
	"testing"
)

func TestParseDecorate(t *testing.T) {
	refs, cur := parseDecorate("HEAD -> refactor/x, origin/refactor/x, origin/HEAD")
	if cur != "refactor/x" {
		t.Fatalf("current = %q, want refactor/x", cur)
	}
	want := []graphRef{{Name: "refactor/x", Type: "head"}, {Name: "origin/refactor/x", Type: "remote"}}
	if !reflect.DeepEqual(refs, want) {
		t.Fatalf("refs = %+v, want %+v (origin/HEAD dropped)", refs, want)
	}

	refs, cur = parseDecorate("origin/main, origin/HEAD, main, tag: v1.0")
	if cur != "" {
		t.Fatalf("current = %q, want empty (no HEAD ->)", cur)
	}
	want = []graphRef{{Name: "origin/main", Type: "remote"}, {Name: "main", Type: "head"}, {Name: "v1.0", Type: "tag"}}
	if !reflect.DeepEqual(refs, want) {
		t.Fatalf("refs = %+v, want %+v", refs, want)
	}

	if refs, cur := parseDecorate(""); len(refs) != 0 || cur != "" {
		t.Fatalf("empty decoration → %+v %q, want none", refs, cur)
	}
}
