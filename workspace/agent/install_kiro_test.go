package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

// TestKiroAsset pins the arch → asset mapping: x86_64 gnu (Debian 12 glibc suffices),
// aarch64 musl (avoids the gnu build's glibc 2.39 floor). Only the running arch is
// asserted concretely; the others are covered by the switch compiling.
func TestKiroAsset(t *testing.T) {
	got, err := kiroAsset()
	if err != nil {
		t.Fatalf("kiroAsset: %v", err)
	}
	want := map[string]string{
		"amd64": "kirocli-x86_64-linux.zip",
		"arm64": "kirocli-aarch64-linux-musl.zip",
	}[runtime.GOARCH]
	if want != "" && got != want {
		t.Errorf("kiroAsset()=%q, want %q", got, want)
	}
}

// fakeKiroHome sets up HOME with a stub kiro-cli that reports version ver from
// `--version` (and swallows `settings …`, i.e. pinKiroSettings), plus a versions.json
// pinning kiro=pin. Returns ~/.local/bin. ver=="" makes `--version` print nothing.
func fakeKiroHome(t *testing.T, ver, pin string) string {
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
	orig := buildPinsPath
	buildPinsPath = pins
	t.Cleanup(func() { buildPinsPath = orig })
	return binDir
}

// TestInstallKiroIdempotentSkip: a kiro-cli already at the PINNED version short-circuits
// before any download, and records the marker so the next launch is exec-free. A killed
// prior install that left a broken kiro-cli would be treated as "installed" here — that
// is exactly why the real install places kiro-cli LAST via atomic rename, so this fast
// path only ever sees a fully-installed binary.
func TestInstallKiroIdempotentSkip(t *testing.T) {
	binDir := fakeKiroHome(t, "2.14.2", "2.14.2")
	if err := installKiro(false); err != nil {
		t.Fatalf("installKiro with pinned binary should skip cleanly, got %v", err)
	}
	b, err := os.ReadFile(kiroVersionMarkerPath(binDir))
	if err != nil || strings.TrimSpace(string(b)) != "2.14.2" {
		t.Fatalf("marker not recorded for the skip fast path: %q / %v", string(b), err)
	}
	if !kiroInstallCurrent() {
		t.Error("kiroInstallCurrent() = false for a binary already at the pin")
	}
}

// TestKiroCheckPinDrift is the regression this whole path exists for: the ~/.local copy
// survives image rebuilds, so when versions.json bumps the pin (2.14.1 → 2.14.2) the
// installed kiro must be reported STALE — the old presence-only check left users on the
// first version they ever installed, forever (kiro's own updater is pinned off).
func TestKiroCheckPinDrift(t *testing.T) {
	binDir := fakeKiroHome(t, "2.14.1", "2.14.2")
	p, cur, st := kiroCheck(binDir, "2.14.2")
	if st != kiroStale || cur != "2.14.1" || p != filepath.Join(binDir, "kiro-cli") {
		t.Fatalf("kiroCheck = (%q, %q, %v), want stale at 2.14.1", p, cur, st)
	}
	if kiroSkipInstall(binDir, "2.14.2", "", false) {
		t.Error("a stale install must NOT be skipped")
	}
	if kiroInstallCurrent() {
		t.Error("kiroInstallCurrent() = true for a stale install (the install route would answer \"done\")")
	}
	// Same version → current, and the recorded marker then short-circuits the probe.
	if _, _, st := kiroCheck(binDir, "2.14.1"); st != kiroCurrent {
		t.Errorf("matching version must be current, got %v", st)
	}
}

// TestKiroCheckMarkerFastPath: the marker written by a successful install lets the
// per-launch guard answer "current" from a stat, without exec'ing the 855MB binary.
func TestKiroCheckMarkerFastPath(t *testing.T) {
	binDir := fakeKiroHome(t, "", "2.14.2") // stub reports NO version
	if _, _, st := kiroCheck(binDir, "2.14.2"); st != kiroUnknownVer {
		t.Fatalf("without a marker an unreadable version must be kiroUnknownVer, got %v", st)
	}
	writeKiroVersionMarker(binDir, "2.14.2")
	if _, _, st := kiroCheck(binDir, "2.14.2"); st != kiroCurrent {
		t.Errorf("marker matching the pin must short-circuit to current, got %v", st)
	}
	// A marker from an older pin is only a fast path for "current" — it must not be
	// trusted to declare staleness on its own; the binary probe decides.
	if _, _, st := kiroCheck(binDir, "2.15.0"); st != kiroUnknownVer {
		t.Errorf("marker mismatch must fall through to the probe, got %v", st)
	}
}

// TestInstallKiroUnknownVersionLeavesAlone: a binary that can't report a version is left
// in place (warn only) rather than triggering a 554MB re-download on every launch.
func TestInstallKiroUnknownVersionLeavesAlone(t *testing.T) {
	fakeKiroHome(t, "", "2.14.2")
	if err := installKiro(true); err != nil {
		t.Fatalf("unparsable version should skip cleanly, got %v", err)
	}
}

// TestInstallKiroNoPinLeavesAlone: no versions.json (hand-built image) → nothing to
// compare against, so a present binary is left alone instead of being re-installed.
func TestInstallKiroNoPinLeavesAlone(t *testing.T) {
	fakeKiroHome(t, "2.14.1", "")
	if err := installKiro(true); err != nil {
		t.Fatalf("missing pin with a present binary should skip cleanly, got %v", err)
	}
}

// TestKiroInstallLockExcludes verifies the cross-process guard (B-2): while the lock
// is held, an independent flock attempt on the same file (a stand-in for a second
// pane's `workspace-agent install-kiro`) cannot acquire it; after release it can.
func TestKiroInstallLockExcludes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	unlock, err := kiroInstallLock()
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}

	// A separate open file description must fail a non-blocking exclusive lock.
	f2, err := os.OpenFile(kiroInstallLockPath(), os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("open lock: %v", err)
	}
	defer f2.Close()
	if err := syscall.Flock(int(f2.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
		_ = syscall.Flock(int(f2.Fd()), syscall.LOCK_UN)
		t.Fatal("second flock acquired while first is held; installs are not serialised")
	}

	unlock()

	// After release the lock is free again.
	if err := syscall.Flock(int(f2.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("lock not released: %v", err)
	}
	_ = syscall.Flock(int(f2.Fd()), syscall.LOCK_UN)
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
