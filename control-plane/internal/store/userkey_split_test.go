package store

import (
	"strings"
	"testing"
)

// The admin API accepts a stored key past 40 characters only in disambiguateUserKey's
// shape, so the parser has to accept exactly what that function mints.
func TestSplitDisambiguatedUserKeyMatchesDisambiguateUserKey(t *testing.T) {
	s := engineModelStore(t)
	ctx := t.Context()
	long := strings.Repeat("a", 40)
	if _, err := s.UpsertIdentity(ctx, long+"1@example.com", long, ""); err != nil {
		t.Fatal(err)
	}
	minted, err := s.disambiguateUserKey(ctx, long+"2@example.com", long)
	if err != nil {
		t.Fatal(err)
	}
	if prefix, ok := SplitDisambiguatedUserKey(minted); !ok || prefix != long {
		t.Errorf("SplitDisambiguatedUserKey(%q) = %q, %v; want %q, true", minted, prefix, ok, long)
	}
	for _, key := range []string{"", "-0123abcd", "abc", "ab-0123abc", "ab-0123ABCD", "ab_0123abcd", "ab-0123abcg"} {
		if prefix, ok := SplitDisambiguatedUserKey(key); ok {
			t.Errorf("SplitDisambiguatedUserKey(%q) = %q, true; want false", key, prefix)
		}
	}
}
