package agents

import (
	"errors"
	"strings"
	"testing"
	"unicode"
)

func hasCJK(s string) bool {
	for _, r := range s {
		if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana) {
			return true
		}
	}
	return false
}

// The shared fork refusals reach the Console as free text, so each must be a "ja / en" pair.
func TestForkErrorsAreBilingual(t *testing.T) {
	for _, err := range []error{ErrNoConversationToFork, ErrNoForkPoint, ErrForkPointNotFound, ErrForkAtManagedOnly} {
		ja, en, ok := strings.Cut(err.Error(), " / ")
		if !ok || !hasCJK(ja) || en == "" || hasCJK(en) {
			t.Errorf("%q is not a \"ja / en\" pair", err)
		}
	}
	if errors.Is(ErrNoForkPoint, ErrForkPointNotFound) {
		t.Error("distinct sentinels compare equal")
	}
}
