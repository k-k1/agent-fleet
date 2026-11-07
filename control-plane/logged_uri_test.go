package main

import (
	"os"
	"strings"
	"testing"
)

// The control plane redacts the same parameter names as the Agent's access log; the
// shared fixture keeps the two lists from drifting apart (the modules cannot import
// each other).
func TestSecretQueryKeysMatchFixture(t *testing.T) {
	b, err := os.ReadFile("../scripts/fixtures/secret-query-keys.txt")
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Fields(string(b))
	if len(want) != len(secretQueryKeys) {
		t.Fatalf("fixture has %d names, main.go has %d", len(want), len(secretQueryKeys))
	}
	for _, k := range want {
		if !secretQueryKeys[k] {
			t.Errorf("secretQueryKeys lacks %q", k)
		}
	}
}
