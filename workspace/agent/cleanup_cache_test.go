package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
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
	// A real home always has the session store; the scan refuses to judge without one.
	if err := os.MkdirAll(session.MetaDir(), 0o700); err != nil {
		t.Fatal(err)
	}
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
		if err := writeCleanupArchive(&man, map[string][]byte{"sessions/x/00.jsonl": []byte("{}\n")}); err != nil {
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
	// Each figure says where it lives: "~/…" to read, browse-root relative to open.
	if g := u.Cache.Parts[0]; g.Path != "~/.cache/agent-fleet/generated" || g.Browse != ".cache/agent-fleet/generated" {
		t.Fatalf("generated place = %q / %q", g.Path, g.Browse)
	}
	if u.Cache.Path != "~/.cache/agent-fleet" || u.Trash.Path != "~/.local/share/agent-fleet/cleanup" ||
		u.Trash.Browse != ".local/share/agent-fleet/cleanup" {
		t.Fatalf("cache place = %q, trash place = %q / %q", u.Cache.Path, u.Trash.Path, u.Trash.Browse)
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

// archivedSession writes a real cleanup archive holding one session and returns its id.
func archivedSession(t *testing.T, name string) (string, session.Meta) {
	t.Helper()
	m := session.Meta{Name: name, Dir: "/home/dev/repos/app", Kind: session.KindClaude}
	jsonl := filepath.Join(os.Getenv("HOME"), name+".jsonl")
	man := cleanupManifest{
		ID: newCleanupID(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), name), Reason: "delete_session",
		Sessions: []cleanupArchivedSession{{
			Name: m.Name, Kind: m.Kind, Meta: marshalMeta(m),
			JSONLPaths: []string{jsonl}, JSONLNames: []string{"sessions/x/00.jsonl"},
		}},
	}
	if err := writeCleanupArchive(&man, map[string][]byte{"sessions/x/00.jsonl": []byte("{}\n")}); err != nil {
		t.Fatal(err)
	}
	return man.ID, m
}

// TestRestoreAndPurgeTakeTheCleanupLock (review ③): the purge, and the restore's meta
// hand-over, wait for the cleanup lock — so a cache delete can never scan in the gap between
// a restore reading an archive and writing the meta back.
func TestRestoreAndPurgeTakeTheCleanupLock(t *testing.T) {
	cacheTestHome(t)
	id, m := archivedSession(t, "srest01")
	mux := http.NewServeMux()
	mux.HandleFunc("POST /cleanup/archives/{id}/restore", handleRestoreCleanupArchive)
	mux.HandleFunc("DELETE /cleanup/archives/{id}", handlePurgeCleanupArchive)
	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/cleanup/archives/"+id+"/restore", nil),
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
				t.Fatalf("%s %s finished while the cleanup lock was held", req.Method, req.URL.Path)
			case <-time.After(150 * time.Millisecond):
			}
		})
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s %s never finished after the lock was released", req.Method, req.URL.Path)
		}
	}
	if _, ok := session.ReadMeta(m.Name); !ok {
		t.Fatal("the restore did not bring the meta back")
	}
}

// TestRestoreLosesToAPurge (re-review ③, third review ①④): a purge that wins the race makes
// the restore fail — and a failed restore has changed nothing: no meta, no transcript, and a
// transcript already at the path is exactly as it was.
func TestRestoreLosesToAPurge(t *testing.T) {
	cacheTestHome(t)
	id, m := archivedSession(t, "srest02")
	live := filepath.Join(os.Getenv("HOME"), "srest02.jsonl")
	staged := make(chan struct{})
	proceed := make(chan struct{})
	restoreAfterStage = func() {
		close(staged)
		<-proceed
	}
	t.Cleanup(func() { restoreAfterStage = nil })

	var err error
	done := make(chan struct{})
	go func() {
		_, err = restoreCleanupArchive(id)
		close(done)
	}()
	<-staged // the archive has been read and the transcript staged
	if _, perr := purgeCleanupArchive(id); perr != nil {
		t.Fatal(perr)
	}
	close(proceed)
	<-done
	if err == nil {
		t.Fatal("a restore of a purged archive succeeded")
	}
	if _, ok := session.ReadMeta(m.Name); ok {
		t.Fatal("the meta came back without its archive")
	}
	if _, serr := os.Stat(live); !os.IsNotExist(serr) {
		t.Fatalf("a failed restore left the transcript behind: %v", serr)
	}
	if left, _ := filepath.Glob(filepath.Join(os.Getenv("HOME"), ".restore-*")); len(left) != 0 {
		t.Fatalf("staging files left behind: %v", left)
	}
}

// TestRestoreKeepsTheLiveTranscript (third review ①): restoring an archive whose session is
// already back — and has moved on — must not roll its transcript back to the archived copy.
func TestRestoreKeepsTheLiveTranscript(t *testing.T) {
	cacheTestHome(t)
	id, m := archivedSession(t, "srest03")
	live := filepath.Join(os.Getenv("HOME"), "srest03.jsonl")
	newer := []byte("{}\n{\"turn\":2}\n")
	if err := os.WriteFile(live, newer, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := restoreCleanupArchive(id); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(live)
	if !bytes.Equal(got, newer) {
		t.Fatalf("the live transcript was rolled back to %q", got)
	}
	if _, ok := session.ReadMeta(m.Name); !ok {
		t.Fatal("the meta did not come back")
	}
}

// TestRestoreWritesAMissingTranscript is the positive control for the two above: with no
// transcript at the path and no purge, the archived one is put back.
func TestRestoreWritesAMissingTranscript(t *testing.T) {
	cacheTestHome(t)
	id, _ := archivedSession(t, "srest04")
	if _, err := restoreCleanupArchive(id); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), "srest04.jsonl"))
	if err != nil || string(got) != "{}\n" {
		t.Fatalf("transcript = %q, %v", got, err)
	}
}

// TestRestoreNeverReplacesATranscriptThatAppears (fourth review, serious): a transcript that
// shows up after the restore's first check — while it stages — still wins. Placing is a hard
// link, which fails on an existing file; a rename would have replaced it.
func TestRestoreNeverReplacesATranscriptThatAppears(t *testing.T) {
	cacheTestHome(t)
	id, m := archivedSession(t, "srest05")
	live := filepath.Join(os.Getenv("HOME"), "srest05.jsonl")
	newer := []byte("{}\n{\"turn\":2}\n")
	restoreAfterStage = func() {
		if err := os.WriteFile(live, newer, 0o600); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { restoreAfterStage = nil })
	if _, err := restoreCleanupArchive(id); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(live); !bytes.Equal(got, newer) {
		t.Fatalf("the transcript that appeared was replaced with %q", got)
	}
	if _, ok := session.ReadMeta(m.Name); !ok {
		t.Fatal("the meta did not come back")
	}
	if left, _ := filepath.Glob(filepath.Join(os.Getenv("HOME"), ".restore-*")); len(left) != 0 {
		t.Fatalf("staging files left behind: %v", left)
	}
}

// TestRestoreFailsLoudlyAndRetries (fourth/fifth review): when a transcript cannot be placed
// the restore fails and brings no meta back — and once the cause is gone, restoring again
// finishes the job.
func TestRestoreFailsLoudlyAndRetries(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes into a read-only directory anyway")
	}
	cacheTestHome(t)
	home := os.Getenv("HOME")
	id, m := archivedSession(t, "srest06")
	restoreAfterStage = func() {
		// Staged already; now the destination directory stops accepting new names.
		if err := os.Chmod(home, 0o500); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() {
		restoreAfterStage = nil
		_ = os.Chmod(home, 0o700)
	})
	_, err := restoreCleanupArchive(id)
	_ = os.Chmod(home, 0o700)
	restoreAfterStage = nil
	if err == nil {
		t.Fatal("a restore that could not place its transcript succeeded")
	}
	if _, ok := session.ReadMeta(m.Name); ok {
		t.Fatal("the meta came back without its transcript")
	}
	if _, err := restoreCleanupArchive(id); err != nil {
		t.Fatalf("restoring again after the cause was gone: %v", err)
	}
	if _, ok := session.ReadMeta(m.Name); !ok {
		t.Fatal("the retry did not bring the meta back")
	}
	if got, err := os.ReadFile(filepath.Join(home, "srest06.jsonl")); err != nil || string(got) != "{}\n" {
		t.Fatalf("transcript after retry = %q, %v", got, err)
	}
}

// TestRestoreHasNoReplacingFallback (fifth review, serious): where a hard link cannot be made,
// the restore fails instead of falling back to a rename that could replace a live transcript.
func TestRestoreHasNoReplacingFallback(t *testing.T) {
	cacheTestHome(t)
	id, m := archivedSession(t, "srest07")
	linkFile = func(string, string) error { return &os.LinkError{Op: "link", Err: syscall.EPERM} }
	t.Cleanup(func() { linkFile = os.Link })
	if _, err := restoreCleanupArchive(id); err == nil {
		t.Fatal("a restore that could not link succeeded")
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), "srest07.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("a transcript was placed without a link: %v", err)
	}
	if _, ok := session.ReadMeta(m.Name); ok {
		t.Fatal("the meta came back")
	}
}

// TestRestoreKeepsTheLiveMeta (sixth review, serious): restoring an archive whose session is
// already back does not roll its meta back to the archived snapshot — a lock set since, for
// one, stays set.
func TestRestoreKeepsTheLiveMeta(t *testing.T) {
	cacheTestHome(t)
	id, m := archivedSession(t, "srest09")
	if _, err := restoreCleanupArchive(id); err != nil {
		t.Fatal(err)
	}
	live, _ := session.ReadMeta(m.Name)
	live.Locked = true
	live.Title = "renamed since"
	session.WriteMeta(live)
	if _, err := restoreCleanupArchive(id); err != nil {
		t.Fatal(err)
	}
	got, _ := session.ReadMeta(m.Name)
	if !got.Locked || got.Title != "renamed since" {
		t.Fatalf("meta rolled back to the archive: locked=%v title=%q", got.Locked, got.Title)
	}
}

// TestPurgeWaitsForAnUnfinishedRestore (sixth review, medium): after a restore that stopped
// half way, the archive cannot be purged — it is what keeps the session's cache reachable
// while its transcript is already back. Finishing the restore lifts that.
func TestPurgeWaitsForAnUnfinishedRestore(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes into a read-only directory anyway")
	}
	cacheTestHome(t)
	id, _ := archivedSession(t, "srest10")
	metaDir := session.MetaDir()
	if err := os.MkdirAll(metaDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(metaDir, 0o500); err != nil { // the meta cannot be written
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(metaDir, 0o700) })
	if _, err := restoreCleanupArchive(id); err == nil {
		t.Fatal("a restore whose meta could not be written reported success")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /cleanup/archives/{id}", handlePurgeCleanupArchive)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/cleanup/archives/"+id, nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("purge during an unfinished restore: status %d, want 409", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(cleanupStoreDir(), id+".tar.gz")); err != nil {
		t.Fatalf("the archive was purged: %v", err)
	}
	// Finish the restore; then the purge goes through.
	_ = os.Chmod(metaDir, 0o700)
	if _, err := restoreCleanupArchive(id); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/cleanup/archives/"+id, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("purge after the restore finished: status %d", rec.Code)
	}
}

// TestRestoreThatChangedNothingLeavesNoMark (seventh review M1): a restore that can never
// succeed — here, no hard links — must not leave its archive impossible to purge.
func TestRestoreThatChangedNothingLeavesNoMark(t *testing.T) {
	cacheTestHome(t)
	id, _ := archivedSession(t, "srest11")
	linkFile = func(string, string) error { return &os.LinkError{Op: "link", Err: syscall.EPERM} }
	t.Cleanup(func() { linkFile = os.Link })
	for i := 0; i < 3; i++ {
		if _, err := restoreCleanupArchive(id); !errors.Is(err, errRestoreStopped) {
			t.Fatalf("attempt %d: err = %v, want errRestoreStopped", i+1, err)
		}
	}
	if _, err := os.Lstat(restoringMarker(id)); !os.IsNotExist(err) {
		t.Fatalf("a restore that changed nothing left its mark: %v", err)
	}
	if _, err := purgeCleanupArchive(id); err != nil {
		t.Fatalf("purge after restores that changed nothing: %v", err)
	}
}

// TestStaleMarkDoesNotBlockPurge (seventh review M1): a mark whose half-done state is gone —
// the transcript an earlier attempt placed was removed since — lets the purge through.
func TestStaleMarkDoesNotBlockPurge(t *testing.T) {
	cacheTestHome(t)
	id, _ := archivedSession(t, "srest12")
	if err := os.WriteFile(restoringMarker(id), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := purgeCleanupArchive(id); err != nil {
		t.Fatalf("stale mark blocked the purge: %v", err)
	}
	if _, err := os.Lstat(restoringMarker(id)); !os.IsNotExist(err) {
		t.Fatal("the purge left the mark behind")
	}
	// The positive control: with the transcript back and no meta, the same mark blocks.
	id2, _ := archivedSession(t, "srest13")
	if err := os.WriteFile(restoringMarker(id2), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), "srest13.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := purgeCleanupArchive(id2); !errors.Is(err, errRestoreIncomplete) {
		t.Fatalf("half-done restore: err = %v, want errRestoreIncomplete", err)
	}
}

// TestStoppedRestoreIsA409 (seventh review L4): a restore that stopped part way is not "no
// such archive" — it answers 409 restore_incomplete, which the Console explains.
func TestStoppedRestoreIsA409(t *testing.T) {
	cacheTestHome(t)
	id, _ := archivedSession(t, "srest14")
	linkFile = func(string, string) error { return &os.LinkError{Op: "link", Err: syscall.EPERM} }
	t.Cleanup(func() { linkFile = os.Link })
	mux := http.NewServeMux()
	mux.HandleFunc("POST /cleanup/archives/{id}/restore", handleRestoreCleanupArchive)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/cleanup/archives/"+id+"/restore", nil))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "restore_incomplete") {
		t.Fatalf("status %d body %s, want 409 restore_incomplete", rec.Code, rec.Body)
	}
}

// TestUnreadableMarkedArchiveIsKept (eighth review L1/L3b): a marked archive whose manifest
// cannot be read at all is kept — the one refusal ADR 0097 allows to be permanent.
func TestUnreadableMarkedArchiveIsKept(t *testing.T) {
	cacheTestHome(t)
	if err := os.MkdirAll(cleanupStoreDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	id := "20260901-000000-broken"
	for name, b := range map[string][]byte{id + ".json": []byte("{"), id + ".tar.gz": []byte("not gzip")} {
		if err := os.WriteFile(filepath.Join(cleanupStoreDir(), name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(restoringMarker(id), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := purgeCleanupArchive(id); !errors.Is(err, errRestoreIncomplete) {
		t.Fatalf("err = %v, want errRestoreIncomplete", err)
	}
}

// TestRestoreThatCannotMarkIsStopped (eighth review L4): failing to write the mark is a
// stopped restore like any other — 409, the same message.
func TestRestoreThatCannotMarkIsStopped(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes into a read-only directory anyway")
	}
	cacheTestHome(t)
	id, _ := archivedSession(t, "srest15")
	if err := os.Chmod(cleanupStoreDir(), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(cleanupStoreDir(), 0o700) })
	if _, err := restoreCleanupArchive(id); !errors.Is(err, errRestoreStopped) {
		t.Fatalf("err = %v, want errRestoreStopped", err)
	}
}

// TestUsageSaysWhatWasNotJudged (ninth review L1): with no session store, session folders are
// not judged — reported as that, not as "too many files", which would call the whole cache
// figure a lower bound for the wrong reason.
func TestUsageSaysWhatWasNotJudged(t *testing.T) {
	cacheTestHome(t)
	if err := os.Remove(session.MetaDir()); err != nil {
		t.Fatal(err)
	}
	oldCacheDir(t, sessionx.CacheFeaturePasted, session.UUID("/d", "sgone01"), 5)
	rec := httptest.NewRecorder()
	handleCleanupUsage(rec, httptest.NewRequest(http.MethodGet, "/cleanup/usage", nil))
	var u cleanupUsage
	if err := json.Unmarshal(rec.Body.Bytes(), &u); err != nil {
		t.Fatal(err)
	}
	if u.Truncated || !u.Orphans.OK || u.Orphans.Unjudged != 1 || u.Orphans.Dirs != 0 {
		t.Fatalf("truncated=%v orphans=%+v — want unjudged=1, not a truncated walk", u.Truncated, u.Orphans)
	}
}

// A folder outside the browse root keeps its readable path but has nothing the Console could
// open it by (the file tree and the gallery only reach inside the browse root).
func TestPlaceOfOutsideTheBrowseRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_BROWSE_ROOT", filepath.Join(home, "repos"))
	p := placeOf(filepath.Join(home, ".cache", "agent-fleet"))
	if p.Path != "~/.cache/agent-fleet" || p.Browse != "" {
		t.Fatalf("outside the root: %+v", p)
	}
	if p := placeOf(filepath.Join(home, "repos", "x")); p.Browse != "x" {
		t.Fatalf("inside the root: %+v", p)
	}
}
