package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/installx"
)

// fakeKiroHome sets up HOME with a stub kiro-cli reporting ver from `--version`, plus a
// versions.json pinning kiro=pin.
func fakeKiroHome(t *testing.T, ver, pin string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	binDir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nexit 0\n"
	if ver != "" {
		script = "#!/bin/sh\ncase \"$1\" in --version) echo \"kiro-cli " + ver + "\";; esac\nexit 0\n"
	}
	if err := os.WriteFile(filepath.Join(binDir, "kiro-cli"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	pins := filepath.Join(home, "versions.json")
	if err := os.WriteFile(pins, []byte(`{"kiro":"`+pin+`","kiro_sha256":"deadbeef"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	orig := installx.BuildPinsPath
	installx.BuildPinsPath = pins
	t.Cleanup(func() { installx.BuildPinsPath = orig })
}

// TestKiroInstallGETReportsPinDrift: the connection card's update affordance is driven
// by this payload. A home copy older than the versions.json pin must report
// updateAvailable=true with both versions, so the user can SEE that an update exists and
// press the button when a multi-minute download suits them (docs/log/43 §11).
func TestKiroInstallGETReportsPinDrift(t *testing.T) {
	fakeKiroHome(t, "2.14.1", "2.14.2")
	kiroInstaller = kiroInstall{}

	rec := httptest.NewRecorder()
	handleKiroInstall(rec, httptest.NewRequest(http.MethodGet, "/connections/kiro/install", nil))
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if got["installed"] != true || got["updateAvailable"] != true ||
		got["version"] != "2.14.1" || got["pin"] != "2.14.2" {
		t.Fatalf("GET payload = %v, want installed/updateAvailable with 2.14.1 → 2.14.2", got)
	}

	// Same version as the pin → no nagging.
	fakeKiroHome(t, "2.14.2", "2.14.2")
	rec = httptest.NewRecorder()
	handleKiroInstall(rec, httptest.NewRequest(http.MethodGet, "/connections/kiro/install", nil))
	got = nil
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["updateAvailable"] != false || got["installed"] != true {
		t.Fatalf("GET payload = %v, want installed with no update", got)
	}
}
