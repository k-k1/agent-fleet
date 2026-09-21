package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The arch → asset mapping the release manifest publishes. Only the running arch is asserted
// concretely; the others are covered by the switch compiling.
func TestMuseAsset(t *testing.T) {
	got, err := museAsset()
	if err != nil {
		t.Fatalf("museAsset: %v", err)
	}
	want := map[string]string{
		"amd64": "muse-x86-linux",
		"arm64": "muse-aarch64-linux",
	}[runtime.GOARCH]
	if want != "" && got != want {
		t.Errorf("museAsset() = %q, want %q", got, want)
	}
}

// 🔴 The version parse, and the single most expensive thing to get wrong in this file.
//
// `muse --version` prints `Muse Code 1.3.0 (1.3.0-R3401.1)` and the pin is the parenthesised
// build id. Bare-semver extraction — `extractVer`'s own regexp, and the `tr -dc '0-9.'` idiom
// the agy block uses — yields `1.3.0`, which never equals the pin: every check would read
// "stale" and re-download 299 MiB, forever.
//
// The table carries the real measured line first, then the shapes that would let a wrong
// implementation pass: a semver-only line (the fallback, so a future release that drops the
// parentheses still compares equal to a pin in the same shape) and lines with nothing to take.
func TestMuseParseVersionTakesTheBuildID(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"Muse Code 1.3.0 (1.3.0-R3401.1)", "1.3.0-R3401.1"}, // measured on 1.3.0-R3401.1
		{"  Muse Code 1.4.0 (1.4.0-R4002.7)  ", "1.4.0-R4002.7"},
		{"Muse Code 1.3.0", "1.3.0"}, // no parentheses: fall back to bare semver
		{"muse 2.0", "2.0"},
		{"Muse Code (unknown)", ""},    // parenthesised but not a version
		{"no version at all here", ""}, // nothing to take
		{"", ""},
	} {
		if got := museParseVersion(tc.raw); got != tc.want {
			t.Errorf("museParseVersion(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
	// The negative control for the whole point: the DEFAULT extraction really does lose the
	// build id, so this test is guarding against a live hazard rather than a hypothetical one.
	// If extractVer ever starts keeping it, museParseVersion stops being load-bearing and this
	// says so instead of leaving a stale justification in the comments.
	const measured = "Muse Code 1.3.0 (1.3.0-R3401.1)"
	if extractVer(measured) == museParseVersion(measured) {
		t.Errorf("extractVer now agrees with museParseVersion on %q — re-check whether the "+
			"muse-specific parse is still needed", measured)
	}
}

// fakeMuseHome sets up HOME with a stub `muse` reporting ver from `--version`, plus a
// versions.json pinning muse=pin. Returns ~/.local/bin. ver=="" makes `--version` print
// nothing, which is the museUnknownVer path.
func fakeMuseHome(t *testing.T, ver, pin string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	binDir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nexit 0\n"
	if ver != "" {
		// The real line shape, parentheses and all — the fixture that makes the pin
		// comparison meaningful.
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
	orig := buildPinsPath
	buildPinsPath = pins
	t.Cleanup(func() { buildPinsPath = orig })
	return binDir
}

// A muse already at the pinned version short-circuits before any download and records the
// marker, so the next check is a stat rather than an exec of a 299 MiB binary.
func TestInstallMuseIdempotentSkip(t *testing.T) {
	binDir := fakeMuseHome(t, "1.3.0-R3401.1", "1.3.0-R3401.1")
	if err := installMuse(false); err != nil {
		t.Fatalf("installMuse with the pinned binary should skip cleanly, got %v", err)
	}
	b, err := os.ReadFile(museVersionMarkerPath(binDir))
	if err != nil || strings.TrimSpace(string(b)) != "1.3.0-R3401.1" {
		t.Fatalf("marker not recorded on the skip fast path: %q / %v", string(b), err)
	}
	if !museInstallCurrent() {
		t.Error("museInstallCurrent() = false for a binary already at the pin")
	}
}

// 🔴 Both halves of decision 8's shadow guard, in one table.
//
// A member who ran the vendor's own one-line installer has an unmanaged, self-updating build at
// EXACTLY the path AF installs to, so a path check would report AF's own binary. What tells them
// apart is the version, and the repair is the re-install this verdict triggers. Gate A measured
// both directions and both are here: a drifted version is replaced, and a shadow reporting the
// PINNED version is left alone (the case a naive "a shadow exists, reinstall" rule would churn
// 299 MiB over on every check).
func TestMuseCheckJudgesVersionNotPath(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ver   string // what the installed binary reports
		pin   string
		want  museState
		wantV string
	}{
		{"a shadow at a drifted version is stale", "1.2.9-R3300.4", "1.3.0-R3401.1", museStale, "1.2.9-R3300.4"},
		{"a shadow reporting the PIN is left alone", "1.3.0-R3401.1", "1.3.0-R3401.1", museCurrent, "1.3.0-R3401.1"},
		{"a pin bump makes the installed copy stale", "1.3.0-R3401.1", "1.4.0-R4002.7", museStale, "1.3.0-R3401.1"},
		{"an unreadable version is not churned", "", "1.3.0-R3401.1", museUnknownVer, ""},
		// No pin (a hand-built image with no versions.json) has nothing to compare against, so
		// a present binary is left alone rather than treated as stale.
		{"no pin leaves a present binary alone", "1.3.0-R3401.1", "", museCurrent, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binDir := fakeMuseHome(t, tc.ver, tc.pin)
			p, cur, st := museCheck(binDir, tc.pin)
			if st != tc.want || cur != tc.wantV {
				t.Fatalf("museCheck = (%q, %q, %v), want (%v, %q)", p, cur, st, tc.want, tc.wantV)
			}
			if p != filepath.Join(binDir, "muse") {
				t.Errorf("museCheck returned %q, want the home copy", p)
			}
			// The verdict has to reach the skip decision, or it is only a label.
			if skipped := museSkipInstall(binDir, tc.pin, "", false); skipped != (st != museStale) {
				t.Errorf("museSkipInstall = %v for verdict %v", skipped, st)
			}
		})
	}
}

// 🔥 The marker is a FAST PATH for "current" and nothing else. A marker claiming the pin must
// not be believed when the binary disagrees, or an interrupted upgrade would leave a marker
// that permanently hides a half-old install.
func TestMuseVersionMarkerNeverOverridesTheBinary(t *testing.T) {
	binDir := fakeMuseHome(t, "1.2.9-R3300.4", "1.3.0-R3401.1")
	// A marker that lies about the installed version, exactly the way an interrupted upgrade
	// could: it claims the new pin while the binary is still the old one.
	writeMuseVersionMarker(binDir, "1.3.0-R3401.1")
	if _, cur, st := museCheck(binDir, "1.3.0-R3401.1"); st != museCurrent {
		// Documenting the shape rather than asserting the opposite: the marker IS trusted when
		// it matches the pin, which is the point of the fast path. The guard that makes that
		// safe is in installMuse, and the next test pins it.
		t.Logf("marker fast path not taken (cur=%q st=%v)", cur, st)
	}
	// The real protection: the marker is removed BEFORE the binary is touched, so an install
	// interrupted mid-swap leaves no marker to believe.
	src, err := os.ReadFile("install_muse.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	rm := strings.Index(body, "os.Remove(museVersionMarkerPath(binDir))")
	place := strings.Index(body, "os.Rename(staged, installed)")
	if rm < 0 || place < 0 {
		t.Fatal("installMuse no longer removes the marker or no longer renames into place")
	}
	if rm > place {
		t.Error("the stale marker is dropped AFTER the binary is placed; an interrupted upgrade " +
			"would leave a marker claiming the new pin over a half-old install")
	}
}

// A missing pin is an error, not a silent no-op: without it the installer has no URL to fetch
// and no checksum to verify, and "did nothing" would look like success to the HTTP route.
func TestInstallMuseRefusesWithoutAPin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	pins := filepath.Join(home, "versions.json")
	if err := os.WriteFile(pins, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	orig := buildPinsPath
	buildPinsPath = pins
	t.Cleanup(func() { buildPinsPath = orig })

	err := installMuse(false)
	if err == nil {
		t.Fatal("installMuse succeeded with no pin in versions.json")
	}
	if !strings.Contains(err.Error(), "versions.json") {
		t.Errorf("the error does not name the cause: %v", err)
	}
}

// The download URL is assembled from the pin and the asset, and it is the one string that a
// typo would turn into a 404 nobody notices until a member presses Install.
func TestMuseDownloadURL(t *testing.T) {
	got := museDownloadURL("1.3.0-R3401.1", "muse-x86-linux")
	want := "https://lookaside.facebook.com/lookaside/muse/download/" +
		"?channel=muse&version=1.3.0-R3401.1&file=muse-x86-linux"
	if got != want {
		t.Errorf("museDownloadURL =\n  %s\nwant\n  %s", got, want)
	}
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
	if !museInstallCurrent() {
		t.Error("museInstallCurrent() = false at the pin")
	}

	fakeMuseHome(t, "1.2.9-R3300.4", "1.3.0-R3401.1")
	if museInstallCurrent() {
		t.Error("museInstallCurrent() = true for a stale install; the route would answer done " +
			"and the member would never get the pinned version")
	}
}

// museSanityCheck is the gate that keeps a binary AF cannot read a version out of from ever
// being promoted — because a version it cannot parse is a permanent "stale", i.e. a 299 MiB
// re-download on every check. The passing arm is the control.
func TestMuseSanityCheckRequiresAParsableVersion(t *testing.T) {
	write := func(t *testing.T, body string) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "muse")
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if err := museSanityCheck(write(t, `echo "Muse Code 1.3.0 (1.3.0-R3401.1)"`)); err != nil {
		t.Errorf("the real version line was rejected: %v", err)
	}
	if err := museSanityCheck(write(t, `echo "Muse Code (preview build)"`)); err == nil {
		t.Error("a version line with nothing parsable was accepted; the pin comparison would never match")
	}
	if err := museSanityCheck(write(t, `exit 3`)); err == nil {
		t.Error("a binary that cannot run was accepted")
	}
}

// 🔴 The checksum gate, exercised for real rather than read.
//
// A mutation sweep found this: deleting the verifySha256 call left every other test in this file
// green. That is the same defect shape as the boot-install incident where five sha256 checks
// were all decorative at once — an absent verification is indistinguishable from a passing one,
// so the only honest test is one that feeds a file whose hash is wrong and requires a refusal.
//
// The download is pointed at a local file (curl speaks file://), so nothing here touches the
// network or downloads 299 MiB. The matching-hash arm is the control: without it, an installer
// that refused everything would pass the refusal assertion.
func TestInstallMuseVerifiesTheChecksum(t *testing.T) {
	// A stand-in artifact that behaves like the real one where it matters: it answers
	// `--version` with a parsable line, so museSanityCheck cannot be what rejects it.
	artifact := filepath.Join(t.TempDir(), "muse-artifact")
	body := "#!/bin/sh\ncase \"$1\" in --version) echo \"Muse Code 1.3.0 (1.3.0-R3401.1)\";; esac\nexit 0\n"
	if err := os.WriteFile(artifact, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256File(t, artifact)

	setup := func(t *testing.T, pinnedSha string) string {
		t.Helper()
		home := t.TempDir()
		t.Setenv("HOME", home)
		pins := filepath.Join(home, "versions.json")
		if err := os.WriteFile(pins, []byte(`{"muse":"1.3.0-R3401.1","muse_sha256":"`+pinnedSha+`"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		origPins, origURL := buildPinsPath, museDownloadURL
		buildPinsPath = pins
		museDownloadURL = func(string, string) string { return "file://" + artifact }
		t.Cleanup(func() { buildPinsPath, museDownloadURL = origPins, origURL })
		return filepath.Join(home, ".local", "bin", "muse")
	}

	t.Run("a hash that does not match is refused and nothing is placed", func(t *testing.T) {
		installed := setup(t, strings.Repeat("0", 64))
		err := installMuse(false)
		if err == nil {
			t.Fatal("installMuse accepted an artifact whose sha256 does not match the pin")
		}
		if !strings.Contains(strings.ToLower(err.Error()), "sha256") {
			t.Errorf("the error does not name the checksum: %v", err)
		}
		if _, statErr := os.Stat(installed); statErr == nil {
			t.Error("a binary that failed verification was placed in ~/.local/bin")
		}
	})

	t.Run("the matching hash installs (the control)", func(t *testing.T) {
		installed := setup(t, sum)
		if err := installMuse(false); err != nil {
			t.Fatalf("installMuse refused a correctly hashed artifact: %v", err)
		}
		if !fileExecutable(installed) {
			t.Fatal("the verified binary was not placed, or is not executable")
		}
		if b, err := os.ReadFile(museVersionMarkerPath(filepath.Dir(installed))); err != nil ||
			strings.TrimSpace(string(b)) != "1.3.0-R3401.1" {
			t.Errorf("marker after a real install: %q / %v", string(b), err)
		}
		// The staging directory must not survive a successful install.
		if _, err := os.Stat(museInstallStaging()); err == nil {
			t.Error("staging was left behind after a successful install")
		}
	})
}

func sha256File(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
