//go:build linux

package egressagent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestFileGenSurvivesRenameAndIsNewForANewFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "log")
	appendFile(t, p, "x\n")
	g1 := genOf(t, p)
	if g1 != genOf(t, p) {
		t.Fatal("a second look at the same file gave a new generation")
	}
	if err := os.Rename(p, p+".0"); err != nil {
		t.Fatal(err)
	}
	if genOf(t, p+".0") != g1 {
		t.Fatal("the generation did not survive a rename")
	}
	appendFile(t, p, "x\n")
	if genOf(t, p) == g1 {
		t.Fatal("a new file took the old file's generation")
	}
}

func TestFileGenRefusesWhatItCannotTag(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root may set an xattr on a read-only file")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "log")
	appendFile(t, p, "x\n")
	if err := os.Chmod(p, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := checkLogTaggable(p); err == nil || !strings.Contains(err.Error(), genXattr) {
		t.Fatalf("checkLogTaggable = %v, want a refusal naming %s", err, genXattr)
	}
	cfg := Config{PolicyURL: "http://x", IngestURL: "http://x", Token: "t", LogPath: p, StatePath: filepath.Join(dir, "state.json")}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := Run(ctx, cfg); err == nil {
		t.Fatal("Run must refuse a log it cannot tag")
	}
}

func TestFileGenRefusesAForeignValue(t *testing.T) {
	p := filepath.Join(t.TempDir(), "log")
	appendFile(t, p, "x\n")
	if err := unix.Setxattr(p, genXattr, []byte("not-a-generation"), 0); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := fileGen(f); err == nil {
		t.Fatal("a value the agent did not write must not be used as an identity")
	}
}

func genOf(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	g, err := fileGen(f)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestShipRecreatedFileOnReusedInodeGetsNewIDs(t *testing.T) {
	// The old log is deleted and a new one takes its inode at once, starting with the same
	// line: inode, birth time (the kernel clock's resolution) and first line can all match.
	// Its lines must not inherit the old file's offsets or ids.
	f := newShipFixture(t)
	ctx := context.Background()
	first := line(1, "10.0.1.5", "a.github.com")
	appendFile(t, f.log, first+line(2, "10.0.1.5", "b.github.com"))
	if err := f.sh.pass(ctx); err != nil {
		t.Fatal(err)
	}
	oldIno := inodeOf(t, f.log)
	reused := false
	for i := 0; i < 50 && !reused; i++ {
		if err := os.Remove(f.log); err != nil {
			t.Fatal(err)
		}
		appendFile(t, f.log, first)
		reused = inodeOf(t, f.log) == oldIno
	}
	if !reused {
		t.Skip("the filesystem did not reuse the inode")
	}
	appendFile(t, f.log, line(3, "10.0.1.5", "c.github.com"))
	f.restart(t) // the agent was down across the replacement: its state still has the old file
	if err := f.sh.pass(ctx); err != nil {
		t.Fatal(err)
	}
	ids, n := f.cp.ids()
	if len(ids) != 4 || n != 4 {
		t.Fatalf("%d ids over %d events, want 4 and 4: %v", len(ids), n, ids)
	}
}

func inodeOf(t *testing.T, path string) uint64 {
	t.Helper()
	fh, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	var st unix.Stat_t
	if err := unix.Fstat(int(fh.Fd()), &st); err != nil {
		t.Fatal(err)
	}
	return st.Ino
}
