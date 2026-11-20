package bootinstall

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const agyObject = "/9.9.9-b1/linux-x64/cli_linux_x64.tar.gz"

const goodAGY = "#!/bin/sh\necho 9.9.9\n"

func agyTarball(t *testing.T, script string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	if err := tw.WriteHeader(&tar.Header{Name: "antigravity", Mode: 0o755, Size: int64(len(script)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	tw.Write([]byte(script))
	tw.Close()
	zw.Close()
	return buf.Bytes()
}

type agyFixture struct {
	env    Env
	home   string
	status string
	out    *bytes.Buffer
}

// newAGY serves the pinned object; the pin's sha256 is computed from the tarball unless sum is set.
func newAGY(t *testing.T, script, sum string) *agyFixture {
	t.Helper()
	tb := agyTarball(t, script)
	if sum == "" {
		h := sha256.Sum256(tb)
		sum = hex.EncodeToString(h[:])
	}
	mux := http.NewServeMux()
	mux.HandleFunc(agyObject, func(w http.ResponseWriter, _ *http.Request) { w.Write(tb) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	home := t.TempDir()
	status := filepath.Join(t.TempDir(), "install.tsv")
	if err := os.WriteFile(status, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	return &agyFixture{home: home, status: status, out: out, env: Env{
		Home: home, StatusPath: status, BakedBin: t.TempDir(),
		Pins: map[string]string{"agy": "9.9.9", "agy_build": "b1", "agy_sha256": sum},
		Arch: "amd64", AgyBaseURL: srv.URL, Client: srv.Client(),
		AgyEnv:  func() []string { return nil },
		FreeMiB: func(string) (int64, bool) { return 100000, true },
		Stdout:  out, Stderr: out, RunTimeout: 10 * time.Second,
	}}
}

func (f *agyFixture) bin() string    { return filepath.Join(f.home, ".local", "bin", "agy") }
func (f *agyFixture) marker() string { return filepath.Join(f.home, ".local", "bin", agyMarker) }
func (f *agyFixture) statusText() string {
	b, _ := os.ReadFile(f.status)
	return string(b)
}
func (f *agyFixture) put(t *testing.T, script, marker string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(f.bin()), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(f.bin(), []byte(script), 0o755)
	if marker != "" {
		os.WriteFile(f.marker(), []byte(marker+"\n"), 0o644)
	}
}

func TestAGYInstallsAndWritesMarker(t *testing.T) {
	f := newAGY(t, goodAGY, "")
	if rc := AGY(t.Context(), f.env); rc != ExitOK {
		t.Fatalf("rc=%d out=%s", rc, f.out)
	}
	if b, _ := os.ReadFile(f.marker()); string(b) != "9.9.9\n" {
		t.Fatalf("marker %q", b)
	}
	if got := f.statusText(); got != "agy\tok\t\n" {
		t.Fatalf("status %q", got)
	}
	if !strings.Contains(f.out.String(), "boot-install agy 9.9.9") {
		t.Fatalf("out %s", f.out)
	}
}

// A wrong pinned digest installs nothing, writes no marker and is recorded as a checksum failure.
func TestAGYWrongSha256FailsInstall(t *testing.T) {
	f := newAGY(t, goodAGY, strings.Repeat("0", 64))
	if rc := AGY(t.Context(), f.env); rc != ExitFailed {
		t.Fatalf("rc=%d out=%s", rc, f.out)
	}
	for _, p := range []string{f.bin(), f.marker()} {
		if _, err := os.Lstat(p); err == nil {
			t.Fatalf("%s written despite a wrong digest", p)
		}
	}
	if got := f.statusText(); got != "agy\tfailed\tchecksum\n" {
		t.Fatalf("status %q", got)
	}
}

func TestAGYCandidateThatDoesNotRunKeepsWorkingCopyAndMarker(t *testing.T) {
	f := newAGY(t, "#!/bin/sh\nexit 1\n", "")
	f.put(t, "#!/bin/sh\necho 0.0.1\n", "0.0.1")
	f.env.Repin = true
	if rc := AGY(t.Context(), f.env); rc != ExitFailed {
		t.Fatalf("rc=%d out=%s", rc, f.out)
	}
	if b, _ := os.ReadFile(f.bin()); !strings.Contains(string(b), "0.0.1") {
		t.Fatal("working copy displaced")
	}
	if b, _ := os.ReadFile(f.marker()); string(b) != "0.0.1\n" {
		t.Fatalf("marker moved: %q", b)
	}
}

func TestAGYDownloadFailureRecordsNetwork(t *testing.T) {
	f := newAGY(t, goodAGY, "")
	f.env.AgyBaseURL = "http://127.0.0.1:1"
	if rc := AGY(t.Context(), f.env); rc != ExitFailed {
		t.Fatalf("rc=%d", rc)
	}
	if got := f.statusText(); got != "agy\tfailed\tnetwork\n" {
		t.Fatalf("status %q", got)
	}
}

// A binary that exists but is silent (truncated by a full disk) is reinstalled even though the
// marker says the pin: the marker must not vouch for it.
func TestAGYSilentBinaryWithMatchingMarkerIsReinstalled(t *testing.T) {
	f := newAGY(t, goodAGY, "")
	f.put(t, "#!/bi", "9.9.9")
	if rc := AGY(t.Context(), f.env); rc != ExitOK {
		t.Fatalf("rc=%d out=%s", rc, f.out)
	}
	if !f.env.runs(f.bin()) {
		t.Fatal("reinstalled binary does not run")
	}
	if !strings.Contains(f.out.String(), "boot-install agy 9.9.9") {
		t.Fatalf("out %s", f.out)
	}
}

func TestAGYHealthyCopyIsLeftAlone(t *testing.T) {
	f := newAGY(t, goodAGY, "")
	f.put(t, "#!/bin/sh\necho 1.2.3\n", "9.9.9")
	if rc := AGY(t.Context(), f.env); rc != ExitOK {
		t.Fatalf("rc=%d", rc)
	}
	if !strings.Contains(f.out.String(), "agy already present") || f.statusText() != "agy\tskipped\t\n" {
		t.Fatalf("out %s status %q", f.out, f.statusText())
	}
	if b, _ := os.ReadFile(f.bin()); !strings.Contains(string(b), "1.2.3") {
		t.Fatal("healthy copy replaced without repin")
	}
}

// Repin decides from the binary, not the marker: marker == pin but the binary is 1.2.3.
func TestAGYRepinMovesDriftedBinaryDespiteMarker(t *testing.T) {
	f := newAGY(t, goodAGY, "")
	f.put(t, "#!/bin/sh\necho 1.2.3\n", "9.9.9")
	f.env.Repin = true
	if rc := AGY(t.Context(), f.env); rc != ExitOK {
		t.Fatalf("rc=%d out=%s", rc, f.out)
	}
	if v := f.env.AgyEffectiveVersion(); v != "9.9.9" {
		t.Fatalf("version %q", v)
	}
}

func TestAGYNoSpaceKeepsUsableCopy(t *testing.T) {
	f := newAGY(t, goodAGY, "")
	f.put(t, "#!/bin/sh\necho 1.2.3\n", "")
	f.env.Repin = true
	f.env.FreeMiB = func(string) (int64, bool) { return 10, true }
	if rc := AGY(t.Context(), f.env); rc != ExitNoSpace {
		t.Fatalf("rc=%d", rc)
	}
	if got := f.statusText(); got != "agy\tskipped\tno_space\n" {
		t.Fatalf("status %q", got)
	}
	f2 := newAGY(t, goodAGY, "")
	f2.env.FreeMiB = func(string) (int64, bool) { return 10, true }
	if rc := AGY(t.Context(), f2.env); rc != ExitNoSpace || f2.statusText() != "agy\tfailed\tno_space\n" {
		t.Fatalf("rc=%d status %q", rc, f2.statusText())
	}
}

func TestAGYIncompletePinInstallsNothing(t *testing.T) {
	f := newAGY(t, goodAGY, "")
	delete(f.env.Pins, "agy_sha256")
	if rc := AGY(t.Context(), f.env); rc != ExitOK {
		t.Fatalf("rc=%d", rc)
	}
	if _, err := os.Lstat(f.bin()); err == nil {
		t.Fatal("installed without a full pin")
	}
	if f.statusText() != "" {
		t.Fatalf("status %q", f.statusText())
	}
}

// The host overlay reaches the binary: this stand-in aborts unless the mask is in its env, the
// way agy does on a host without RDRAND.
func TestAGYEffectiveVersionAppliesMaskAndMarkerOnlyWithoutBinary(t *testing.T) {
	f := newAGY(t, goodAGY, "")
	if v := f.env.AgyEffectiveVersion(); v != "" {
		t.Fatalf("no binary, no marker: %q", v)
	}
	os.MkdirAll(filepath.Dir(f.marker()), 0o755)
	os.WriteFile(f.marker(), []byte("4.5.6\n"), 0o644)
	if v := f.env.AgyEffectiveVersion(); v != "4.5.6" {
		t.Fatalf("marker answers when there is no binary: %q", v)
	}
	f.put(t, "#!/bi", "")
	if v := f.env.AgyEffectiveVersion(); v != "" {
		t.Fatalf("a silent binary must not be vouched for by the marker: %q", v)
	}
	f.put(t, "#!/bin/sh\n[ \"$AF_TEST_MASK\" = on ] || exit 134\necho 'agy version 1.2.3-x'\n", "")
	if v := f.env.AgyEffectiveVersion(); v != "" {
		t.Fatalf("unmasked: %q", v)
	}
	f.env.AgyEnv = func() []string { return []string{"AF_TEST_MASK=on"} }
	if v := f.env.AgyEffectiveVersion(); v != "1.2.3" { // non-digits dropped, as the shell's tr -dc '0-9.' did
		t.Fatalf("masked: %q", v)
	}
}

// A candidate that exits 0 but prints no version never replaces the working copy or its marker.
func TestAGYCandidateWithoutVersionKeepsWorkingCopy(t *testing.T) {
	f := newAGY(t, "#!/bin/sh\nexit 0\n", "")
	f.put(t, "#!/bin/sh\necho 0.0.1\n", "0.0.1")
	f.env.Repin = true
	if rc := AGY(t.Context(), f.env); rc != ExitFailed {
		t.Fatalf("rc=%d out=%s", rc, f.out)
	}
	if b, _ := os.ReadFile(f.bin()); !strings.Contains(string(b), "0.0.1") {
		t.Fatal("working copy displaced by a silent candidate")
	}
	if b, _ := os.ReadFile(f.marker()); string(b) != "0.0.1\n" {
		t.Fatalf("marker moved: %q", b)
	}
	if got := f.statusText(); got != "agy\tfailed\tother\n" {
		t.Fatalf("status %q", got)
	}
}
