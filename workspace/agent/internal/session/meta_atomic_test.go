package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func atomicDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "sessions")
	t.Setenv("AF_SESSIONS_DIR", dir)
	return dir
}

// holdWriter runs WriteMeta(m) and parks it in the gap; the returned release lets it finish.
func holdWriter(t *testing.T, m Meta) (release func()) {
	t.Helper()
	inGap, rel := make(chan struct{}), make(chan struct{})
	var once sync.Once
	metaWriteGap = func() {
		once.Do(func() { close(inGap) })
		<-rel
	}
	done := make(chan struct{})
	go func() { defer close(done); WriteMeta(m) }()
	select {
	case <-inGap:
	case <-time.After(10 * time.Second):
		t.Fatal("the writer never reached the gap")
	}
	return func() {
		close(rel)
		<-done
		metaWriteGap = nil
	}
}

// A reader that arrives while a writer is mid-write sees the old content or the new, never a
// cut file.
func TestWriteMetaReaderNeverSeesACutFile(t *testing.T) {
	atomicDir(t)
	t.Cleanup(func() { metaWriteGap = nil })
	WriteMeta(Meta{Name: "s1", Kind: KindShell, Title: "old"})

	release := holdWriter(t, Meta{Name: "s1", Kind: KindShell, Title: "new"})
	got, st := ReadMetaState("s1")
	if st != MetaOK || got.Title != "old" {
		t.Errorf("reader during a write: state=%v title=%q, want MetaOK and the old content", st, got.Title)
	}
	if _, ok := ReadMeta("s1"); !ok {
		t.Error("ReadMeta answered not-found during a write")
	}
	found := false
	for _, m := range ListMetas() {
		found = found || m.Name == "s1"
	}
	if !found {
		t.Error("ListMetas left the session out during a write")
	}
	release()
	if got, st := ReadMetaState("s1"); st != MetaOK || got.Title != "new" {
		t.Errorf("after the write: state=%v title=%q, want the new content", st, got.Title)
	}
}

// Writers of one meta at once and readers throughout: every read is whole, the last rename wins,
// and nothing but the meta is left in the directory.
func TestWriteMetaWritersAndReadersRace(t *testing.T) {
	dir := atomicDir(t)
	WriteMeta(Meta{Name: "r1", Kind: KindShell})
	stop := make(chan struct{})
	var rd, wr sync.WaitGroup
	bad := make(chan string, 1)
	for i := 0; i < 4; i++ {
		rd.Add(1)
		go func() {
			defer rd.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if _, st := ReadMetaState("r1"); st != MetaOK {
					select {
					case bad <- fmt.Sprintf("state %v", st):
					default:
					}
					return
				}
				ListMetas()
			}
		}()
	}
	for w := 0; w < 4; w++ {
		wr.Add(1)
		go func() {
			defer wr.Done()
			for i := 0; i < 200; i++ {
				WriteMeta(Meta{Name: "r1", Kind: KindShell, Title: strings.Repeat("x", (i*37)%4000)})
			}
		}()
	}
	wr.Wait()
	close(stop)
	rd.Wait()
	select {
	case s := <-bad:
		t.Fatalf("a reader met an unreadable meta: %s", s)
	default:
	}
	ents, err := os.ReadDir(dir)
	if err != nil || len(ents) != 1 || ents[0].Name() != "r1.json" {
		t.Fatalf("directory after the race = %v (err %v), want only r1.json", ents, err)
	}
	if fi, _ := os.Stat(MetaPath("r1")); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", fi.Mode().Perm())
	}
}

// A meta deleted between the temporary file and the rename does not come back, and the
// temporary file does not stay.
func TestWriteMetaDeleteInTheGapStaysDeleted(t *testing.T) {
	dir := atomicDir(t)
	t.Cleanup(func() { metaWriteGap = nil })
	WriteMeta(Meta{Name: "d1", Kind: KindShell})
	release := holdWriter(t, Meta{Name: "d1", Kind: KindShell, Title: "stale"})
	releaseTrash, err := RemoveMetaAndLineage("d1")
	if err != nil {
		t.Fatal(err)
	}
	releaseTrash()
	release()
	if _, st := ReadMetaState("d1"); st != MetaAbsent {
		t.Fatalf("state = %v, want MetaAbsent: the write brought a deleted meta back", st)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Fatalf("directory not empty after the refused write: %v", ents)
	}
}

func TestReadMetaStateTellsTheThreeCasesApart(t *testing.T) {
	dir := atomicDir(t)
	WriteMeta(Meta{Name: "ok1", Kind: KindShell})
	for name, body := range map[string]string{"empty1": "", "cut1": `{"name":"cut1","ki`, "notjson1": "nope"} {
		if err := os.WriteFile(filepath.Join(dir, name+".json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for name, want := range map[string]MetaState{"ok1": MetaOK, "empty1": MetaUnreadable, "cut1": MetaUnreadable, "notjson1": MetaUnreadable, "gone1": MetaAbsent} {
		if _, st := ReadMetaState(name); st != want {
			t.Errorf("%s: state %v, want %v", name, st, want)
		}
	}
	metas, bad, err := ListMetasChecked()
	if err != nil || len(metas) != 1 || metas[0].Name != "ok1" || len(bad) != 3 {
		t.Fatalf("ListMetasChecked = %v, %v, %v", metas, bad, err)
	}
	// A temporary file is not a session.
	if err := os.WriteFile(filepath.Join(dir, ".meta-1.tmp"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, bad, _ := ListMetasChecked(); len(bad) != 3 {
		t.Fatalf("a .meta-*.tmp file was listed: %v", bad)
	}
}
