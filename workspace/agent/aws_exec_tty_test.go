package main

import (
	"os"
	"testing"
)

// /dev/null is a character device; a redirected run must not count as interactive.
func TestIsTerminalRejectsDevNull(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if isTerminal(f) {
		t.Fatal("/dev/null was treated as a terminal")
	}
}
