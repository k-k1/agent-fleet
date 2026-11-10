package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/muse"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/installx"
)

// fakeMuseHome sets up HOME with a stub `muse` reporting ver from `--version`, plus a
// versions.json pinning muse=pin. ver=="" makes `--version` print nothing.
//
// PATH is rebuilt without any other muse: the installer falls back to exec.LookPath("muse")
// on purpose, so a developer workspace that has muse installed would otherwise answer
// "already installed" for reasons unrelated to the code under test (measured: three tests
// went red on exactly that while CI, which has no muse, stayed green).
func fakeMuseHome(t *testing.T, ver, pin string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	binDir := filepath.Join(home, ".local", "bin")
	keep := []string{binDir}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" || fileExecutable(filepath.Join(dir, "muse")) {
			continue
		}
		keep = append(keep, dir)
	}
	t.Setenv("PATH", strings.Join(keep, string(filepath.ListSeparator)))
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nexit 0\n"
	if ver != "" {
		script = "#!/bin/sh\ncase \"$1\" in --version) echo \"Muse Code " +
			strings.SplitN(ver, "-", 2)[0] + " (" + ver + ")\";; esac\nexit 0\n"
	}
	if err := os.WriteFile(filepath.Join(binDir, "muse"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	pins := filepath.Join(home, "versions.json")
	if err := os.WriteFile(pins, []byte(`{"muse":"`+pin+`","muse_sha256":"deadbeef"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	orig := installx.BuildPinsPath
	installx.BuildPinsPath = pins
	t.Cleanup(func() { installx.BuildPinsPath = orig })
}

// The HTTP route the connection card polls. GET must report the version facts the card turns
// into its "update available" affordance — and must NOT claim an update when the versions are
// merely unknown, or the card would offer a 299 MiB download that changes nothing.
func TestMuseInstallRouteReportsVersionFacts(t *testing.T) {
	for _, tc := range []struct {
		name            string
		ver, pin        string
		wantInstalled   bool
		wantUpdateAvail bool
	}{
		{"at the pin: installed, no update", "1.3.0-R3401.1", "1.3.0-R3401.1", true, false},
		{"behind the pin: update available", "1.2.9-R3300.4", "1.3.0-R3401.1", true, true},
		{"unreadable version: installed, but no update claimed", "", "1.3.0-R3401.1", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeMuseHome(t, tc.ver, tc.pin)
			rec := httptest.NewRecorder()
			handleMuseInstall(rec, httptest.NewRequest(http.MethodGet, "/connections/muse/install", nil))
			var body struct {
				State           string `json:"state"`
				Installed       bool   `json:"installed"`
				Version         string `json:"version"`
				Pin             string `json:"pin"`
				UpdateAvailable bool   `json:"updateAvailable"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode: %v (%s)", err, rec.Body.String())
			}
			if body.State != "idle" {
				t.Errorf("state = %q", body.State)
			}
			if body.Installed != tc.wantInstalled || body.UpdateAvailable != tc.wantUpdateAvail {
				t.Errorf("installed=%v updateAvailable=%v, want %v/%v (version=%q pin=%q)",
					body.Installed, body.UpdateAvailable, tc.wantInstalled, tc.wantUpdateAvail,
					body.Version, body.Pin)
			}
			if body.Pin != tc.pin {
				t.Errorf("pin = %q, want %q", body.Pin, tc.pin)
			}
		})
	}
}

// A POST with the pinned version already present answers "done" without starting anything —
// the card's Install button is idempotent. A present-but-STALE muse must NOT answer done: that
// is the upgrade and the shadow repair, and this route is the only thing that performs them
// (muse is managed-only, so unlike kiro there is no launch guard behind it).
func TestMuseInstallPostIsIdempotentButNotForStale(t *testing.T) {
	fakeMuseHome(t, "1.3.0-R3401.1", "1.3.0-R3401.1")
	rec := httptest.NewRecorder()
	handleMuseInstall(rec, httptest.NewRequest(http.MethodPost, "/connections/muse/install", nil))
	if !strings.Contains(rec.Body.String(), `"done"`) {
		t.Fatalf("POST at the pin should be a no-op done: %s", rec.Body.String())
	}
	if museInstaller.state == "installing" {
		t.Error("POST at the pin started an install")
	}
	if !muse.InstallCurrent() {
		t.Error("InstallCurrent() = false at the pin")
	}

	fakeMuseHome(t, "1.2.9-R3300.4", "1.3.0-R3401.1")
	if muse.InstallCurrent() {
		t.Error("InstallCurrent() = true for a stale install; the route would answer done " +
			"and the member would never get the pinned version")
	}
}
