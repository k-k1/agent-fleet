package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

// The shipped defaults must stay loadable.
func TestExamplesLoad(t *testing.T) {
	cfg, err := memengine.LoadConfig("../../models.json")
	if err != nil || len(cfg.Candidates) == 0 {
		t.Fatalf("%v", err)
	}
	if _, err := memengine.NewPipeline(cfg); err != nil {
		t.Errorf("the shipped roles do not resolve: %v", err)
	}
	if _, err := goldset.LoadBar("../../bar.json"); err != nil {
		t.Error(err)
	}
}

func TestExportPromptsSplitNeverLeaksTheOtherHalf(t *testing.T) {
	dir := t.TempDir()
	var cs, in string
	for _, id := range []string{"a", "e", "f", "h"} { // a, h: tune; e, f: held-out
		cs += `{"id":"` + id + `","observation":"o","memories":[],"label":{"operation":"add","ok":"OK"}}` + "\n"
		in += `{"id":"` + id + `","memories":[]}` + "\n"
	}
	os.WriteFile(filepath.Join(dir, "c.jsonl"), []byte(cs), 0o644)
	os.WriteFile(filepath.Join(dir, "i.jsonl"), []byte(in), 0o644)
	exportPrompts([]string{"-cases", filepath.Join(dir, "c.jsonl"), "-inputs", filepath.Join(dir, "i.jsonl"), "-out", dir, "-split", "tune"})
	b, err := os.ReadFile(filepath.Join(dir, "prompts-classify.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if strings.Count(got, `"case_id"`) != 2 || !strings.Contains(got, `"case_id":"a"`) || !strings.Contains(got, `"case_id":"h"`) || strings.Contains(got, `"case_id":"e"`) {
		t.Errorf("tune export must hold exactly the tune cases:\n%s", got)
	}
}
