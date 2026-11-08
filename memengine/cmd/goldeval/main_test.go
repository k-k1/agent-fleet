package main

import (
	"reflect"
	"testing"

	"github.com/k-k1/memengine"
	"github.com/k-k1/memengine/internal/goldset"
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
	cfg, _ := memengine.LoadConfig("")
	n, r, m, c, err := resolveTier(cfg, "claude:some-model-1")
	if err != nil || n != "claude:some-model-1" || r != "claude" || m != "some-model-1" || c.Argv[len(c.Argv)-1] != "some-model-1" {
		t.Fatalf("%q %q %q %v %v", n, r, m, c.Argv, err)
	}
	if _, _, _, c, err = resolveTier(cfg, "codex:x-y"); err != nil || c.Argv[0] != "codex" {
		t.Fatalf("%v %v", c.Argv, err)
	}
	n, r, _, c, err = resolveTier(cfg, "mine=./my-model --flag")
	if err != nil || n != "mine" || r != "custom" || len(c.Argv) != 2 {
		t.Fatalf("%q %v %v", n, c.Argv, err)
	}
	for _, bad := range []string{"nocolon", "claude:", "other:model", "mine="} {
		if _, _, _, _, err := resolveTier(cfg, bad); err == nil {
			t.Errorf("%q should not resolve", bad)
		}
	}
}

// The shipped examples must stay loadable.
func TestExamplesLoad(t *testing.T) {
	cfg, err := memengine.LoadConfig("../../models.example.json")
	if err != nil || len(cfg.Candidates) == 0 {
		t.Fatalf("%v", err)
	}
	if _, err := memengine.NewPipeline(cfg); err != nil {
		t.Errorf("the example roles do not resolve: %v", err)
	}
	if _, err := goldset.LoadBar("../../bar.example.json"); err != nil {
		t.Error(err)
	}
}
