package codex

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexUserConfigWritersKeepDanglingLink(t *testing.T) {
	for _, tc := range []struct {
		name string
		want string
		edit func(*testing.T, string)
	}{
		{"settings", "hide_rate_limit_model_nudge = true", func(t *testing.T, _ string) {
			r := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(`{"rate_limit_model_nudge":false}`))
			w := httptest.NewRecorder()
			HandleSettingsPut(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("settings update = %d, %s", w.Code, w.Body.String())
			}
		}},
		{"memories", "memories = true", func(t *testing.T, _ string) {
			if err := SetMemoriesFlag(true); err != nil {
				t.Fatal(err)
			}
		}},
		{"trust", `trust_level = "trusted"`, func(_ *testing.T, home string) {
			ensureFolderTrusted(home)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("CODEX_HOME", filepath.Join(home, "codex"))
			path := codexConfigPath()
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			link := "../settings/config.toml"
			if err := os.Symlink(link, path); err != nil {
				t.Fatal(err)
			}
			tc.edit(t, home)
			if got, err := os.Readlink(path); err != nil || got != link {
				t.Fatalf("config link = %q, %v; want %q", got, err, link)
			}
			if b, err := os.ReadFile(filepath.Join(home, "settings", "config.toml")); err != nil || !strings.Contains(string(b), tc.want) {
				t.Fatalf("config target = %q, %v; want %q", b, err, tc.want)
			}
		})
	}
}

func TestRateLimitModelNudgeDefaultAndRoundTrip(t *testing.T) {
	original := []byte("model = \"gpt-5.6-sol\"\n\n[projects.\"/repo\"]\ntrust_level = \"trusted\"\n")
	if !rateLimitModelNudgeEnabled(original) {
		t.Fatal("absent key should default to enabled")
	}
	off := setRateLimitModelNudge(original, false)
	if rateLimitModelNudgeEnabled(off) {
		t.Fatal("disabled setting was not read back")
	}
	if !strings.Contains(string(off), "[notice]\nhide_rate_limit_model_nudge = true") ||
		!strings.Contains(string(off), "[projects.\"/repo\"]\ntrust_level = \"trusted\"") {
		t.Fatalf("unrelated config was not preserved:\n%s", off)
	}
	on := setRateLimitModelNudge(off, true)
	if !rateLimitModelNudgeEnabled(on) || strings.Count(string(on), "hide_rate_limit_model_nudge") != 1 {
		t.Fatalf("enabled setting did not round-trip cleanly:\n%s", on)
	}
}

func TestSetRateLimitModelNudgeInsideExistingNotice(t *testing.T) {
	original := []byte("[notice]\nhide_full_access_warning = true\n\n[projects.\"/repo\"]\ntrust_level = \"trusted\"\n")
	got := string(setRateLimitModelNudge(original, false))
	want := "[notice]\nhide_full_access_warning = true\n\nhide_rate_limit_model_nudge = true\n[projects.\"/repo\"]"
	if !strings.Contains(got, want) {
		t.Fatalf("notice entry inserted in wrong section:\n%s", got)
	}
}
