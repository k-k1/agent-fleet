package main

import (
	"reflect"
	"testing"
)

func TestSplitArgs(t *testing.T) {
	got, err := splitArgs(`env A=1 claude -p --tools "" --system-prompt "only json please" --x 'a b'`)
	want := []string{"env", "A=1", "claude", "-p", "--tools", "", "--system-prompt", "only json please", "--x", "a b"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := splitArgs(`a "b`); err == nil {
		t.Error("unterminated quote must be an error")
	}
}
