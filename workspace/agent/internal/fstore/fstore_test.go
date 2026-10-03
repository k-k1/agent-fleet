package fstore

import (
	"os"
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

// A reader racing a writer sees the old record or the new one, never a missing or empty one.
// The session-status hook writes from another process while the delivery loop and the list
// read; a torn read there answers "idle" for a session that is blocked or working.
func TestWriteIsNeverSeenTorn(t *testing.T) {
	root := t.TempDir()
	type rec struct {
		State string `json:"state"`
		Pad   string `json:"pad"`
	}
	s := JSON[rec](func() string { return root }, "test-torn", ".json")
	pad := strings.Repeat("x", 16<<10) // a write large enough to be observed half-done
	if err := s.Write("k", rec{State: "permission", Pad: pad}); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 500; i++ {
			st := "working"
			if i%2 == 0 {
				st = "permission"
			}
			_ = s.Write("k", rec{State: st, Pad: pad})
		}
	}()
	torn := 0
	for reading := true; reading; {
		select {
		case <-done:
			reading = false
		default:
		}
		if _, ok := s.Read("k"); !ok {
			torn++
		}
	}
	if torn > 0 {
		t.Fatalf("%d reads saw no record while it was being rewritten", torn)
	}
	ents, _ := os.ReadDir(s.Dir())
	if len(ents) != 1 {
		var names []string
		for _, e := range ents {
			names = append(names, e.Name())
		}
		t.Fatalf("store dir holds %v, want only k.json (no temp file left behind)", names)
	}
}
