package secrets

import (
	"bytes"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/testguard"
)

func TestMain(m *testing.M) {
	os.Exit(testguard.Run(m, func() {
		os.Unsetenv("AF_SECRET_KEY")
		os.Unsetenv("AF_SECRET_KEY_NEXT")
	}))
}

// testKey is a key built at run time, so no key-shaped literal sits in the source.
func testKey(b byte) string { return hex.EncodeToString(bytes.Repeat([]byte{b}, 32)) }

func keyEnv(t *testing.T, derived, next string) {
	t.Helper()
	t.Setenv("AF_SECRET_KEY", derived)
	t.Setenv("AF_SECRET_KEY_NEXT", next)
}

// seedStore writes a store sealed with AF_SECRET_KEY alone, as every store is today.
func seedStore(t *testing.T, derived string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	keyEnv(t, derived, "")
	if err := Update(func(s *Data) error {
		s.Git["git.example.com"] = GitEntry{User: "u", Token: "af-test-fixture-token"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func opensWith(t *testing.T, key string) bool {
	t.Helper()
	ct, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	k, _ := hex.DecodeString(key)
	_, err = aesOpen(k, ct)
	return err == nil
}

func storedToken(t *testing.T) string {
	t.Helper()
	s, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	return s.Git["git.example.com"].Token
}

func TestMigrateKeyReSealsUnderNext(t *testing.T) {
	derived, next := testKey(1), testKey(2)
	seedStore(t, derived)
	keyEnv(t, derived, next)

	if got := MigrateKey(); got != KeyStateMigrated {
		t.Fatalf("MigrateKey = %q, want %q", got, KeyStateMigrated)
	}
	if !opensWith(t, next) || opensWith(t, derived) {
		t.Fatal("after the migration the store must open with the home's key and not with the derived one")
	}
	if got := storedToken(t); got != "af-test-fixture-token" {
		t.Fatalf("token after migration = %q", got)
	}
	if got := MigrateKey(); got != KeyStateCurrent {
		t.Fatalf("second MigrateKey = %q, want %q", got, KeyStateCurrent)
	}
}

// The git cred helper and the builtin MCP servers read the store in their own processes,
// possibly before the Agent's boot migration: they must open it under either key, and any
// save they make seals it under the home's key.
func TestLoadOpensWithEitherKeyAndSavesUnderNext(t *testing.T) {
	derived, next := testKey(1), testKey(2)
	seedStore(t, derived)
	keyEnv(t, derived, next)
	if got := storedToken(t); got != "af-test-fixture-token" {
		t.Fatalf("token read through the fallback = %q", got)
	}
	if err := Update(func(s *Data) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if !opensWith(t, next) {
		t.Fatal("a save during the migration did not seal under the home's key")
	}
}

func TestMigrateKeyNeverRewritesWhatItCannotOpen(t *testing.T) {
	seedStore(t, testKey(3))
	before, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	keyEnv(t, testKey(1), testKey(2))
	if got := MigrateKey(); got != KeyStateUnreadable {
		t.Fatalf("MigrateKey = %q, want %q", got, KeyStateUnreadable)
	}
	after, err := os.ReadFile(Path())
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("an unreadable store was rewritten (err %v)", err)
	}
}

func TestMigrateKeyStates(t *testing.T) {
	t.Run("no store", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		keyEnv(t, testKey(1), testKey(2))
		if got := MigrateKey(); got != KeyStateNone {
			t.Fatalf("MigrateKey = %q, want %q", got, KeyStateNone)
		}
	})
	t.Run("no next key", func(t *testing.T) {
		seedStore(t, testKey(1))
		if got := MigrateKey(); got != KeyStateCurrent || !opensWith(t, testKey(1)) {
			t.Fatalf("MigrateKey = %q, want %q and the store untouched", got, KeyStateCurrent)
		}
	})
	t.Run("malformed next key is ignored", func(t *testing.T) {
		seedStore(t, testKey(1))
		keyEnv(t, testKey(1), "not-hex")
		if got := MigrateKey(); got != KeyStateCurrent || !opensWith(t, testKey(1)) {
			t.Fatalf("MigrateKey = %q, want %q and the store still under AF_SECRET_KEY", got, KeyStateCurrent)
		}
	})
	t.Run("next key without AF_SECRET_KEY is ignored", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		keyEnv(t, "", testKey(2))
		if agentSecretKey() != nil {
			t.Fatal("AF_SECRET_KEY_NEXT alone must not seal the store")
		}
	})
}

func storeBytes(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A re-seal whose write fails or falls short must leave the old store in place and say so.
func TestMigrateKeyKeepsStoreWhenTheWriteFails(t *testing.T) {
	failWrite := func(f func(*os.File, []byte) (int, error)) func() {
		prev := writeTemp
		writeTemp = f
		return func() { writeTemp = prev }
	}
	for name, arm := range map[string]func() func(){
		"error": func() func() {
			return failWrite(func(*os.File, []byte) (int, error) { return 0, errors.New("file too large") })
		},
		"short": func() func() { return failWrite(func(f *os.File, b []byte) (int, error) { return f.Write(b[:1]) }) },
		"fsync": func() func() {
			prev := syncTemp
			syncTemp = func(*os.File) error { return errors.New("input/output error") }
			return func() { syncTemp = prev }
		},
	} {
		t.Run(name, func(t *testing.T) {
			derived := testKey(1)
			seedStore(t, derived)
			before := storeBytes(t)
			keyEnv(t, derived, testKey(2))
			defer arm()()
			if got := MigrateKey(); got != KeyStateDerived {
				t.Fatalf("MigrateKey = %q, want %q", got, KeyStateDerived)
			}
			if !bytes.Equal(storeBytes(t), before) {
				t.Fatal("a failed re-seal replaced the store")
			}
			left, _ := filepath.Glob(filepath.Join(filepath.Dir(Path()), ".secrets-*"))
			if len(left) != 0 {
				t.Fatalf("temp files left behind: %v", left)
			}
		})
	}
}

// Load on a store that does not open hands back an empty snapshot with an error; saving that
// snapshot must not write it over the store.
func TestSaveRefusesToOverwriteAnUnreadableStore(t *testing.T) {
	seedStore(t, testKey(3))
	before := storeBytes(t)
	for name, next := range map[string]string{"with next": testKey(2), "without next": ""} {
		t.Run(name, func(t *testing.T) {
			keyEnv(t, testKey(1), next)
			s, err := Load()
			if err == nil {
				t.Fatal("Load opened a store sealed under another key")
			}
			s.Git["git.example.com"] = GitEntry{User: "u", Token: "af-test-fixture-new"}
			if err := s.Save(); err == nil {
				t.Fatal("Save wrote over a store it could not open")
			}
			if !bytes.Equal(storeBytes(t), before) {
				t.Fatal("the unreadable store was overwritten")
			}
		})
	}
}

// The git cred helper is another process; the re-seal must wait for its flock, or a write it
// makes under the old key could land between our read and our rename and be lost.
func TestMigrateKeyWaitsForTheFileLock(t *testing.T) {
	derived := testKey(1)
	seedStore(t, derived)
	keyEnv(t, derived, testKey(2))
	f, err := os.OpenFile(Path()+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	done := make(chan string, 1)
	go func() { done <- MigrateKey() }()
	select {
	case got := <-done:
		t.Fatalf("MigrateKey returned %q while another process held the store lock", got)
	case <-time.After(200 * time.Millisecond):
	}
	if !opensWith(t, derived) {
		t.Fatal("the store was rewritten while another process held the lock")
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got != KeyStateMigrated {
			t.Fatalf("MigrateKey after the lock = %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("MigrateKey did not finish once the lock was released")
	}
}
