package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecordTerminalPersistsOutput(t *testing.T) {
	t.Setenv("AF_TERMINAL_HISTORY_DIR", t.TempDir())
	want := []byte("\x1b[32mhello\x1b[0m\r\n")
	if err := recordTerminal("demo", bytes.NewReader(want)); err != nil {
		t.Fatal(err)
	}
	got, ok := readTerminalHistory("demo")
	if !ok || !bytes.Equal(got, want) {
		t.Fatalf("history = %q, %v; want %q, true", got, ok, want)
	}
	st, err := os.Stat(terminalHistoryPath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("history mode = %v; want 0600", st.Mode().Perm())
	}
}

func TestRecordTerminalKeepsBoundedTail(t *testing.T) {
	t.Setenv("AF_TERMINAL_HISTORY_DIR", t.TempDir())
	src := bytes.Repeat([]byte("0123456789abcdef"), int(terminalHistoryMaxBytes/(16))+32768)
	if err := recordTerminal("demo", bytes.NewReader(src)); err != nil {
		t.Fatal(err)
	}
	got, ok := readTerminalHistory("demo")
	if !ok {
		t.Fatal("history missing")
	}
	if int64(len(got)) != terminalHistoryMaxBytes {
		t.Fatalf("history size = %d; want %d", len(got), terminalHistoryMaxBytes)
	}
	if !bytes.Equal(got, src[len(src)-len(got):]) {
		t.Fatal("history is not the newest tail")
	}
}

func TestReadTerminalHistoryRejectsInvalidName(t *testing.T) {
	t.Setenv("AF_TERMINAL_HISTORY_DIR", t.TempDir())
	if _, ok := readTerminalHistory("../escape"); ok {
		t.Fatal("invalid session name was accepted")
	}
}

func TestCleanupTerminalHistoryHonorsRetention(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_TERMINAL_HISTORY_DIR", "")
	t.Setenv("AF_TERMINAL_HISTORY_RETENTION_DAYS", "7")
	dir := persistentTerminalHistoryDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(dir, "old.ansi")
	newPath := filepath.Join(dir, "new.ansi")
	if err := os.WriteFile(oldPath, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newPath, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(oldPath, old, old); err != nil {
		t.Fatal(err)
	}
	cleanupTerminalHistory()
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("expired history still exists: %v", err)
	}
	if _, err := os.Stat(newPath); err != nil {
		t.Fatalf("fresh history was removed: %v", err)
	}
}

func TestCleanupTerminalHistoryDisabledDeletesPersistentStore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_TERMINAL_HISTORY_DIR", "")
	t.Setenv("AF_TERMINAL_HISTORY_RETENTION_DAYS", "0")
	dir := persistentTerminalHistoryDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "demo.ansi"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	cleanupTerminalHistory()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("disabled persistent store still exists: %v", err)
	}
}
