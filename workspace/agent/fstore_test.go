package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestFileStoreRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	ss := stringStore("test-str", ".txt")
	if _, ok := ss.read("a"); ok {
		t.Fatal("missing key must be !ok")
	}
	if err := ss.write("a", "hello"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if v, ok := ss.read("a"); !ok || v != "hello" {
		t.Fatalf("read: %q ok=%v", v, ok)
	}
	if !strings.HasSuffix(ss.path("a"), filepath.Join("test-str", "a.txt")) {
		t.Fatalf("path layout: %s", ss.path("a"))
	}
	ss.remove("a")
	if _, ok := ss.read("a"); ok {
		t.Fatal("removed key must be !ok")
	}

	type payload struct {
		N int `json:"n"`
	}
	js := jsonStore[payload]("test-json", ".json")
	if err := js.write("k", payload{N: 42}); err != nil {
		t.Fatalf("json write: %v", err)
	}
	if v, ok := js.read("k"); !ok || v.N != 42 {
		t.Fatalf("json read: %+v ok=%v", v, ok)
	}

	ts := trimmedStringStore("test-sid")
	if err := ts.write("s", "abc\n"); err != nil {
		t.Fatalf("sid write: %v", err)
	}
	if v, ok := ts.read("s"); !ok || v != "abc" {
		t.Fatalf("trimmed read: %q ok=%v", v, ok)
	}

	// Empty file reads as absent — the shared "no value" convention.
	rs := rawStore("test-raw", ".bin")
	if err := rs.write("e", nil); err != nil {
		t.Fatalf("raw write: %v", err)
	}
	if _, ok := rs.read("e"); ok {
		t.Fatal("empty file must read as !ok")
	}
}
