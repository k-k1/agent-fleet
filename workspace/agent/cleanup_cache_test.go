package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
)

func cacheTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SESSIONS_DIR", "")
	invalidateCleanupUsage()
	t.Cleanup(invalidateCleanupUsage)
	return home
}

// oldCacheDir makes ~/.cache/agent-fleet/<feature>/<name>/f (n bytes), aged past the grace.
func oldCacheDir(t *testing.T, feature, name string, n int) string {
	t.Helper()
	d := filepath.Join(sessionx.CacheRoot(), feature, name)
	if err := os.MkdirAll(d, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(d, "f")
	if err := os.WriteFile(p, make([]byte, n), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	for _, x := range []string{p, d} {
		if err := os.Chtimes(x, old, old); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

// TestCacheScanSeesRealCleanupArchive pins the format seam: the scan reads the gz trash with
// its own minimal decoder, so an archive written by the real writer — both with its sidecar
// and with the sidecar lost — must keep that session's cache.
func TestCacheScanSeesRealCleanupArchive(t *testing.T) {
	for _, dropSidecar := range []bool{false, true} {
		cacheTestHome(t)
		m := session.Meta{Name: "sarch01", Dir: "/home/dev/repos/app", Kind: session.KindClaude}
		man := cleanupManifest{
			ID: newCleanupID(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), "sarch01"), Reason: "delete_session",
			Sessions: []cleanupArchivedSession{{Name: m.Name, Kind: m.Kind, Meta: marshalMeta(m)}},
		}
		if err := writeCleanupArchive(man, map[string][]byte{"sessions/x/00.jsonl": []byte("{}\n")}); err != nil {
			t.Fatal(err)
		}
		if dropSidecar {
			if err := os.Remove(filepath.Join(cleanupStoreDir(), man.ID+".json")); err != nil {
				t.Fatal(err)
			}
		}
		oldCacheDir(t, sessionx.CacheFeaturePasted, session.UUID(m.Dir, m.Name), 5)
		got, err := sessionx.ScanCacheOrphans(sessionx.CacheFeaturePasted, time.Now(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Dirs) != 0 {
			t.Fatalf("sidecar dropped=%v: a session in the trash was offered for deletion", dropSidecar)
		}
	}
}

func TestHandleDeleteCacheOrphans(t *testing.T) {
	cacheTestHome(t)
	dead := oldCacheDir(t, sessionx.CacheFeatureCodexViewImage, session.UUID("/d", "sgone01"), 64)

	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /cleanup/cache/{feature}", handleDeleteCacheOrphans)
	do := func(feature string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/cleanup/cache/"+feature, nil))
		return rec
	}

	if rec := do("generated"); rec.Code != http.StatusBadRequest {
		t.Fatalf("generated/ is not reachability-swept, got %d", rec.Code)
	}
	rec := do(sessionx.CacheFeatureCodexViewImage)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Dirs  int   `json:"dirs"`
		Bytes int64 `json:"bytes"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Dirs != 1 || body.Bytes != 64 {
		t.Fatalf("body = %s", rec.Body)
	}
	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Fatalf("directory survived: %v", err)
	}

	// An unreadable meta makes every owner unprovable: 409, nothing deleted.
	dead2 := oldCacheDir(t, sessionx.CacheFeaturePasted, session.UUID("/d", "sgone02"), 1)
	if err := os.MkdirAll(session.MetaDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(session.MetaDir(), "sbroken.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if rec := do(sessionx.CacheFeaturePasted); rec.Code != http.StatusConflict {
		t.Fatalf("unsafe scan: status %d", rec.Code)
	}
	if _, err := os.Stat(dead2); err != nil {
		t.Fatalf("unsafe scan deleted: %v", err)
	}
}

func TestHandleCleanupUsage(t *testing.T) {
	cacheTestHome(t)
	oldCacheDir(t, "generated", "console", 1000)
	oldCacheDir(t, sessionx.CacheFeaturePasted, session.UUID("/d", "sgone01"), 30)
	if err := os.MkdirAll(cleanupStoreDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	// A readable sidecar (10 bytes) stands in for the tarball's contents, which are then
	// never opened; the tarball only has to have a size.
	for name, b := range map[string][]byte{"a.tar.gz": make([]byte, 200), "a.json": []byte(`{"id":"a"}`)} {
		if err := os.WriteFile(filepath.Join(cleanupStoreDir(), name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	get := func() cleanupUsage {
		rec := httptest.NewRecorder()
		handleCleanupUsage(rec, httptest.NewRequest(http.MethodGet, "/cleanup/usage", nil))
		var u cleanupUsage
		if err := json.Unmarshal(rec.Body.Bytes(), &u); err != nil {
			t.Fatalf("%v: %s", err, rec.Body)
		}
		return u
	}
	u := get()
	if u.Cache.Bytes != 1030 || u.Cache.Files != 2 || len(u.Cache.Parts) != 2 || u.Cache.Parts[0].Name != "generated" {
		t.Fatalf("cache = %+v", u.Cache)
	}
	if !u.Orphans.OK || u.Orphans.Bytes != 30 || u.Orphans.Dirs != 1 {
		t.Fatalf("orphans = %+v", u.Orphans)
	}
	if u.Trash.Bytes != 210 || u.Trash.Archives != 1 {
		t.Fatalf("trash = %+v", u.Trash)
	}

	// Held for a while — a new file does not show — until something invalidates it.
	oldCacheDir(t, "thumbs", "x", 5)
	if get().Cache.Bytes != 1030 {
		t.Fatal("answer was not held")
	}
	invalidateCleanupUsage()
	if got := get().Cache.Bytes; got != 1035 {
		t.Fatalf("after invalidation cache = %d, want 1035", got)
	}
}

// TestRestoreAndPurgeTakeTheCleanupLock (review ③, the main side): both trash operations
// wait for the cleanup lock, so a cache delete can never scan between a restore reading an
// archive and writing the meta back.
func TestRestoreAndPurgeTakeTheCleanupLock(t *testing.T) {
	cacheTestHome(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /cleanup/archives/{id}/restore", handleRestoreCleanupArchive)
	mux.HandleFunc("DELETE /cleanup/archives/{id}", handlePurgeCleanupArchive)
	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/cleanup/archives/none/restore", nil),
		httptest.NewRequest(http.MethodDelete, "/cleanup/archives/none", nil),
	} {
		done := make(chan struct{})
		sessionx.WithCleanupLock(func() {
			go func() {
				mux.ServeHTTP(httptest.NewRecorder(), req)
				close(done)
			}()
			select {
			case <-done:
				t.Fatalf("%s %s ran while the cleanup lock was held", req.Method, req.URL.Path)
			case <-time.After(100 * time.Millisecond):
			}
		})
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s %s never ran after the lock was released", req.Method, req.URL.Path)
		}
	}
}
