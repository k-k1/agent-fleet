package fstore

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreRoundTrip(t *testing.T) {
	root := t.TempDir()
	base := func() string { return root }

	ss := Strings(base, "test-str", ".txt")
	if _, ok := ss.Read("a"); ok {
		t.Fatal("missing key must be !ok")
	}
	if err := ss.Write("a", "hello"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if v, ok := ss.Read("a"); !ok || v != "hello" {
		t.Fatalf("read: %q ok=%v", v, ok)
	}
	if !strings.HasSuffix(ss.Path("a"), filepath.Join("test-str", "a.txt")) {
		t.Fatalf("path layout: %s", ss.Path("a"))
	}
	ss.Remove("a")
	if _, ok := ss.Read("a"); ok {
		t.Fatal("removed key must be !ok")
	}

	type payload struct {
		N int `json:"n"`
	}
	js := JSON[payload](base, "test-json", ".json")
	if err := js.Write("k", payload{N: 42}); err != nil {
		t.Fatalf("json write: %v", err)
	}
	if v, ok := js.Read("k"); !ok || v.N != 42 {
		t.Fatalf("json read: %+v ok=%v", v, ok)
	}

	ts := TrimmedStrings(base, "test-sid")
	if err := ts.Write("s", "abc\n"); err != nil {
		t.Fatalf("sid write: %v", err)
	}
	if v, ok := ts.Read("s"); !ok || v != "abc" {
		t.Fatalf("trimmed read: %q ok=%v", v, ok)
	}

	// Empty file reads as absent — the shared "no value" convention.
	rs := Raw(base, "test-raw", ".bin")
	if err := rs.Write("e", nil); err != nil {
		t.Fatalf("raw write: %v", err)
	}
	if _, ok := rs.Read("e"); ok {
		t.Fatal("empty file must read as !ok")
	}
}
