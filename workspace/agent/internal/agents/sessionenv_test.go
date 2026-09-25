package agents

import (
	"slices"
	"testing"
)

func TestWithSessionNameReplacesAnInheritedValue(t *testing.T) {
	base := []string{"PATH=/bin", "AF_SESSION_NAME=someone-else", "AF_SESSION_NAME_X=keep"}
	got := WithSessionName(base, "mine")
	want := []string{"PATH=/bin", "AF_SESSION_NAME_X=keep", "AF_SESSION_NAME=mine"}
	if !slices.Equal(got, want) {
		t.Errorf("WithSessionName = %v, want %v", got, want)
	}
	if base[1] != "AF_SESSION_NAME=someone-else" {
		t.Errorf("the caller's slice was modified: %v", base)
	}
	if got := WithSessionName(base, ""); slices.Contains(got, "AF_SESSION_NAME=someone-else") {
		t.Errorf("an unnamed child inherited another session's name: %v", got)
	}
}
