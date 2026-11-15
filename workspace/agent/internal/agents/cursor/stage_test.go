package cursor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, p, body string, perm os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), perm); err != nil {
		t.Fatal(err)
	}
}

// The install replaces the tokens and the account caches, and leaves the member's settings.
func TestInstallLoginReplacesTokensAndAccountCaches(t *testing.T) {
	stage, home := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(stage, ".config/cursor/auth.json"), `{"accessToken":"new"}`, 0o600)
	writeFile(t, filepath.Join(stage, ".cursor/cli-config.json"), `{"authInfo":{"email":"new@example.com"},"editor":{"vimMode":false}}`, 0o644)
	writeFile(t, filepath.Join(home, ".config/cursor/auth.json"), `{"accessToken":"old"}`, 0o600)
	writeFile(t, filepath.Join(home, ".cursor/cli-config.json"), `{"authInfo":{"email":"old@example.com"},"privacyCache":{"old":true},"editor":{"vimMode":true},"channel":"static"}`, 0o644)

	if err := installLogin(stage, home); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(home, ".config/cursor/auth.json")); string(b) != `{"accessToken":"new"}` {
		t.Fatalf("auth.json = %s", b)
	}
	var cfg map[string]any
	b, _ := os.ReadFile(filepath.Join(home, ".cursor/cli-config.json"))
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["authInfo"].(map[string]any)["email"] != "new@example.com" {
		t.Fatalf("authInfo not replaced: %v", cfg["authInfo"])
	}
	if _, ok := cfg["privacyCache"]; ok {
		t.Fatal("the old account's privacyCache survived")
	}
	if cfg["editor"].(map[string]any)["vimMode"] != true || cfg["channel"] != "static" {
		t.Fatalf("member settings changed: %v", cfg)
	}
}

func TestInstallLoginWithoutStagedTokens(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".config/cursor/auth.json"), `{"accessToken":"old"}`, 0o600)
	if err := installLogin(t.TempDir(), home); err == nil {
		t.Fatal("installed a sign-in that wrote no tokens")
	}
	if b, _ := os.ReadFile(filepath.Join(home, ".config/cursor/auth.json")); string(b) != `{"accessToken":"old"}` {
		t.Fatalf("live tokens touched: %s", b)
	}
}
