package gitx

import (
	"reflect"
	"testing"
)

func TestParseDecorate(t *testing.T) {
	// Full-refname form (--decorate=full), current branch via "HEAD ->".
	refs, cur := parseDecorate("HEAD -> refs/heads/refactor/x, refs/remotes/origin/refactor/x, refs/remotes/origin/HEAD")
	if cur != "refactor/x" {
		t.Fatalf("current = %q, want refactor/x", cur)
	}
	want := []graphRef{{Name: "refactor/x", Type: "head"}, {Name: "origin/refactor/x", Type: "remote"}}
	if !reflect.DeepEqual(refs, want) {
		t.Fatalf("refs = %+v, want %+v (origin/HEAD dropped)", refs, want)
	}

	// The regression: a NON-current local branch whose name contains "/" must classify
	// as a head, not a remote (short form couldn't tell "feat/x" from "origin/feat/x").
	refs, cur = parseDecorate("refs/remotes/origin/feat/assistant-chat, refs/heads/feat/assistant-chat")
	if cur != "" {
		t.Fatalf("current = %q, want empty (no HEAD ->)", cur)
	}
	want = []graphRef{{Name: "origin/feat/assistant-chat", Type: "remote"}, {Name: "feat/assistant-chat", Type: "head"}}
	if !reflect.DeepEqual(refs, want) {
		t.Fatalf("refs = %+v, want %+v", refs, want)
	}

	refs, _ = parseDecorate("refs/remotes/origin/main, refs/remotes/origin/HEAD, refs/heads/main, refs/tags/v1.0")
	want = []graphRef{{Name: "origin/main", Type: "remote"}, {Name: "main", Type: "head"}, {Name: "v1.0", Type: "tag"}}
	if !reflect.DeepEqual(refs, want) {
		t.Fatalf("refs = %+v, want %+v", refs, want)
	}

	// What git ACTUALLY emits under --decorate=full: the "tag: " marker rides on top of
	// the full refname. The chip used to show the raw "refs/tags/v0.5.0".
	refs, _ = parseDecorate("tag: refs/tags/v0.5.0")
	want = []graphRef{{Name: "v0.5.0", Type: "tag"}}
	if !reflect.DeepEqual(refs, want) {
		t.Fatalf("refs = %+v, want %+v", refs, want)
	}

	// Short-form fallback still classifies sanely (plain name → head, a/b → remote, tag:).
	refs, cur = parseDecorate("HEAD -> main, origin/main, tag: v1.0")
	if cur != "main" {
		t.Fatalf("current = %q, want main", cur)
	}
	want = []graphRef{{Name: "main", Type: "head"}, {Name: "origin/main", Type: "remote"}, {Name: "v1.0", Type: "tag"}}
	if !reflect.DeepEqual(refs, want) {
		t.Fatalf("refs = %+v, want %+v", refs, want)
	}

	if refs, cur := parseDecorate(""); len(refs) != 0 || cur != "" {
		t.Fatalf("empty decoration → %+v %q, want none", refs, cur)
	}
}
