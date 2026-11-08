package main

import (
	"reflect"
	"strings"
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

func TestResolveTier(t *testing.T) {
	n, c, ok := resolveTier("claude:haiku")
	if !ok || n != "claude:haiku" || !strings.HasSuffix(c, "--model haiku") {
		t.Fatalf("%q %q %v", n, c, ok)
	}
	n, c, ok = resolveTier("codex:gpt-6-luna")
	if !ok || n != "codex:gpt-6-luna" || !strings.HasPrefix(c, "codex exec --model gpt-6-luna --json") {
		t.Fatalf("%q %q %v", n, c, ok)
	}
	n, c, ok = resolveTier("mine=./my-model --flag")
	if !ok || n != "mine" || c != "./my-model --flag" {
		t.Fatalf("%q %q %v", n, c, ok)
	}
	for _, bad := range []string{"nocolon", "claude:", "other:model"} {
		if _, _, ok := resolveTier(bad); ok {
			t.Errorf("%q should not resolve", bad)
		}
	}
}
