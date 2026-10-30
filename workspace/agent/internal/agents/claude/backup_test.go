package claude

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// backupFixture is a claude session with a meta, a finished turn on record and one log in its
// own project directory.
func backupFixture(t *testing.T, name string) (cfg string, m session.Meta, sid, logPath string) {
	t.Helper()
	cfg = isolateSlot(t)
	m = session.Meta{Kind: session.KindClaude, Name: name, Dir: t.TempDir(), CreatedAt: time.Now().Format(time.RFC3339)}
	session.WriteMeta(m)
	sid = session.UUID(m.Dir, m.Name)
	logPath = writeSlotJSONL(t, cfg, projectKey(m.Dir), sid)
	NoteTalked(sid)
	return
}

func mustBackup(t *testing.T, m session.Meta) {
	t.Helper()
	if err := BackupTranscript(m); err != nil {
		t.Fatal(err)
	}
}

// The acceptance case: the log is gone, the backup brings the conversation back, and the
// restored file keeps the original's permission bits and mtime.
func TestBuildLaunchRestoresDeletedLogFromBackup(t *testing.T) {
	_, m, sid, logPath := backupFixture(t, "restore-me")
	if err := os.Chmod(logPath, 0o640); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(logPath, old, old); err != nil {
		t.Fatal(err)
	}
	want, _ := os.ReadFile(logPath)
	mustBackup(t, m)
	if err := os.Remove(logPath); err != nil {
		t.Fatal(err)
	}

	plan, err := launchOf(t, m)
	if err != nil {
		t.Fatalf("launch after the log was deleted: %v", err)
	}
	if !strings.Contains(plan.Program, "--resume") {
		t.Fatalf("program = %q, want a resume of the restored log", plan.Program)
	}
	got, err := os.ReadFile(logPath)
	if err != nil || string(got) != string(want) {
		t.Fatalf("restored log = %q, %v; want %q", got, err, want)
	}
	fi, _ := os.Stat(logPath)
	if fi.Mode().Perm() != 0o640 || !fi.ModTime().Equal(old) {
		t.Fatalf("restored mode %v mtime %v, want 0640 and %v", fi.Mode().Perm(), fi.ModTime(), old)
	}
	if !HadConversation(sid) {
		t.Fatal("conversation marker lost")
	}
}

// The launch reports a notice for a restore that happened — and only then: a resume of a log
// that was never lost, and a second resume after the restore, say nothing.
func TestBuildLaunchReportsRestoreNoticeOnce(t *testing.T) {
	_, m, _, logPath := backupFixture(t, "restore-notice")
	plain, err := launchOf(t, m)
	if err != nil || len(plain.Notices) != 0 {
		t.Fatalf("untouched log: notices %v, err %v; want none", plain.Notices, err)
	}
	mustBackup(t, m)
	if err := os.Remove(logPath); err != nil {
		t.Fatal(err)
	}
	restored, err := launchOf(t, m)
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.Notices) != 1 || restored.Notices[0] != agents.NoticeTranscriptRestored {
		t.Fatalf("notices after a restore = %v, want [%s]", restored.Notices, agents.NoticeTranscriptRestored)
	}
	again, err := launchOf(t, m)
	if err != nil || len(again.Notices) != 0 {
		t.Fatalf("second resume: notices %v, err %v; want none", again.Notices, err)
	}
}

// Without a backup the refusal of the previous change stands.
func TestBuildLaunchWithoutBackupStillRefuses(t *testing.T) {
	_, m, _, logPath := backupFixture(t, "no-backup")
	if err := os.Remove(logPath); err != nil {
		t.Fatal(err)
	}
	if _, err := launchOf(t, m); !errors.Is(err, ErrTranscriptLost) {
		t.Fatalf("err = %v, want ErrTranscriptLost", err)
	}
}

// A live log that is newer than the backup is never replaced by it.
func TestRestoreNeverOverwritesNewerLiveLog(t *testing.T) {
	_, m, sid, logPath := backupFixture(t, "live-newer")
	mustBackup(t, m)
	newer := []byte(`{"type":"user"}` + "\n" + `{"type":"assistant"}` + "\n")
	if err := os.WriteFile(logPath, newer, 0o600); err != nil {
		t.Fatal(err)
	}
	restored, err := RestoreTranscript(sid, m.Dir)
	if err != nil || restored {
		t.Fatalf("restored = %v, %v; want no restore while a live log exists", restored, err)
	}
	if got, _ := os.ReadFile(logPath); string(got) != string(newer) {
		t.Fatalf("live log was overwritten: %q", got)
	}
	// Positive control: the same call does restore once the log is gone.
	if err := os.Remove(logPath); err != nil {
		t.Fatal(err)
	}
	if restored, err := RestoreTranscript(sid, m.Dir); err != nil || !restored {
		t.Fatalf("control: restored = %v, %v", restored, err)
	}
}

// The backup is only ever replaced by a newer live log: an older or stub one leaves it alone,
// and an unchanged one is not copied again.
func TestBackupKeepsNewestAndSkipsUnchanged(t *testing.T) {
	_, m, sid, logPath := backupFixture(t, "bk-newest")
	mustBackup(t, m)
	slotDir, _ := backupSlotDir(sid)
	bp, _, err := backupFile(slotDir, sid)
	if err != nil || bp == "" {
		t.Fatalf("backup file: %q %v", bp, err)
	}
	first, _ := os.Stat(bp)

	mustBackup(t, m)
	if again, _ := os.Stat(bp); !os.SameFile(first, again) {
		t.Fatal("an unchanged log was copied again")
	}

	// A stub (no user/assistant line) is not a conversation: the backup must survive it.
	if err := os.WriteFile(logPath, []byte(`{"type":"bridge-session"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustBackup(t, m)
	if got, _ := os.ReadFile(bp); !strings.Contains(string(got), `"type":"user"`) {
		t.Fatalf("a stub replaced the backup: %q", got)
	}

	// A resumable log with an OLDER mtime than the backup does not replace it either.
	grown := []byte(`{"type":"user"}` + "\n" + `{"type":"assistant","x":1}` + "\n")
	if err := os.WriteFile(logPath, grown, 0o600); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(logPath, past, past); err != nil {
		t.Fatal(err)
	}
	mustBackup(t, m)
	if got, _ := os.ReadFile(bp); string(got) == string(grown) {
		t.Fatal("an older live log replaced a newer backup")
	}

	// A newer one does.
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(logPath, future, future); err != nil {
		t.Fatal(err)
	}
	mustBackup(t, m)
	if got, _ := os.ReadFile(bp); string(got) != string(grown) {
		t.Fatalf("a newer live log did not update the backup: %q", got)
	}
}

// The backup lives under the Agent's data directory, never where claude's sweep looks, and
// holds at most one log per slot even after the conversation drifted to a new id.
func TestBackupLocationAndOneCopyPerSlot(t *testing.T) {
	cfg, m, sid, _ := backupFixture(t, "bk-one")
	mustBackup(t, m)
	drifted := "47000000-0000-4000-8000-0000000drift"
	writeSlotJSONL(t, cfg, projectKey(m.Dir), drifted)
	sids.Write(sid, drifted)
	mustBackup(t, m)

	slotDir, _ := backupSlotDir(sid)
	if strings.HasPrefix(slotDir, cfg) {
		t.Fatalf("backup %s is inside CLAUDE_CONFIG_DIR %s", slotDir, cfg)
	}
	var files []string
	_ = filepath.WalkDir(slotDir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			files = append(files, p)
		}
		return nil
	})
	if len(files) != 1 || filepath.Base(files[0]) != drifted+".jsonl" {
		t.Fatalf("backup files = %v, want only the drifted id's log", files)
	}
}

// StartFresh must not leave the backup to be restored by the launch it exists for; the backup is
// kept aside and goes with the session.
func TestStartFreshSetsAsideBackupAndRemoveClearsIt(t *testing.T) {
	_, m, sid, logPath := backupFixture(t, "bk-fresh")
	mustBackup(t, m)
	if err := os.Remove(logPath); err != nil {
		t.Fatal(err)
	}
	if err := StartFresh(sid); err != nil {
		t.Fatal(err)
	}
	plan, err := launchOf(t, m)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plan.Program, "--resume") {
		t.Fatalf("program = %q: the fresh start resumed the backup", plan.Program)
	}
	if _, err := os.Stat(logPath); err == nil {
		t.Fatal("the backup was restored after a fresh start")
	}
	slotDir, _ := backupSlotDir(sid)
	kept, _ := filepath.Glob(slotDir + ".superseded.*")
	if len(kept) != 1 {
		t.Fatalf("superseded backups = %v, want one kept", kept)
	}
	if err := RemoveTranscriptBackup(sid); err != nil {
		t.Fatal(err)
	}
	if left, _ := filepath.Glob(slotDir + "*"); len(left) != 0 {
		t.Fatalf("backup left after removal: %v", left)
	}
}

// A fresh start replaces the conversation the restore notes talk about, so they go with it;
// a StartFresh that finds the log in place changes nothing and keeps them.
func TestStartFreshClearsRestoreNotesOnlyWhenItResets(t *testing.T) {
	_, m, sid, logPath := backupFixture(t, "bk-fresh-notes")
	mustBackup(t, m)
	if err := os.Remove(logPath); err != nil {
		t.Fatal(err)
	}
	if _, err := launchOf(t, m); err != nil { // restores
		t.Fatal(err)
	}
	if err := status.AddRestoreNote(sid); err != nil {
		t.Fatal(err)
	}
	if started, err := StartFreshIfLost(sid); err != nil || started { // log is back: nothing to reset
		t.Fatalf("StartFreshIfLost = %v, %v; want no reset", started, err)
	}
	if len(status.ReadRestoreNotes(sid)) != 1 {
		t.Fatalf("a stale fresh request dropped the notes: %v", status.ReadRestoreNotes(sid))
	}
	if err := StartFresh(sid); err != nil {
		t.Fatal(err)
	}
	if got := status.ReadRestoreNotes(sid); len(got) != 0 {
		t.Fatalf("notes survived a fresh start: %v", got)
	}
}

// An unreadable projects tree is an error, not "no log": restoring over a log that is merely
// hidden would fork the conversation.
func TestRestoreReturnsEnumerationError(t *testing.T) {
	cfg, m, sid, logPath := backupFixture(t, "bk-eacces")
	mustBackup(t, m)
	if err := os.Remove(logPath); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(cfg, "projects")
	if err := os.Chmod(root, 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(root, 0o700)
	if _, err := os.ReadDir(root); err == nil {
		t.Skip("directory permissions are not enforced (running as root)")
	}
	if restored, err := RestoreTranscript(sid, m.Dir); err == nil || restored {
		t.Fatalf("restored = %v, err = %v; want an error", restored, err)
	}
	if _, err := launchOf(t, m); errors.Is(err, ErrTranscriptLost) || err == nil {
		t.Fatalf("launch err = %v, want the I/O error and not transcript_lost", err)
	}
}

// A stub sitting where the log belongs is set aside, not overwritten or deleted.
func TestRestoreSetsAsideStubInTheWay(t *testing.T) {
	_, m, sid, logPath := backupFixture(t, "bk-stub")
	mustBackup(t, m)
	stub := []byte(`{"type":"bridge-session"}` + "\n")
	if err := os.WriteFile(logPath, stub, 0o600); err != nil {
		t.Fatal(err)
	}
	if restored, err := RestoreTranscript(sid, m.Dir); err != nil || !restored {
		t.Fatalf("restored = %v, %v", restored, err)
	}
	kept, _ := filepath.Glob(logPath + ".superseded.*")
	if len(kept) != 1 {
		t.Fatalf("set-aside stubs = %v, want one", kept)
	}
	if got, _ := os.ReadFile(kept[0]); string(got) != string(stub) {
		t.Fatalf("set-aside content = %q", got)
	}
}

// No meta, no backup: a delete that raced the copy must not leave one behind.
func TestBackupSkipsDeletedSession(t *testing.T) {
	_, m, sid, _ := backupFixture(t, "bk-gone")
	session.RemoveMeta(m.Name)
	mustBackup(t, m)
	slotDir, _ := backupSlotDir(sid)
	if _, err := os.Stat(slotDir); err == nil {
		t.Fatal("a backup was made for a session with no meta")
	}
}

// An older copy of the same conversation in ANOTHER project directory must not replace a newer
// backup once the original is gone.
func TestBackupOlderLogInOtherProjectDoesNotReplaceNewerBackup(t *testing.T) {
	cfg, m, sid, logPath := backupFixture(t, "bk-other-proj")
	mustBackup(t, m)
	slotDir, _ := backupSlotDir(sid)
	bp, _, _ := backupFile(slotDir, sid)
	want, _ := os.ReadFile(bp)
	if err := os.Remove(logPath); err != nil {
		t.Fatal(err)
	}
	other := writeSlotJSONL(t, cfg, "other-project", sid)
	if err := os.WriteFile(other, []byte(`{"type":"user","old":1}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-24 * time.Hour)
	if err := os.Chtimes(other, past, past); err != nil {
		t.Fatal(err)
	}
	mustBackup(t, m)
	bp2, _, _ := backupFile(slotDir, sid)
	if got, _ := os.ReadFile(bp2); string(got) != string(want) {
		t.Fatalf("backup replaced by the older log: %q", got)
	}
}

// A log with a half-written last line is not copied over a complete backup.
func TestBackupRejectsTornLog(t *testing.T) {
	_, m, sid, logPath := backupFixture(t, "bk-torn")
	mustBackup(t, m)
	slotDir, _ := backupSlotDir(sid)
	bp, _, _ := backupFile(slotDir, sid)
	want, _ := os.ReadFile(bp)
	torn := `{"type":"user"}` + "\n" + `{"type":"assistant","message":"par`
	if err := os.WriteFile(logPath, []byte(torn), 0o600); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(logPath, future, future); err != nil {
		t.Fatal(err)
	}
	mustBackup(t, m)
	if got, _ := os.ReadFile(bp); string(got) != string(want) {
		t.Fatalf("a torn log replaced the backup: %q", got)
	}
	// Positive control: the same log completed is taken.
	if err := os.WriteFile(logPath, []byte(torn+`t"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(logPath, future, future); err != nil {
		t.Fatal(err)
	}
	mustBackup(t, m)
	if got, _ := os.ReadFile(bp); !strings.Contains(string(got), `part"}`) {
		t.Fatalf("a complete newer log was not taken: %q", got)
	}
}

// StartFresh and RestoreTranscript exclude each other, and a fresh start asked for after a
// restore made the log resumable again does nothing.
func TestStartFreshIfLostRechecksAndLocks(t *testing.T) {
	_, m, sid, logPath := backupFixture(t, "bk-lock")
	mustBackup(t, m)
	if err := os.Remove(logPath); err != nil {
		t.Fatal(err)
	}
	if restored, err := RestoreTranscript(sid, m.Dir); err != nil || !restored {
		t.Fatalf("restore: %v %v", restored, err)
	}
	if started, err := StartFreshIfLost(sid); err != nil || started {
		t.Fatalf("started = %v, %v; want no fresh start of a resumable conversation", started, err)
	}
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("the restored log was set aside: %v", err)
	}

	unlock := lockBackup(sid)
	done := make(chan struct{})
	go func() { _ = StartFresh(sid); close(done) }()
	select {
	case <-done:
		t.Fatal("StartFresh ran while the slot lock was held")
	case <-time.After(150 * time.Millisecond):
	}
	unlock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("StartFresh did not finish after the lock was released")
	}
}

// A conversation that appears at the destination is never set aside as if it were a stub.
func TestSetAsideStubRefusesConversation(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "x.jsonl")
	body := `{"type":"user"}` + "\n"
	if err := os.WriteFile(dest, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := setAsideStub(dest); err == nil {
		t.Fatal("a conversation was set aside")
	}
	if got, _ := os.ReadFile(dest); string(got) != body {
		t.Fatalf("conversation touched: %q", got)
	}
}

// A newer conversation outside the cwd's project is found even though the hint names an older
// complete log; a stub at the hint does not hide a good log elsewhere.
func TestBackupFindsConversationOutsideHint(t *testing.T) {
	cfg, m, sid, logPath := backupFixture(t, "bk-hint")
	past := time.Now().Add(-24 * time.Hour)
	if err := os.Chtimes(logPath, past, past); err != nil {
		t.Fatal(err)
	}
	other := writeSlotJSONL(t, cfg, "elsewhere", sid)
	if err := os.WriteFile(other, []byte(`{"type":"user"}`+"\n"+`{"type":"assistant","new":1}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lastSweep.mu.Lock()
	delete(lastSweep.m, sid)
	lastSweep.mu.Unlock()
	mustBackup(t, m)
	slotDir, _ := backupSlotDir(sid)
	bp, _, _ := backupFile(slotDir, sid)
	if got, _ := os.ReadFile(bp); !strings.Contains(string(got), `"new":1`) {
		t.Fatalf("the newer log outside the hint was not backed up: %q", got)
	}

	// Stub at the hint, good log elsewhere, no backup yet.
	cfg2, m2, sid2, log2 := backupFixture(t, "bk-hint-stub")
	if err := os.WriteFile(log2, []byte(`{"type":"bridge-session"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeSlotJSONL(t, cfg2, "elsewhere", sid2)
	mustBackup(t, m2)
	slotDir2, _ := backupSlotDir(sid2)
	if bp2, _, _ := backupFile(slotDir2, sid2); bp2 == "" {
		t.Fatal("a stub at the hint blocked the first backup")
	}
}
