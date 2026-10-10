package secrets

import (
	"bytes"
	"encoding/hex"
	"os"
	"testing"

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
