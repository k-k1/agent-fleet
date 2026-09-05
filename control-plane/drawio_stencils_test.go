package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func testStencils(t *testing.T) *drawioStencils {
	t.Helper()
	return &drawioStencils{cacheDir: t.TempDir(), loading: map[string]*sync.Mutex{}}
}

// The manifest first: if it is broken, nothing else here means anything.
func TestDrawioManifestLoads(t *testing.T) {
	m, err := loadDrawioManifest()
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if m.Version == "" || !strings.HasPrefix(m.Base, "https://") {
		t.Fatalf("version/base is empty: %+v", m.Version)
	}
	if len(m.Sets) < 150 {
		t.Fatalf("%d sets - the manifest must never be narrowed (a name it does not list is a 404 = the diagram degrades silently)", len(m.Sets))
	}
	// The viewer only ever asks for `<basename>.xml`. Anything else in the manifest
	// widens the barrier that keys off it.
	for name, e := range m.Sets {
		if !strings.HasSuffix(name, ".xml") {
			t.Fatalf("%q is not a .xml", name)
		}
		if strings.Contains(name, "..") || strings.HasPrefix(name, "/") {
			t.Fatalf("%q is not a valid manifest key", name)
		}
		if len(e.SHA256) != 64 || e.Size <= 0 {
			t.Fatalf("the entry for %q is invalid: %+v", name, e)
		}
	}
	// A known hole: the libraries reference sap.xml but upstream does not ship it.
	// Do not read that as a gap in the manifest and add it by hand — the entry would
	// only send fetches at a URL that does not exist.
	if _, ok := m.Sets["sap.xml"]; ok {
		t.Fatalf("sap.xml does not exist in upstream v31.1.8 - it must never be added to the manifest")
	}
	if _, ok := m.Sets["aws4.xml"]; !ok {
		t.Fatalf("aws4.xml is not in the manifest")
	}
}

// A name that is not in the manifest is a 404 without touching upstream at all. This is the
// SSRF barrier itself.
func TestDrawioStencilRejectsUnknownName(t *testing.T) {
	d := testStencils(t)
	for _, name := range []string{
		"evil.xml",
		"../../etc/passwd",
		"http://169.254.169.254/latest/meta-data/",
		"aws4.xml.bak",
		"AWS4.XML", // no case folding: the manifest key is taken as written
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/drawio/stencils/"+name, nil)
		req.SetPathValue("name", name)
		d.serve(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%q: %d (anything but 404 turns this into a tool for fetching arbitrary URLs)", name, rec.Code)
		}
	}
}

// Bytes from upstream that disagree with the manifest are neither stored nor served.
func TestDrawioStencilRejectsTamperedBytes(t *testing.T) {
	body := []byte("<shapes name=\"mxgraph.test\"/>")
	sum := sha256.Sum256(body)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<shapes name=\"mxgraph.evil\"/>!!")) // different content
	}))
	defer srv.Close()

	d := testStencils(t)
	m := drawioStencilManifest{Base: srv.URL + "/", Sets: map[string]drawioStencilEntry{
		"test.xml": {SHA256: hex.EncodeToString(sum[:]), Size: int64(len(body))},
	}}
	if _, err := d.fetch(context.Background(), m, "test.xml", m.Sets["test.xml"]); err == nil {
		t.Fatal("tampered bytes were accepted")
	}
	ents, _ := os.ReadDir(d.cacheDir)
	for _, e := range ents {
		if !strings.HasPrefix(e.Name(), ".tmp-") {
			t.Fatalf("verification failed yet it is left in the cache: %s", e.Name())
		}
	}
}

// The second fetch comes from the cache and never reaches upstream.
func TestDrawioStencilCaches(t *testing.T) {
	body := []byte("<shapes name=\"mxgraph.test\"><shape name=\"a\"/></shapes>")
	sum := sha256.Sum256(body)
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	d := testStencils(t)
	entry := drawioStencilEntry{SHA256: hex.EncodeToString(sum[:]), Size: int64(len(body))}
	m := drawioStencilManifest{Base: srv.URL + "/", Sets: map[string]drawioStencilEntry{"test.xml": entry}}

	for i := 0; i < 3; i++ {
		got, err := d.fetch(context.Background(), m, "test.xml", entry)
		if err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
		if string(got) != string(body) {
			t.Fatalf("the body differs on attempt %d", i)
		}
	}
	if hits != 1 {
		t.Fatalf("upstream was hit %d times (it should be 1)", hits)
	}
	// Content-addressed by sha256, so a changed manifest lands on a different file.
	if _, err := os.Stat(filepath.Join(d.cacheDir, entry.SHA256+".xml")); err != nil {
		t.Fatalf("the cache file is missing: %v", err)
	}
}

// A failed fetch is a 502 and nothing more — the CP stays up. Degrading this way is the
// expected behaviour in an air-gapped deployment.
func TestDrawioStencilUpstreamDownIs502(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	srv.Close() // make it unreachable

	d := testStencils(t)
	entry := drawioStencilEntry{SHA256: strings.Repeat("0", 64), Size: 10}
	m := drawioStencilManifest{Base: srv.URL + "/", Sets: map[string]drawioStencilEntry{"test.xml": entry}}
	if _, err := d.fetch(context.Background(), m, "test.xml", entry); err == nil {
		t.Fatal("an unreachable upstream did not error")
	}
}

// A default-bundle name that is missing from the manifest makes that line do nothing at
// all, and a typo still runs green — so the two lists are checked against each other here.
func TestDrawioPreseedDefaultBundle(t *testing.T) {
	m, err := loadDrawioManifest()
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	for _, n := range drawioPreseedExact {
		if _, ok := m.Sets[n]; !ok {
			t.Fatalf("%q from the default bundle is not in the manifest (a misspelling is silently ignored)", n)
		}
	}
	for _, p := range drawioPreseedPrefixes {
		hit := false
		for name := range m.Sets {
			if strings.HasPrefix(name, p) {
				hit = true
				break
			}
		}
		if !hit {
			t.Fatalf("not a single set matches the default bundle prefix %q", p)
		}
	}

	names := drawioPreseedNames(m, false)
	var total int64
	for _, n := range names {
		total += m.Sets[n].Size
	}
	// The default bundle is what an air-gapped administrator installs without thinking
	// about it. Approaching the full set (40.8 MB) makes "default" meaningless; far too
	// small and it is not a bundle at all.
	if total > 25<<20 {
		t.Fatalf("the default bundle is %.1f MB - too large (no different from --all)", float64(total)/(1<<20))
	}
	if len(names) < 20 {
		t.Fatalf("the default bundle holds only %d sets", len(names))
	}
	// Large sets with a narrow audience stay out of the default.
	for _, n := range names {
		if strings.HasPrefix(n, "rack/hpe_aruba/") {
			t.Fatalf("%q (3.67 MB) does not belong in the default bundle", n)
		}
	}
	// --all must match the manifest exactly.
	if got := len(drawioPreseedNames(m, true)); got != len(m.Sets) {
		t.Fatalf("--all yields %d sets (the manifest has %d)", got, len(m.Sets))
	}
	t.Logf("default bundle: %d sets / %.1f MB", len(names), float64(total)/(1<<20))
}

// The whole point of preseeding is serving with no way out to the network, so this serves
// from a preseeded cache while upstream is unreachable. If it is not green, preseeding buys
// nothing.
func TestDrawioStencilPreseededServesOffline(t *testing.T) {
	body := []byte("<shapes name=\"mxgraph.test\"><shape name=\"a\"/></shapes>")
	sum := sha256.Sum256(body)
	entry := drawioStencilEntry{SHA256: hex.EncodeToString(sum[:]), Size: int64(len(body))}
	// The manifest base points at a host that cannot even connect, so a single outbound
	// request fails the test.
	m := drawioStencilManifest{Base: "http://127.0.0.1:1/", Sets: map[string]drawioStencilEntry{"test.xml": entry}}

	d := testStencils(t)
	if err := d.store(d.pathFor(entry), body); err != nil {
		t.Fatalf("preseed: %v", err)
	}

	got, err := d.fetch(context.Background(), m, "test.xml", entry)
	if err != nil {
		t.Fatalf("preseeded yet it cannot be served with no way out to the network: %v", err)
	}
	if string(got) != string(body) {
		t.Fatalf("the body differs")
	}

	// Preseeded bytes that disagree with the manifest (a version skew, a corrupted
	// copy) are not used.
	if err := os.WriteFile(d.pathFor(entry), []byte("<shapes/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := d.fetch(context.Background(), m, "test.xml", entry); err == nil {
		t.Fatal("a cache entry that disagrees with the manifest was served as-is")
	}
}

// store always writes to a temporary name and renames. A half-written file visible under
// the real name would be served as verified bytes, and preseeding writes into the same
// directory a live CP reads from.
func TestDrawioStencilStoreIsAtomic(t *testing.T) {
	d := testStencils(t)
	body := []byte("<shapes/>")
	sum := sha256.Sum256(body)
	entry := drawioStencilEntry{SHA256: hex.EncodeToString(sum[:]), Size: int64(len(body))}
	if err := d.store(d.pathFor(entry), body); err != nil {
		t.Fatalf("store: %v", err)
	}
	ents, err := os.ReadDir(d.cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	// No leftover temporary file means the rename happened.
	if len(ents) != 1 || strings.HasPrefix(ents[0].Name(), ".tmp-") {
		var names []string
		for _, e := range ents {
			names = append(names, e.Name())
		}
		t.Fatalf("the cache holds %v", names)
	}
	if ents[0].Name() != entry.SHA256+".xml" {
		t.Fatalf("stored at %q (it should be content-addressed)", ents[0].Name())
	}
}

// A momentary upstream failure is retried. Returning 502 on a single reset leaves the
// Console marking that set as already requested, so it never asks again and the icons stay
// missing. Observed for real as a connection reset from raw.githubusercontent.
func TestDrawioStencilRetriesTransient(t *testing.T) {
	body := []byte("<shapes name=\"mxgraph.test\"><shape name=\"a\"/></shapes>")
	sum := sha256.Sum256(body)
	entry := drawioStencilEntry{SHA256: hex.EncodeToString(sum[:]), Size: int64(len(body))}

	var tries int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&tries, 1) < 3 {
			// Drop the connection: a network-layer failure, the kind worth retrying.
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Error("cannot hijack")
				return
			}
			conn, _, err := hj.Hijack()
			if err == nil {
				conn.Close()
			}
			return
		}
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	d := testStencils(t)
	m := drawioStencilManifest{Base: srv.URL + "/", Sets: map[string]drawioStencilEntry{"test.xml": entry}}
	got, err := d.fetch(context.Background(), m, "test.xml", entry)
	if err != nil {
		t.Fatalf("gave up after only 2 disconnects: %v", err)
	}
	if string(got) != string(body) {
		t.Fatal("the body differs")
	}
	if n := atomic.LoadInt32(&tries); n != 3 {
		t.Fatalf("%d attempts (it should be 3)", n)
	}
}

// The mirror image: a failure that will repeat identically is not retried — a 404, and a
// mismatch on a response that arrived complete.
func TestDrawioStencilDoesNotRetryPermanent(t *testing.T) {
	body := []byte("<shapes/>")
	sum := sha256.Sum256(body)
	entry := drawioStencilEntry{SHA256: hex.EncodeToString(sum[:]), Size: int64(len(body))}

	for _, tc := range []struct {
		name  string
		hit   func(w http.ResponseWriter)
		tries int32
	}{
		{"404", func(w http.ResponseWriter) { w.WriteHeader(http.StatusNotFound) }, 1},
		// Right length, wrong bytes: not a truncated transfer, a different file.
		{"tampered", func(w http.ResponseWriter) { _, _ = w.Write([]byte("<shapeX/>")) }, 1},
		// A 5xx can be transient, so it is retried.
		{"503", func(w http.ResponseWriter) { w.WriteHeader(http.StatusServiceUnavailable) }, drawioFetchTries},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var tries int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt32(&tries, 1)
				tc.hit(w)
			}))
			defer srv.Close()
			d := testStencils(t)
			m := drawioStencilManifest{Base: srv.URL + "/", Sets: map[string]drawioStencilEntry{"test.xml": entry}}
			if _, err := d.fetch(context.Background(), m, "test.xml", entry); err == nil {
				t.Fatal("succeeded where it should have failed")
			}
			if n := atomic.LoadInt32(&tries); n != tc.tries {
				t.Fatalf("%d attempts (want %d)", n, tc.tries)
			}
		})
	}
}
