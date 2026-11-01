package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
)

// ADR 0115 decision 9: a move target's handover files are part of its conversation, so the
// trash carries them and a restore puts them back.

func handoverEnv(t *testing.T) (home string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))
	return home
}

func shellSession(t *testing.T, home, name string) session.Meta {
	t.Helper()
	m := session.Meta{Name: name, Dir: home, Kind: session.KindShell}
	session.WriteMeta(m)
	return m
}

func writeHandover(t *testing.T, target, source, body string) string {
	t.Helper()
	p, err := sessionx.WriteHandoverFile(target, source, body)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func mustRead(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestTrashCarriesHandoverFilesAndRestorePutsThemBack(t *testing.T) {
	home := handoverEnv(t)
	m := shellSession(t, home, "tgt0001")
	body := "# handover\n\n日本語 \x00 bytes\n"
	p1 := writeHandover(t, m.Name, "srca001", body)
	p2 := writeHandover(t, m.Name, "srcb001", "second")
	scratch := filepath.Join(paths.SessionWorkDir(m.Name), "probe.txt")
	if err := os.WriteFile(scratch, []byte("throwaway"), 0o600); err != nil {
		t.Fatal(err)
	}

	arch, code, err := trashSession(m, false)
	if err != nil {
		t.Fatalf("trash: %s %v", code, err)
	}
	if _, err := os.Stat(paths.SessionWorkDir(m.Name)); err == nil {
		t.Fatal("the work directory should be gone after the delete")
	}
	if _, err := restoreCleanupArchive(arch); err != nil {
		t.Fatal(err)
	}
	if got := mustRead(t, p1); got != body {
		t.Fatalf("handover-srca001.md = %q, want identical bytes", got)
	}
	if got := mustRead(t, p2); got != "second" {
		t.Fatalf("handover-srcb001.md = %q", got)
	}
	if _, err := os.Stat(scratch); err == nil {
		t.Fatal("a throwaway file that is not a handover file came back")
	}
}

func TestRestoreKeepsAHandoverFileThatIsAlreadyThere(t *testing.T) {
	home := handoverEnv(t)
	m := shellSession(t, home, "tgt0002")
	p := writeHandover(t, m.Name, "srca001", "archived")
	arch, _, err := trashSession(m, false)
	if err != nil {
		t.Fatal(err)
	}
	writeHandover(t, m.Name, "srca001", "newer")
	if _, err := restoreCleanupArchive(arch); err != nil {
		t.Fatal(err)
	}
	if got := mustRead(t, p); got != "newer" {
		t.Fatalf("restore overwrote the file already there: %q", got)
	}
}

func TestChainSurvivesDeletingTheEarlierSessions(t *testing.T) {
	home := handoverEnv(t)
	a := shellSession(t, home, "chaina1")
	b := shellSession(t, home, "chainb1")
	c := shellSession(t, home, "chainc1")
	pa := writeHandover(t, b.Name, a.Name, "A's history")
	if _, err := sessionx.CopyHandoverChain(a.Name, b.Name); err != nil {
		t.Fatal(err)
	}
	pb := writeHandover(t, c.Name, b.Name, "B's history")
	if _, err := sessionx.CopyHandoverChain(b.Name, c.Name); err != nil {
		t.Fatal(err)
	}
	cA := filepath.Join(paths.SessionWorkDir(c.Name), sessionx.HandoverFileName(a.Name))
	cB := filepath.Join(paths.SessionWorkDir(c.Name), sessionx.HandoverFileName(b.Name))
	_, _ = pa, pb

	for _, m := range []session.Meta{a, b} {
		if _, code, err := trashSession(m, false); err != nil {
			t.Fatalf("trash %s: %s %v", m.Name, code, err)
		}
	}
	if mustRead(t, cA) != "A's history" || mustRead(t, cB) != "B's history" {
		t.Fatal("deleting A and B took files C depends on")
	}

	// The last link goes to the trash too, and comes back whole.
	arch, _, err := trashSession(c, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restoreCleanupArchive(arch); err != nil {
		t.Fatal(err)
	}
	if mustRead(t, cA) != "A's history" || mustRead(t, cB) != "B's history" {
		t.Fatal("restoring C did not bring back its copied chain")
	}
}

func TestPurgeDropsTheHandoverPayload(t *testing.T) {
	home := handoverEnv(t)
	m := shellSession(t, home, "tgt0003")
	writeHandover(t, m.Name, "srca001", "x")
	arch, _, err := trashSession(m, false)
	if err != nil {
		t.Fatal(err)
	}
	man, payloads, err := readCleanupArchive(arch)
	if err != nil {
		t.Fatal(err)
	}
	if len(man.Sessions) != 1 || len(man.Sessions[0].JSONLPaths) != 1 || len(payloads) != 1 {
		t.Fatalf("the archive should hold exactly the handover file: %+v, %d payloads", man.Sessions, len(payloads))
	}
	if _, err := purgeCleanupArchive(arch); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cleanupStoreDir(), arch+".tar.gz")); err == nil {
		t.Fatal("the payload outlived the purge")
	}
	if _, err := restoreCleanupArchive(arch); err == nil {
		t.Fatal("a purged archive restored")
	}
}

func TestTrashWithoutHandoverFilesIsUnchanged(t *testing.T) {
	home := handoverEnv(t)
	m := shellSession(t, home, "tgt0004")
	probe := filepath.Join(paths.SessionWorkDir(m.Name), "probe.txt")
	if err := os.MkdirAll(filepath.Dir(probe), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(probe, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	arch, _, err := trashSession(m, false)
	if err != nil {
		t.Fatal(err)
	}
	man, payloads, err := readCleanupArchive(arch)
	if err != nil {
		t.Fatal(err)
	}
	if len(man.Sessions[0].JSONLPaths) != 0 || len(payloads) != 0 {
		t.Fatalf("a session with no handover file gained a payload: %+v", man.Sessions[0])
	}
	if _, err := os.Stat(paths.SessionWorkDir(m.Name)); err == nil {
		t.Fatal("the work directory should still be removed")
	}
}

func TestTrashRefusesAHandoverFileTooBigToRestore(t *testing.T) {
	home := handoverEnv(t)
	m := shellSession(t, home, "tgt0005")
	p := writeHandover(t, m.Name, "srca001", "")
	write := func(n int) {
		if err := os.WriteFile(p, make([]byte, n), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	write(maxCleanupEntryBytes + 1)
	if _, code, err := trashSession(m, false); err == nil || code != sessionx.TrashErrArchive {
		t.Fatalf("oversized handover file: code=%q err=%v, want an archive error", code, err)
	}
	if _, ok := session.ReadMeta(m.Name); !ok {
		t.Fatal("the meta was removed although nothing reached the trash")
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("the original handover file was removed: %v", err)
	}
	if len(listCleanupArchives()) != 0 {
		t.Fatal("a refused delete left an archive")
	}

	write(maxCleanupEntryBytes)
	arch, _, err := trashSession(m, false)
	if err != nil {
		t.Fatalf("a file exactly at the limit must be accepted: %v", err)
	}
	if _, err := restoreCleanupArchive(arch); err != nil {
		t.Fatalf("an accepted archive must restore: %v", err)
	}
	if fi, err := os.Stat(p); err != nil || fi.Size() != maxCleanupEntryBytes {
		t.Fatalf("restored file: %v %v", fi, err)
	}
}

// The trash is the delete path; the per-name caches must be gone after it. Pins the
// ForgetRuntime call in trashSession, which the sessionx tests cannot reach.
func TestTrashSessionForgetsSessionCaches(t *testing.T) {
	home := handoverEnv(t)
	m := shellSession(t, home, "tgtcache1")
	sessionx.SeedSessionCachesForTest(m.Name)
	t.Cleanup(func() { sessionx.ForgetRuntime(m) })
	if _, _, err := trashSession(m, false); err != nil {
		t.Fatal(err)
	}
	if sessionx.SessionCachesHeldForTest(m.Name) {
		t.Fatal("the file / spend caches survived the trash")
	}
}
