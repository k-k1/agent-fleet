package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Tests use a made-up canary term. The real ledger's terms are not written down
// anywhere, here included — the point of the design is that the machinery can be
// proven correct without them.
const canary = "zarquon"

func testEntries(t *testing.T, terms ...string) []Entry {
	t.Helper()
	var out []Entry
	for i, term := range terms {
		e, err := NewEntry(term, "t"+string(rune('1'+i)))
		if err != nil {
			t.Fatalf("NewEntry(%q): %v", term, err)
		}
		out = append(out, e)
	}
	return out
}

func TestCanonical(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Zarquon", "zarquon"},
		{"  Zarquon Inc.  ", "zarquon inc"},
		{"Zarquon-Inc", "zarquon inc"},
		{"zarquon_inc", "zarquon inc"},
		{"Zarquon\n *  Inc", "zarquon inc"},
		{"ZarquonInc", "zarquoninc"},
		{"証券コード 4395", "証券コード 4395"},
		{"", ""},
		{"---", ""},
	} {
		if got := Canonical(tc.in); got != tc.want {
			t.Errorf("Canonical(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestLedgerRoundTrip(t *testing.T) {
	e, err := NewEntry("Zarquon Inc.", "corp-1")
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseLedger(strings.NewReader("# comment\n\n" + e.String() + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Sum != e.Sum || got[0].Len != e.Len || got[0].RK != e.RK {
		t.Fatalf("round trip lost data: %+v vs %+v", got[0], e)
	}
	// Spelling variants must all fold onto the one ledger entry.
	for _, v := range []string{"Zarquon Inc.", "zarquon-inc", "ZARQUON   INC"} {
		if Canonical(v) != Canonical("Zarquon Inc.") {
			t.Errorf("variant %q does not fold onto the entry", v)
		}
	}
}

func TestLedgerRejectsEmptyAndShort(t *testing.T) {
	if _, err := ParseLedger(strings.NewReader("# only comments\n")); err == nil {
		t.Fatal("an empty ledger must be an error — a gate that cannot fire is not a gate")
	}
	if _, err := NewEntry("ab", "x"); err == nil {
		t.Fatal("a 2-rune term must be rejected")
	}
}

// scanBytes folds one buffer and returns the hits.
func scanBytes(t *testing.T, entries []Entry, data []byte) []Hit {
	t.Helper()
	w := NewWalker(entries, &AllowList{}, false)
	if err := w.stream("leaf", bytes.NewReader(data), 0); err != nil {
		t.Fatalf("stream: %v", err)
	}
	return w.Hits
}

func TestMatchesSpellingVariants(t *testing.T) {
	entries := testEntries(t, canary)
	for _, v := range []string{
		"Zarquon",
		"the ZARQUON theme",
		"zarquon.css",
		"ZarquonInc",       // inside a longer word
		"x" + canary + "y", // inside a longer word, both sides
		"/* @theme zarquon */",
		"z a r q u o n", // NOT a match — see below
	} {
		hits := scanBytes(t, entries, []byte(v))
		want := v != "z a r q u o n"
		if got := len(hits) > 0; got != want {
			t.Errorf("%q: hit=%v, want %v", v, got, want)
		}
	}
}

func TestMultiWordTermFoldsSeparators(t *testing.T) {
	entries := testEntries(t, "Zarquon Inc")
	for _, v := range []string{"Zarquon Inc", "zarquon-inc", "Zarquon\n *  Inc.", "ZARQUON___INC"} {
		if len(scanBytes(t, entries, []byte(v))) == 0 {
			t.Errorf("%q should have matched the multi-word term", v)
		}
	}
	if len(scanBytes(t, entries, []byte("zarquoninc"))) != 0 {
		t.Error("a run with no separator must not match the two-word term")
	}
}

func TestNoMatchAcrossChunkOrFileBoundaries(t *testing.T) {
	entries := testEntries(t, canary)
	// Split across Write calls: must still match (the rune stream is continuous).
	w := NewWalker(entries, &AllowList{}, false)
	m := w.matcher("split")
	m.Write([]byte("zar"))
	m.Write([]byte("quon"))
	if len(w.Hits) != 1 {
		t.Fatalf("term split across Write calls: got %d hits, want 1", len(w.Hits))
	}
	// Two separate files whose halves would concatenate into the term must not.
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, body := range map[string]string{"a": "aaazar", "b": "quonbbb"} {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
		tw.Write([]byte(body))
	}
	tw.Close()
	if hits := scanBytes(t, entries, buf.Bytes()); len(hits) != 0 {
		t.Fatalf("halves in two tar members must not fabricate a match: %+v", hits)
	}
}

func TestMultiByteAcrossWriteBoundary(t *testing.T) {
	entries := testEntries(t, "証券コード")
	w := NewWalker(entries, &AllowList{}, false)
	m := w.matcher("cjk")
	b := []byte("x証券コード")
	// Feed one byte at a time, so every multi-byte rune straddles a Write.
	for i := range b {
		m.Write(b[i : i+1])
	}
	if len(w.Hits) != 1 {
		t.Fatalf("CJK term fed byte-by-byte: got %d hits, want 1", len(w.Hits))
	}
}

func TestInvalidUTF8IsASeparator(t *testing.T) {
	entries := testEntries(t, canary)
	if hits := scanBytes(t, entries, []byte("zar\xffquon")); len(hits) != 0 {
		t.Fatalf("an invalid byte must break the window: %+v", hits)
	}
	if hits := scanBytes(t, entries, append([]byte{0x00, 0xff, 0xfe}, []byte("zarquon")...)); len(hits) != 1 {
		t.Fatalf("binary noise before the term must not hide it: %d hits", len(hits))
	}
}

// buildNestedFixture writes the shape a real release artifact has:
// outer .tar.gz -> inner tar (a `docker save` layout) -> gzip layer -> tar -> file.
func buildNestedFixture(t *testing.T, payload string) []byte {
	t.Helper()
	tarWith := func(name string, body []byte) []byte {
		var b bytes.Buffer
		tw := tar.NewWriter(&b)
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write(body)
		tw.Close()
		return b.Bytes()
	}
	gz := func(b []byte) []byte {
		var out bytes.Buffer
		zw := gzip.NewWriter(&out)
		zw.Write(b)
		zw.Close()
		return out.Bytes()
	}
	layer := tarWith("app/console/assets/index.js", []byte("const x=`/* "+payload+" */`;"))
	// The blob has no file extension at all — exactly like `docker save` output.
	image := tarWith("blobs/sha256/ab12cd34", gz(layer))
	return gz(image)
}

func TestNestedArchiveExpansion(t *testing.T) {
	entries := testEntries(t, canary)
	if hits := scanBytes(t, entries, buildNestedFixture(t, "Zarquon Ltd deck")); len(hits) != 1 {
		t.Fatalf("term four archives deep: got %d hits, want 1", len(hits))
	}
	if hits := scanBytes(t, entries, buildNestedFixture(t, "nothing to see")); len(hits) != 0 {
		t.Fatalf("clean fixture: got %d hits, want 0", len(hits))
	}
}

func TestPathIsScannedToo(t *testing.T) {
	entries := testEntries(t, canary)
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	body := []byte("clean content")
	tw.WriteHeader(&tar.Header{Name: "console/src/zarquon.css", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
	tw.Write(body)
	tw.Close()
	if hits := scanBytes(t, entries, b.Bytes()); len(hits) == 0 {
		t.Fatal("a forbidden token in a file name must be caught")
	}
}

func TestSymlinkTargetIsScanned(t *testing.T) {
	entries := testEntries(t, canary)
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	tw.WriteHeader(&tar.Header{Name: "link", Linkname: "../zarquon/theme.css", Typeflag: tar.TypeSymlink, Mode: 0o777})
	tw.Close()
	if hits := scanBytes(t, entries, b.Bytes()); len(hits) == 0 {
		t.Fatal("a forbidden token in a symlink target must be caught")
	}
}

func TestAllowList(t *testing.T) {
	a, err := ParseAllow(strings.NewReader("# comment\nt1 *!vendor/*\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !a.Allows("t1", "images.tar.gz!vendor/pkg/doc.txt") {
		t.Error("exempted path should be allowed")
	}
	if a.Allows("t2", "images.tar.gz!vendor/pkg/doc.txt") {
		t.Error("the exemption is scoped to its term id")
	}
	if a.Allows("t1", "images.tar.gz!app/doc.txt") {
		t.Error("non-matching path must not be allowed")
	}
}

func TestRunEndToEnd(t *testing.T) {
	dir := t.TempDir()
	e, err := NewEntry(canary, "corp-1")
	if err != nil {
		t.Fatal(err)
	}
	ledger := filepath.Join(dir, "forbidden.sha256")
	if err := os.WriteFile(ledger, []byte(e.String()+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	art := filepath.Join(dir, "dist")
	if err := os.MkdirAll(art, 0o755); err != nil {
		t.Fatal(err)
	}
	clean := filepath.Join(art, "clean.tar.gz")
	if err := os.WriteFile(clean, buildNestedFixture(t, "all good"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(ledger, "", false, "", false, []string{art}); err != nil {
		t.Fatalf("clean tree must pass: %v", err)
	}
	dirty := filepath.Join(art, "dirty.tar.gz")
	if err := os.WriteFile(dirty, buildNestedFixture(t, "Zarquon Ltd"), 0o644); err != nil {
		t.Fatal(err)
	}
	err = run(ledger, "", false, "", false, []string{art})
	if err == nil {
		t.Fatal("dirty tree must fail")
	}
	if exitCode(err) != 1 {
		t.Fatalf("a hit must exit 1, got %d (%v)", exitCode(err), err)
	}
	// A missing ledger is exit 2, not a pass.
	if got := exitCode(runErr(t, filepath.Join(dir, "nope"), art)); got != 2 {
		t.Fatalf("missing ledger must exit 2, got %d", got)
	}
}

func runErr(t *testing.T, ledger, path string) error {
	t.Helper()
	err := run(ledger, "", false, "", false, []string{path})
	if err == nil {
		t.Fatal("expected an error")
	}
	return err
}
