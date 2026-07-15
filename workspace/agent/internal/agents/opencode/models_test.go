package opencode

import (
	"reflect"
	"testing"
)

func TestParseModels(t *testing.T) {
	out := `opencode/big-pickle
opencode/deepseek-v4-flash-free

anthropic/claude-sonnet-4-5
WARN some warning line
  opencode/hy3-free
not-a-model
`
	got := parseModels(out)
	want := []string{
		"opencode/big-pickle",
		"opencode/deepseek-v4-flash-free",
		"anthropic/claude-sonnet-4-5",
		"opencode/hy3-free",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseModels = %v, want %v", got, want)
	}
}

func TestParseModelsEmpty(t *testing.T) {
	if got := parseModels(""); len(got) != 0 {
		t.Fatalf("parseModels(empty) = %v, want []", got)
	}
}

func TestMergeCommandEnv(t *testing.T) {
	got := mergeCommandEnv(
		[]string{"PATH=/usr/bin", "OPENCODE_API_KEY=old", "KEEP=yes"},
		[]string{"OPENCODE_API_KEY=stored", "ANTHROPIC_API_KEY=anthropic", "invalid"},
	)
	want := []string{
		"PATH=/usr/bin",
		"OPENCODE_API_KEY=stored",
		"KEEP=yes",
		"ANTHROPIC_API_KEY=anthropic",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mergeCommandEnv = %v, want %v", got, want)
	}
}
