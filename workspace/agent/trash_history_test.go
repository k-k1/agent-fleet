package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// A trashed shell's terminal history rides in its archive while history retention is on, and a
// restore puts it back (item 7 of #5122).

func historyEnv(t *testing.T, days string) string {
	t.Helper()
	home := handoverEnv(t)
	t.Setenv("AF_TERMINAL_HISTORY_DIR", "")
	t.Setenv("AF_TERMINAL_HISTORY_RETENTION_DAYS", days)
	return home
}

func writeHistory(t *testing.T, name, body string, mod time.Time) {
	t.Helper()
	p := terminalHistoryPath(name)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mod, mod); err != nil {
		t.Fatal(err)
	}
}

func TestTrashedShellKeepsTerminalHistoryForItsRetention(t *testing.T) {
	home := historyEnv(t, "7")
	m := shellSession(t, home, "hist001")
	mod := time.Now().Add(-48 * time.Hour).Truncate(time.Second)
	writeHistory(t, m.Name, "\x1b[1mscreen\x1b[0m", mod)

	arch, code, err := trashSession(m, false)
	if err != nil {
		t.Fatalf("trash: %s %v", code, err)
	}
	if _, err := os.Stat(terminalHistoryPath(m.Name)); err == nil {
		t.Fatal("the history should leave its live path with the shell")
	}
	if _, err := restoreCleanupArchive(arch); err != nil {
		t.Fatal(err)
	}
	if got := mustRead(t, terminalHistoryPath(m.Name)); got != "\x1b[1mscreen\x1b[0m" {
		t.Fatalf("restored history = %q", got)
	}
	st, _ := os.Stat(terminalHistoryPath(m.Name))
	if !st.ModTime().Equal(mod) {
		t.Fatalf("history mtime = %v, want the original %v (the janitor ages it from there)", st.ModTime(), mod)
	}
}

func TestTrashedShellHistoryIsNotArchivedWithRetentionOff(t *testing.T) {
	home := historyEnv(t, "")
	m := shellSession(t, home, "hist002")
	writeHistory(t, m.Name, "ephemeral", time.Now())
	arch, _, err := trashSession(m, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range listCleanupArchives() {
		if a.ID == arch && a.Sessions[0].History != "" {
			t.Fatal("history was archived although retention is off")
		}
	}
	// Retention turned on afterwards must not resurrect what was never kept.
	t.Setenv("AF_TERMINAL_HISTORY_RETENTION_DAYS", "7")
	if _, err := restoreCleanupArchive(arch); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(terminalHistoryPath(m.Name)); err == nil {
		t.Fatal("history appeared out of nowhere")
	}
}

func TestRestoreDropsHistoryOlderThanTheRetention(t *testing.T) {
	home := historyEnv(t, "7")
	m := shellSession(t, home, "hist003")
	writeHistory(t, m.Name, "old", time.Now().Add(-24*time.Hour))
	arch, _, err := trashSession(m, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AF_TERMINAL_HISTORY_RETENTION_DAYS", "1") // the history is now past its retention
	time.Sleep(10 * time.Millisecond)
	if _, err := restoreCleanupArchive(arch); err != nil {
		t.Fatal(err)
	}
	if _, ok := session.ReadMeta(m.Name); !ok {
		t.Fatal("the shell itself must still come back")
	}
	if _, err := os.Stat(terminalHistoryPath(m.Name)); err == nil {
		t.Fatal("history past its retention was restored")
	}
}

func TestRestoreKeepsALiveTerminalHistory(t *testing.T) {
	home := historyEnv(t, "7")
	m := shellSession(t, home, "hist004")
	writeHistory(t, m.Name, "archived", time.Now())
	arch, _, err := trashSession(m, false)
	if err != nil {
		t.Fatal(err)
	}
	writeHistory(t, m.Name, "newer", time.Now())
	if _, err := restoreCleanupArchive(arch); err != nil {
		t.Fatal(err)
	}
	if got := mustRead(t, terminalHistoryPath(m.Name)); got != "newer" {
		t.Fatalf("restore replaced a newer history: %q", got)
	}
}

func TestSSMHistoryRoundTripsAndRetentionOffAtRestoreRestoresOnlyTheSession(t *testing.T) {
	home := historyEnv(t, "7")
	mod := time.Now().Add(-time.Hour).Truncate(time.Second)
	ssm := session.Meta{Name: "hist005", Dir: home, Kind: session.KindSSM}
	session.WriteMeta(ssm)
	writeHistory(t, ssm.Name, "ssm screen", mod)
	arch, _, err := trashSession(ssm, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restoreCleanupArchive(arch); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(terminalHistoryPath(ssm.Name))
	if err != nil || !st.ModTime().Equal(mod) || mustRead(t, terminalHistoryPath(ssm.Name)) != "ssm screen" {
		t.Fatalf("ssm history did not round-trip: %v %v", st, err)
	}

	sh := shellSession(t, home, "hist006")
	writeHistory(t, sh.Name, "kept", time.Now())
	arch, _, err = trashSession(sh, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AF_TERMINAL_HISTORY_RETENTION_DAYS", "")
	if _, err := restoreCleanupArchive(arch); err != nil {
		t.Fatal(err)
	}
	if _, ok := session.ReadMeta(sh.Name); !ok {
		t.Fatal("the shell must come back with retention off")
	}
	if _, err := os.Stat(terminalHistoryPath(sh.Name)); err == nil {
		t.Fatal("history restored with retention off")
	}
}

func TestTrashDoesNotArchiveWhatAHistorySymlinkPointsAt(t *testing.T) {
	home := historyEnv(t, "7")
	m := shellSession(t, home, "hist007")
	secret := filepath.Join(home, "secret.txt")
	if err := os.WriteFile(secret, []byte("not a history"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(terminalHistoryPath(m.Name)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, terminalHistoryPath(m.Name)); err != nil {
		t.Fatal(err)
	}
	arch, _, err := trashSession(m, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range listCleanupArchives() {
		if a.ID == arch && a.Sessions[0].History != "" {
			t.Fatal("a symlinked history was archived")
		}
	}
}

func TestRestoreIgnoresAHistoryRecordNamedForAnotherSession(t *testing.T) {
	home := historyEnv(t, "7")
	m := shellSession(t, home, "hist008")
	writeHistory(t, m.Name, "mine", time.Now())
	arch, _, err := trashSession(m, false)
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(cleanupStoreDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	f, _, err := openArchiveFile(root, arch+".tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	man, payloads, err := readCleanupTar(f, arch)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	man.Sessions[0].Name = "other01"
	// Rewrite the archive under the same id so the restore sees the forged record.
	if err := root.Remove(arch + ".tar.gz"); err != nil {
		t.Fatal(err)
	}
	if err := root.Remove(arch + ".json"); err != nil {
		t.Fatal(err)
	}
	if err := writeCleanupBundleIn(root, &man, payloads, false); err != nil || man.ID != arch {
		t.Fatalf("cannot rewrite the fixture archive: %v (id %s)", err, man.ID)
	}
	if _, err := restoreCleanupArchive(arch); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(terminalHistoryPath("other01")); err == nil {
		t.Fatal("history was written under a name the restore never locked")
	}
}

func TestListFlagsASpaceSizeThatRanOutOfBudget(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := cleanupStoreDir()
	if err := os.MkdirAll(store, 0o700); err != nil {
		t.Fatal(err)
	}
	put := func(id string, files int) {
		b, _ := json.Marshal(cleanupManifest{ID: id, Space: &spaceTombstone{Folder: id}})
		if err := os.WriteFile(filepath.Join(store, id+".json"), b, 0o600); err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(store, spaceTrashName(id))
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < files; i++ {
			if err := os.WriteFile(filepath.Join(dir, string(rune('a'+i))), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	put("20260101T000000Z-small", 1)
	put("20260102T000000Z-large", 30)
	put("20260103T000000Z-late", 1)

	got := map[string]cleanupManifest{}
	budget := 100
	for _, m := range listArchives(&budget, true) {
		got[m.ID] = m
	}
	for id, m := range got {
		if m.BytesPartial {
			t.Fatalf("%s flagged partial with budget to spare", id)
		}
	}
	budget = 10
	got = map[string]cleanupManifest{}
	for _, m := range listArchives(&budget, true) {
		got[m.ID] = m
	}
	if got["20260101T000000Z-small"].BytesPartial {
		t.Fatal("a space walked in full was flagged")
	}
	if !got["20260103T000000Z-late"].BytesPartial {
		t.Fatal("the space walked after the budget ran out must be flagged: its size is 0 here")
	}
	if !got["20260102T000000Z-large"].BytesPartial {
		t.Fatal("the walk that was cut short was not flagged")
	}
}
