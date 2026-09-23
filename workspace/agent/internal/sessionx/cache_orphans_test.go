package sessionx

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// cacheFixture is a home with one cache subtree populated and every kind of owner the scan
// has to recognise. All files are back-dated past the grace window unless a test says not.
type cacheFixture struct {
	home string
	old  time.Time
}

func newCacheFixture(t *testing.T) *cacheFixture {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SESSIONS_DIR", "")
	return &cacheFixture{home: home, old: time.Now().Add(-48 * time.Hour)}
}

// dir makes <feature>/<name>/a.png (size n) and back-dates both.
func (f *cacheFixture) dir(t *testing.T, feature, name string, n int) string {
	t.Helper()
	d := filepath.Join(CacheRoot(), feature, name)
	if err := os.MkdirAll(d, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(d, "a.png")
	if err := os.WriteFile(p, bytes.Repeat([]byte{1}, n), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, x := range []string{p, d} {
		if err := os.Chtimes(x, f.old, f.old); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

func metaFor(name string) session.Meta {
	return session.Meta{Name: name, Dir: "/home/dev/repos/app", Kind: session.KindClaude}
}

func sid(m session.Meta) string { return session.UUID(m.Dir, m.Name) }

func marshalMetaT(t *testing.T, m session.Meta) string {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// writeTarball writes <id>.tar.gz with manifest.json first, and no sidecar.
func writeTarball(t *testing.T, id string, manifest []byte) {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	if err := tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0o600, Size: int64(len(manifest))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(manifest); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(CleanupArchiveDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(CleanupArchiveDir(), id+".tar.gz"), buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func manifestWith(t *testing.T, metas ...session.Meta) []byte {
	t.Helper()
	type s struct {
		Name string `json:"name"`
		Meta string `json:"meta"`
	}
	var ss []s
	for _, m := range metas {
		ss = append(ss, s{Name: m.Name, Meta: marshalMetaT(t, m)})
	}
	b, err := json.Marshal(map[string]any{"id": "x", "sessions": ss})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func orphanNames(o CacheOrphans) []string {
	var out []string
	for _, d := range o.Dirs {
		out = append(out, d.Name)
	}
	sort.Strings(out)
	return out
}

// TestScanCacheOrphansReachability is the whole rule in one fixture: every owner that can
// still name a directory keeps it, and only the two provably-gone ones are listed.
func TestScanCacheOrphansReachability(t *testing.T) {
	f := newCacheFixture(t)

	live := metaFor("slive01")
	session.WriteMeta(live)
	shelved := metaFor("sshelf1")
	shelved.Archived = true
	session.WriteMeta(shelved)

	// A deleted session still in the trash, once with a sidecar, once tarball-only (the
	// sidecar is best-effort on the writing side).
	inTrash := metaFor("strash1")
	if err := os.MkdirAll(CleanupArchiveDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(CleanupArchiveDir(), "a1.json"), manifestWith(t, inTrash), 0o600); err != nil {
		t.Fatal(err)
	}
	inTarball := metaFor("stball1")
	writeTarball(t, "a2", manifestWith(t, inTarball))

	gone := metaFor("sgone01")

	liveConv := &chatx.ChatConversation{ID: chatx.RandUUID(), Agent: "claude", Messages: []chatx.ChatMessage{}}
	if err := chatx.SaveConv(liveConv); err != nil {
		t.Fatal(err)
	}
	goneConv := chatx.RandUUID()

	for _, m := range []session.Meta{live, shelved, inTrash, inTarball, gone} {
		f.dir(t, CacheFeaturePasted, sid(m), 100)
	}
	f.dir(t, CacheFeaturePasted, "chat-"+liveConv.ID, 10)
	f.dir(t, CacheFeaturePasted, "chat-"+goneConv, 20)
	f.dir(t, CacheFeaturePasted, "not-ours", 30) // a name we never write: never ours to delete

	got, err := ScanCacheOrphans(CacheFeaturePasted, time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"chat-" + goneConv, sid(gone)}
	sort.Strings(want)
	if g := orphanNames(got); len(g) != 2 || g[0] != want[0] || g[1] != want[1] {
		t.Fatalf("orphans = %v, want %v", g, want)
	}
	if got.Bytes != 120 || got.Files != 2 {
		t.Fatalf("totals = %d bytes / %d files, want 120 / 2", got.Bytes, got.Files)
	}
}

// TestScanCacheOrphansGrace: a directory touched inside the grace window is left alone even
// when nothing owns it.
func TestScanCacheOrphansGrace(t *testing.T) {
	f := newCacheFixture(t)
	d := f.dir(t, CacheFeatureCodexViewImage, sid(metaFor("sfresh1")), 5)
	now := time.Now()
	if err := os.Chtimes(filepath.Join(d, "a.png"), now, now); err != nil {
		t.Fatal(err)
	}
	got, err := ScanCacheOrphans(CacheFeatureCodexViewImage, now, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Dirs) != 0 {
		t.Fatalf("a fresh directory was listed: %v", orphanNames(got))
	}
	// The positive control: the same directory, once it is old, is listed.
	if err := os.Chtimes(filepath.Join(d, "a.png"), f.old, f.old); err != nil {
		t.Fatal(err)
	}
	got, err = ScanCacheOrphans(CacheFeatureCodexViewImage, now, nil)
	if err != nil || len(got.Dirs) != 1 {
		t.Fatalf("old directory not listed: %v %v", orphanNames(got), err)
	}
}

// TestScanCacheOrphansRefusesWhatItCannotRead: an unreadable meta or archive would make its
// session's files look orphaned, so the scan fails instead of guessing.
func TestScanCacheOrphansRefusesWhatItCannotRead(t *testing.T) {
	t.Run("meta", func(t *testing.T) {
		f := newCacheFixture(t)
		f.dir(t, CacheFeaturePasted, sid(metaFor("sgone01")), 1)
		if err := os.MkdirAll(session.MetaDir(), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(session.MetaDir(), "sbroken.json"), []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ScanCacheOrphans(CacheFeaturePasted, time.Now(), nil); !errors.Is(err, ErrCacheScanUnsafe) {
			t.Fatalf("err = %v, want ErrCacheScanUnsafe", err)
		}
	})
	t.Run("archive", func(t *testing.T) {
		f := newCacheFixture(t)
		f.dir(t, CacheFeaturePasted, sid(metaFor("sgone01")), 1)
		if err := os.MkdirAll(CleanupArchiveDir(), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(CleanupArchiveDir(), "bad.tar.gz"), []byte("not gzip"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ScanCacheOrphans(CacheFeaturePasted, time.Now(), nil); !errors.Is(err, ErrCacheScanUnsafe) {
			t.Fatalf("err = %v, want ErrCacheScanUnsafe", err)
		}
	})
}

// TestRemoveCacheOrphans deletes exactly what the scan lists, and nothing outside the one
// feature it was asked for.
func TestRemoveCacheOrphans(t *testing.T) {
	f := newCacheFixture(t)
	live := metaFor("slive01")
	session.WriteMeta(live)
	keep := f.dir(t, CacheFeaturePasted, sid(live), 7)
	dead := f.dir(t, CacheFeaturePasted, sid(metaFor("sgone01")), 9)
	otherFeature := f.dir(t, CacheFeatureCodexViewImage, sid(metaFor("sgone02")), 11)

	got, err := RemoveCacheOrphans(CacheFeaturePasted, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Dirs) != 1 || got.Bytes != 9 {
		t.Fatalf("removed %v (%d bytes), want the one dead dir (9 bytes)", orphanNames(got), got.Bytes)
	}
	if _, err := os.Stat(dead); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dead dir still there: %v", err)
	}
	for _, p := range []string{keep, otherFeature} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s was removed: %v", p, err)
		}
	}
	if _, err := RemoveCacheOrphans("../..", time.Now()); err == nil {
		t.Fatal("an unknown feature was accepted")
	}
}

// TestCacheCleanupCandidates: one row per feature with something to take, a keep row when
// the scan cannot be trusted, nothing when there is nothing.
func TestCacheCleanupCandidates(t *testing.T) {
	f := newCacheFixture(t)
	if got := cacheCleanupCandidates(time.Now()); len(got) != 0 {
		t.Fatalf("empty cache produced rows: %+v", got)
	}
	f.dir(t, CacheFeaturePasted, sid(metaFor("sgone01")), 3)
	f.dir(t, CacheFeaturePasted, sid(metaFor("sgone02")), 4)
	got := cacheCleanupCandidates(time.Now())
	if len(got) != 1 {
		t.Fatalf("rows = %+v, want one pasted row", got)
	}
	c := got[0]
	if c.Type != "cache" || c.Action != "delete_cache" || c.ID != CacheFeaturePasted || c.Safety != "safe" ||
		c.Dirs != 2 || c.Bytes != 7 || c.ReasonKey != cleanReasonCacheOrphan {
		t.Fatalf("row = %+v", c)
	}

	if err := os.MkdirAll(session.MetaDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(session.MetaDir(), "sbroken.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	got = cacheCleanupCandidates(time.Now())
	for _, c := range got {
		if c.Action != "" || c.Safety != "keep" || c.ReasonKey != cleanReasonCacheUnsafe {
			t.Fatalf("unsafe scan offered an action: %+v", c)
		}
	}
	// codex-view-image does not exist in this home: nothing to judge, so no row. The keep
	// row is only for a subtree that has directories the scan could not clear.
	if len(got) != 1 || got[0].ID != CacheFeaturePasted {
		t.Fatalf("rows = %+v, want one keep row for pasted", got)
	}
}

// TestCacheOrphansFeatureSymlink (review ①): a feature directory that is a link would make
// ReadDir and RemoveAll act on wherever it points. That is refused; a link further up — a
// ~/.cache kept on persistent storage — is legitimate and keeps working.
func TestCacheOrphansFeatureSymlink(t *testing.T) {
	t.Run("feature dir is a link: refused, target untouched", func(t *testing.T) {
		f := newCacheFixture(t)
		elsewhere := filepath.Join(f.home, "elsewhere")
		victim := filepath.Join(elsewhere, sid(metaFor("sgone01")))
		if err := os.MkdirAll(victim, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(victim, f.old, f.old); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(CacheRoot(), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(elsewhere, filepath.Join(CacheRoot(), CacheFeaturePasted)); err != nil {
			t.Fatal(err)
		}
		if _, err := ScanCacheOrphans(CacheFeaturePasted, time.Now(), nil); !errors.Is(err, ErrCacheScanUnsafe) {
			t.Fatalf("scan through a linked feature dir: err = %v, want ErrCacheScanUnsafe", err)
		}
		if _, err := RemoveCacheOrphans(CacheFeaturePasted, time.Now()); err == nil {
			t.Fatal("remove through a linked feature dir was accepted")
		}
		if _, err := os.Stat(victim); err != nil {
			t.Fatalf("the link's target was deleted: %v", err)
		}
	})
	t.Run("~/.cache is a link: still works", func(t *testing.T) {
		f := newCacheFixture(t)
		store := filepath.Join(f.home, "persistent-cache")
		if err := os.MkdirAll(store, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(store, filepath.Join(f.home, ".cache")); err != nil {
			t.Fatal(err)
		}
		dead := f.dir(t, CacheFeaturePasted, sid(metaFor("sgone01")), 3)
		got, err := RemoveCacheOrphans(CacheFeaturePasted, time.Now())
		if err != nil || len(got.Dirs) != 1 {
			t.Fatalf("removed %v, err %v — want the one dead dir", orphanNames(got), err)
		}
		if _, err := os.Stat(dead); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("dead dir survived: %v", err)
		}
	})
}

// TestCacheOrphansMetaNameMismatch (review ②): the paste endpoint keys by the meta's FILE
// name, codex by the name inside it. A meta whose two names differ keeps both directories.
func TestCacheOrphansMetaNameMismatch(t *testing.T) {
	f := newCacheFixture(t)
	inside := metaFor("sinside")
	if err := os.MkdirAll(session.MetaDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	// File sfile01.json holding Name "sinside".
	if err := os.WriteFile(filepath.Join(session.MetaDir(), "sfile01.json"), []byte(marshalMetaT(t, inside)), 0o600); err != nil {
		t.Fatal(err)
	}
	byFile := f.dir(t, CacheFeaturePasted, session.UUID(inside.Dir, "sfile01"), 1)
	byInside := f.dir(t, CacheFeaturePasted, sid(inside), 1)
	got, err := RemoveCacheOrphans(CacheFeaturePasted, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Dirs) != 0 {
		t.Fatalf("removed %v from a live session", orphanNames(got))
	}
	for _, p := range []string{byFile, byInside} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s was removed: %v", p, err)
		}
	}
}

// TestCacheCleanupLock (review ③): a restore or purge holding the cleanup lock keeps a
// cache delete from scanning until it is done.
func TestCacheCleanupLock(t *testing.T) {
	newCacheFixture(t)
	done := make(chan struct{})
	WithCleanupLock(func() {
		go func() {
			_, _ = RemoveCacheOrphans(CacheFeaturePasted, time.Now())
			close(done)
		}()
		select {
		case <-done:
			t.Fatal("a cache delete ran while the cleanup lock was held")
		case <-time.After(100 * time.Millisecond):
		}
	})
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the cache delete never ran after the lock was released")
	}
}

// TestCacheOrphansBudget (review ④): the scan stops when its budget runs out, says so, and
// never lists a directory it did not walk to the end — that one could hold a fresh file.
func TestCacheOrphansBudget(t *testing.T) {
	f := newCacheFixture(t)
	for _, n := range []string{"sgone01", "sgone02", "sgone03"} {
		d := f.dir(t, CacheFeaturePasted, sid(metaFor(n)), 1)
		p := filepath.Join(d, "b.png") // two files per dir: 3 entries with the dir itself
		if err := os.WriteFile(p, []byte{1}, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, f.old, f.old); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(d, f.old, f.old); err != nil {
			t.Fatal(err)
		}
	}
	// Listing the three dirs costs 3, walking one (two files) costs 2: the first completes and
	// the second cannot start.
	budget := 5
	got, err := ScanCacheOrphans(CacheFeaturePasted, time.Now(), &budget)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Truncated || len(got.Dirs) != 1 || got.Files != 2 {
		t.Fatalf("truncated=%v dirs=%v files=%d, want truncated with the one complete dir", got.Truncated, orphanNames(got), got.Files)
	}
	// The positive control: with the default budget all three are listed and nothing is cut.
	got, err = ScanCacheOrphans(CacheFeaturePasted, time.Now(), nil)
	if err != nil || got.Truncated || len(got.Dirs) != 3 {
		t.Fatalf("default budget: truncated=%v dirs=%d err=%v", got.Truncated, len(got.Dirs), err)
	}
}

// TestCacheOrphansUnreadableInside (re-review ④, third review ②): a directory the walk cannot
// fully read may hide a fresh upload, so it is neither listed nor deleted — and it is counted
// as unreadable, not as a budget cut.
func TestCacheOrphansUnreadableInside(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a 000 directory anyway")
	}
	f := newCacheFixture(t)
	d := f.dir(t, CacheFeaturePasted, sid(metaFor("sgone01")), 1)
	hidden := filepath.Join(d, "sub")
	if err := os.Mkdir(hidden, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(hidden, 0o700) })
	for _, p := range []string{hidden, d} {
		if err := os.Chtimes(p, f.old, f.old); err != nil {
			t.Fatal(err)
		}
	}
	got, err := RemoveCacheOrphans(CacheFeaturePasted, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// A read error is its own state, not a budget cut: surveying again would not help.
	if len(got.Dirs) != 0 || got.Truncated || got.Unreadable != 1 {
		t.Fatalf("removed %v truncated=%v unreadable=%d — want nothing removed, one unreadable", orphanNames(got), got.Truncated, got.Unreadable)
	}
	if _, err := os.Stat(d); err != nil {
		t.Fatalf("partly unreadable dir was removed: %v", err)
	}
	// The positive control: readable again, the same directory goes.
	if err := os.Chmod(hidden, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(hidden, f.old, f.old); err != nil {
		t.Fatal(err)
	}
	got, err = RemoveCacheOrphans(CacheFeaturePasted, time.Now())
	if err != nil || len(got.Dirs) != 1 {
		t.Fatalf("readable dir not removed: %v %v", orphanNames(got), err)
	}
}

// TestCacheOrphansBudgetCoversReachability (re-review ②): reading metas and archives spends
// the budget too; when it runs out there, nothing is decided and the answer says so.
func TestCacheOrphansBudgetCoversReachability(t *testing.T) {
	f := newCacheFixture(t)
	f.dir(t, CacheFeaturePasted, sid(metaFor("sgone01")), 1)
	for _, n := range []string{"slive01", "slive02", "slive03", "slive04", "slive05"} {
		session.WriteMeta(metaFor(n))
	}
	// The dead directory alone costs 3 (its listing slot, itself, its file) and would fit;
	// the five metas do not. So only charging the metas can make this come back partial.
	budget := 3
	got, err := ScanCacheOrphans(CacheFeaturePasted, time.Now(), &budget)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Truncated || len(got.Dirs) != 0 {
		t.Fatalf("truncated=%v dirs=%v — want a partial answer that decides nothing", got.Truncated, orphanNames(got))
	}
}

// TestCacheCandidatePartial (re-review ①): a scan cut short still offers what it fully
// checked, but the row says it is partial; one that could check nothing is a keep row.
func TestCacheCandidatePartial(t *testing.T) {
	f := newCacheFixture(t)
	old := CacheScanMaxEntries
	t.Cleanup(func() { CacheScanMaxEntries = old })
	for _, n := range []string{"sgone01", "sgone02"} {
		f.dir(t, CacheFeaturePasted, sid(metaFor(n)), 1)
	}
	// One entry for the first listing slot and two for its walk (dir + file): the first
	// directory completes, the second never starts.
	CacheScanMaxEntries = 3
	got := cacheCleanupCandidates(time.Now())
	if len(got) != 1 || got[0].Action != "delete_cache" || !got[0].Truncated || got[0].Dirs != 1 ||
		got[0].ReasonKey != cleanReasonCachePartial {
		t.Fatalf("rows = %+v, want one partial delete row covering the one checked dir", got)
	}
	CacheScanMaxEntries = 0
	got = cacheCleanupCandidates(time.Now())
	if len(got) != 1 || got[0].Action != "" || got[0].Safety != "keep" || !got[0].Truncated {
		t.Fatalf("rows = %+v, want one keep row that offers nothing", got)
	}
}

// TestCacheCandidateUnreadable (third review ②): a folder that cannot be read gets its own
// note, which says surveying again will not help — not the budget's "survey again".
func TestCacheCandidateUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a 000 directory anyway")
	}
	f := newCacheFixture(t)
	d := f.dir(t, CacheFeaturePasted, sid(metaFor("sgone01")), 1)
	f.dir(t, CacheFeaturePasted, sid(metaFor("sgone02")), 1)
	hidden := filepath.Join(d, "sub")
	if err := os.Mkdir(hidden, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(hidden, 0o700) })
	got := cacheCleanupCandidates(time.Now())
	if len(got) != 1 || got[0].Action != "delete_cache" || got[0].Dirs != 1 || got[0].Unreadable != 1 ||
		got[0].Truncated || got[0].ReasonKey != cleanReasonCacheUnread {
		t.Fatalf("rows = %+v, want the readable dir offered with the unreadable note", got)
	}
}

// TestReadDirBudgetIsABound (third review ③): the budget limits what is READ, not what is
// counted after reading everything — a huge directory is listed a chunk at a time.
func TestReadDirBudgetIsABound(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 2500; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%04d", i)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	budget := 100
	got, err := readDirPathBudget(dir, &budget)
	if !errors.Is(err, errBudget) {
		t.Fatalf("err = %v, want errBudget", err)
	}
	if len(got) > 100 {
		t.Fatalf("read %d entries on a budget of 100", len(got))
	}
	// Exactly enough budget is enough: running out on the last entry is not a cut.
	budget = 2500
	got, err = readDirPathBudget(dir, &budget)
	if err != nil || len(got) != 2500 {
		t.Fatalf("exact budget: %d entries, err %v", len(got), err)
	}
}
