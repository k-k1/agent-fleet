package statemig

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// seed builds a source tree shaped like the real ~/.config/agent-fleet: two state entries,
// one credential the migration must not touch, and the borrowed-credential symlink that
// makes following links a leak rather than a copy.
func seed(t *testing.T) (src, dst, secretTarget string) {
	t.Helper()
	root := t.TempDir()
	src = filepath.Join(root, "config")
	dst = filepath.Join(root, "state")
	secretTarget = filepath.Join(root, "real-auth.json")

	write(t, filepath.Join(src, "sessions", "alpha.json"), `{"name":"alpha"}`)
	write(t, filepath.Join(src, "sessions", "beta.json"), `{"name":"beta"}`)
	write(t, filepath.Join(src, "session-status", "sid.json"), `{"state":"idle"}`)
	write(t, secretTarget, `{"token":"real"}`)
	if err := os.MkdirAll(filepath.Join(src, "chat-codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secretTarget, filepath.Join(src, "chat-codex", "auth.json")); err != nil {
		t.Fatal(err)
	}
	// Two things that stay: the credential store and a user's own configuration.
	write(t, filepath.Join(src, "secrets.enc"), "ciphertext")
	write(t, filepath.Join(src, "user-notes.md"), "how I work")
	return src, dst, secretTarget
}

func write(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}

func TestMovesStateAndLeavesTheDurableHalfAlone(t *testing.T) {
	src, dst, _ := seed(t)

	res := run(src, dst)
	if len(res.Errs) > 0 {
		t.Fatalf("errors: %v", res.Errs)
	}
	if got := read(t, filepath.Join(dst, "sessions", "alpha.json")); got != `{"name":"alpha"}` {
		t.Fatalf("session meta did not move: %q", got)
	}
	if read(t, filepath.Join(dst, "session-status", "sid.json")) == "" {
		t.Fatal("session-status did not move")
	}
	if _, err := os.Stat(filepath.Join(src, "sessions")); !os.IsNotExist(err) {
		t.Fatalf("the source entry should be gone once it is done, got %v", err)
	}
	// The credential store and the user's notes are not in Entries and must be untouched —
	// this is the ADR 0045 line the migration is not allowed to cross.
	if read(t, filepath.Join(src, "secrets.enc")) != "ciphertext" {
		t.Fatal("secrets.enc was touched")
	}
	if read(t, filepath.Join(src, "user-notes.md")) != "how I work" {
		t.Fatal("user-notes.md was touched")
	}
	for _, name := range []string{"secrets.enc", "user-notes.md"} {
		if _, err := os.Stat(filepath.Join(dst, name)); !os.IsNotExist(err) {
			t.Fatalf("%s must not appear in the state dir", name)
		}
	}
}

// A borrowed credential is a symlink into the volume that still holds it. Copying it as a
// file would put the plaintext token on the home volume and leave the copy-back reconcile
// writing into a file nothing else reads.
func TestSymlinkIsCopiedAsASymlink(t *testing.T) {
	src, dst, target := seed(t)

	if errs := run(src, dst).Errs; len(errs) > 0 {
		t.Fatalf("errors: %v", errs)
	}
	link := filepath.Join(dst, "chat-codex", "auth.json")
	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("lstat: %v", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("auth.json was dereferenced into a real file — the token is now on the home volume")
	}
	if got, _ := os.Readlink(link); got != target {
		t.Fatalf("link target = %q, want %q", got, target)
	}
}

// Rule 1. A hook subprocess writing to the state dir while this runs must win: it wrote the
// current truth, the copy under .config is what the old build left behind.
func TestDestinationIsNeverOverwritten(t *testing.T) {
	src, dst, _ := seed(t)
	write(t, filepath.Join(dst, "session-status", "sid.json"), `{"state":"working"}`)

	if errs := run(src, dst).Errs; len(errs) > 0 {
		t.Fatalf("errors: %v", errs)
	}
	if got := read(t, filepath.Join(dst, "session-status", "sid.json")); got != `{"state":"working"}` {
		t.Fatalf("live file was overwritten by the stale one: %q", got)
	}
}

// Rule 2. Killed mid-entry, the next boot copies what is left rather than deciding the
// directory is done because it exists.
func TestInterruptedRunIsFinishedByTheNext(t *testing.T) {
	src, dst, _ := seed(t)
	// What a run killed after one file looks like: one meta at the destination, the other
	// still only at the source, and no marker.
	write(t, filepath.Join(dst, "sessions", "alpha.json"), `{"name":"alpha"}`)
	if err := os.Remove(filepath.Join(src, "sessions", "alpha.json")); err != nil {
		t.Fatal(err)
	}

	if errs := run(src, dst).Errs; len(errs) > 0 {
		t.Fatalf("errors: %v", errs)
	}
	for _, name := range []string{"alpha.json", "beta.json"} {
		if _, err := os.Stat(filepath.Join(dst, "sessions", name)); err != nil {
			t.Fatalf("%s missing after the second run: %v", name, err)
		}
	}
}

// Rule 3. Deleting a session must stay deleted: without the marker (and the source removal)
// the next boot would restore its meta from the leftover under .config and the session would
// come back from the dead in the Console's list.
func TestAFinishedEntryIsNotMigratedTwice(t *testing.T) {
	src, dst, _ := seed(t)
	if errs := run(src, dst).Errs; len(errs) > 0 {
		t.Fatalf("errors: %v", errs)
	}
	if err := os.Remove(filepath.Join(dst, "sessions", "beta.json")); err != nil {
		t.Fatal(err)
	}
	// A leftover the removal above could not clear (a read-only source, say).
	write(t, filepath.Join(src, "sessions", "beta.json"), `{"name":"beta"}`)

	res := run(src, dst)
	if res.Files != 0 {
		t.Fatalf("copied %d file(s) from a finished entry", res.Files)
	}
	if _, err := os.Stat(filepath.Join(dst, "sessions", "beta.json")); !os.IsNotExist(err) {
		t.Fatal("a deleted session came back")
	}
}

// A run reads the marker when it starts and writes it when it ends, so the interleaving that
// matters is another agent finishing something IN BETWEEN — writing back the stale snapshot
// wholesale would drop their entry. That entry is then migrated a second time, which is how
// something the user deleted in the meantime comes back.
//
// Note what this does NOT reproduce: seeding the marker before run() and letting run() write
// it, because run() reads that seed into the very map it writes back. Written that way the
// test passes with or without the merge (measured) — it has to be the write that starts from
// a stale snapshot.
func TestMarkerKeepsEntriesWrittenByAnotherRun(t *testing.T) {
	_, dst, _ := seed(t)
	mine := readMarker(dst) // the snapshot a run starts from: empty
	mine.Done["sessions"] = "2026-09-17T00:00:00Z"

	// Another agent finishes an entry and records it while we were copying.
	theirs := readMarker(dst)
	theirs.Done["chat-wd"] = "2026-09-17T00:00:01Z"
	if err := writeMarker(dst, theirs); err != nil {
		t.Fatal(err)
	}

	if err := writeMarker(dst, mine); err != nil {
		t.Fatal(err)
	}
	got := readMarker(dst)
	for _, entry := range []string{"chat-wd", "sessions"} {
		if _, ok := got.Done[entry]; !ok {
			t.Errorf("%q fell out of the marker", entry)
		}
	}
	if _, err := os.Stat(filepath.Join(dst, markerName+".tmp")); !os.IsNotExist(err) {
		t.Error("the temporary marker was left behind")
	}
}

func TestEmptySourceIsANoOp(t *testing.T) {
	root := t.TempDir()
	res := run(filepath.Join(root, "config"), filepath.Join(root, "state"))
	if res.Files != 0 || res.Entries != 0 || len(res.Errs) > 0 {
		t.Fatalf("not a no-op: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(root, "state", markerName)); !os.IsNotExist(err) {
		t.Fatal("a marker was written with nothing to mark")
	}
}

// A socket or fifo under a migrated tree belongs to a process that is running right now.
// There is nothing to copy, and removing it would take that process's listener away — so it
// is skipped, and the entry stays unfinished rather than being swept up by RemoveAll.
func TestALiveSocketIsNeitherCopiedNorDeleted(t *testing.T) {
	src, dst, _ := seed(t)
	fifo := filepath.Join(src, "af-db", "s.PGSQL.5432")
	if err := os.MkdirAll(filepath.Dir(fifo), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("cannot create a fifo here: %v", err)
	}

	res := run(src, dst)
	if len(res.Errs) > 0 {
		t.Fatalf("errors: %v", res.Errs)
	}
	if res.Skipped != 1 {
		t.Fatalf("Skipped = %d, want 1", res.Skipped)
	}
	if _, err := os.Lstat(fifo); err != nil {
		t.Fatalf("the live socket was removed: %v", err)
	}
	if _, ok := readMarker(dst).Done["af-db"]; ok {
		t.Error("an entry with something left behind must not be marked finished — " +
			"the next boot would never look at it again")
	}
}

// The chat scratch borrows the real credentials through symlinks, but a token refresh
// replaces the link with a REAL FILE (that is why reconcileChatCreds exists). Migrating one
// in that state writes the plaintext token onto the home volume, which is what ADR 0045
// decision 3-6 forbids — so it stays where it is and the next chat turn re-links it.
func TestARefreshedTokenIsLeftOnTheOldVolume(t *testing.T) {
	src, dst, _ := seed(t)
	real := filepath.Join(src, "chat-codex", "auth.json")
	if err := os.Remove(real); err != nil { // the seed made it a symlink
		t.Fatal(err)
	}
	write(t, real, `{"tokens":{"access_token":"secret"}}`)

	res := run(src, dst)
	if len(res.Errs) > 0 {
		t.Fatalf("errors: %v", res.Errs)
	}
	if _, err := os.Stat(filepath.Join(dst, "chat-codex", "auth.json")); !os.IsNotExist(err) {
		t.Fatal("a plaintext token was copied onto the home volume")
	}
	if got := read(t, real); got != `{"tokens":{"access_token":"secret"}}` {
		t.Fatalf("the token was removed from the volume that owns it: %q", got)
	}
}
