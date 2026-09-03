package sessionx

import (
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// ssmDefaultTitle builds "{alias} @MMDD-HHMM" from the host alias, falls back to the raw
// instance target, and returns "" when neither is set.
func TestSSMDefaultTitle(t *testing.T) {
	now := time.Date(2026, 7, 23, 15, 30, 0, 0, time.UTC)
	cases := []struct{ alias, target, want string }{
		{"mng@g3prod-mon01", "i-06f4", "mng@g3prod-mon01 @0723-1530"},
		{"mng@g3prod-mon01 (prod)", "i-06f4", "mng@g3prod-mon01 (prod) @0723-1530"},
		{"  ", "i-abc", "i-abc @0723-1530"}, // blank alias → instance target
		{"", "", ""},                        // nothing known → empty (caller keeps generic fallback)
	}
	for _, c := range cases {
		if got := ssmDefaultTitle(c.alias, c.target, now); got != c.want {
			t.Fatalf("ssmDefaultTitle(%q, %q) = %q; want %q", c.alias, c.target, got, c.want)
		}
	}
}

// randSlug returns a valid, "s"-prefixed, fixed-length slug and doesn't repeat across
// a batch of draws (random, so collisions are astronomically unlikely).
func TestRandSlugFormat(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		s := randSlug()
		if len(s) != slugLen || s[0] != 's' {
			t.Fatalf("randSlug() = %q; want %d chars starting with 's'", s, slugLen)
		}
		if !session.ValidName(s) {
			t.Fatalf("randSlug() = %q; does not match session.ValidName", s)
		}
		if seen[s] {
			t.Fatalf("randSlug() repeated %q within 200 draws", s)
		}
		seen[s] = true
	}
}

// allocSessionName returns a slug that is not already claimed by a meta and (given a
// dir with no conversation on disk) passes the jsonl guard.
func TestAllocSessionNameUnused(t *testing.T) {
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir()) // empty → sessionJSONLExists is false

	s := allocSessionName(t.TempDir())
	if !session.ValidName(s) {
		t.Fatalf("allocSessionName = %q; invalid slug", s)
	}
	if _, ok := session.ReadMeta(s); ok {
		t.Fatalf("allocSessionName returned an already-taken slug %q", s)
	}
}

// slugTaken reflects an existing meta.
func TestSlugTaken(t *testing.T) {
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	session.WriteMeta(session.Meta{Name: "staken1"})
	if !slugTaken("staken1") {
		t.Fatalf("slugTaken(existing meta) = false; want true")
	}
	if slugTaken("sfree99") {
		t.Fatalf("slugTaken(unused) = true; want false")
	}
}
