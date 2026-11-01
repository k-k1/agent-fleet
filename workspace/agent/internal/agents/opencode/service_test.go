package opencode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureServiceDisabledV2(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	useMajor(t, 2)
	if err := EnsureServiceDisabled(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(ServiceConfigPath())
	if err != nil || !strings.Contains(string(b), `"disabled": true`) {
		t.Fatalf("service.json = %q, %v", b, err)
	}
}

func TestEnsureServiceDisabledKeepsMembersChoices(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	useMajor(t, 2)
	// Another key is kept...
	if err := os.MkdirAll(strings.TrimSuffix(ServiceConfigPath(), "/service.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ServiceConfigPath(), []byte(`{"port":49400}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := EnsureServiceDisabled(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(ServiceConfigPath())
	if !strings.Contains(string(b), `"port": 49400`) || !strings.Contains(string(b), `"disabled": true`) {
		t.Fatalf("merge lost a key: %s", b)
	}
	// ...and an explicit disabled:false is the member's, not ours to flip.
	if err := os.WriteFile(ServiceConfigPath(), []byte(`{"disabled":false}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := EnsureServiceDisabled(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(ServiceConfigPath()); !strings.Contains(string(b), "false") {
		t.Fatalf("an explicit disabled:false was overwritten: %s", b)
	}
	// Not JSON: untouched, reported.
	if err := os.WriteFile(ServiceConfigPath(), []byte(`{oops`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := EnsureServiceDisabled(); err != ErrUnreadableConfig {
		t.Fatalf("err = %v, want ErrUnreadableConfig", err)
	}
	if b, _ := os.ReadFile(ServiceConfigPath()); string(b) != `{oops` {
		t.Fatalf("unreadable file was rewritten: %s", b)
	}
}

// `null` unmarshals into a nil map without an error; writing to it panicked the Agent at boot.
func TestEnsureServiceDisabledNullFileIsUnreadableNotAPanic(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	useMajor(t, 2)
	if err := os.MkdirAll(filepath.Dir(ServiceConfigPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"null", "[]", "3"} {
		if err := os.WriteFile(ServiceConfigPath(), []byte(raw), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := EnsureServiceDisabled(); err != ErrUnreadableConfig {
			t.Fatalf("%s: err = %v, want ErrUnreadableConfig", raw, err)
		}
		if b, _ := os.ReadFile(ServiceConfigPath()); string(b) != raw {
			t.Fatalf("%s: file rewritten to %q", raw, b)
		}
	}
}
