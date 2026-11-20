package bootinstall

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const rtkAssetAMD64 = "rtk-oss-x86_64-unknown-linux-musl.tar.gz"

func rtkTarball(t *testing.T, script string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	if err := tw.WriteHeader(&tar.Header{Name: "rtk", Mode: 0o755, Size: int64(len(script)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	tw.Write([]byte(script))
	tw.Close()
	zw.Close()
	return buf.Bytes()
}

type rtkFixture struct {
	env    Env
	home   string
	status string
	out    *bytes.Buffer
}

// newRTK serves a release whose checksums.txt lists sum (computed from the tarball when "").
func newRTK(t *testing.T, script, sum string) *rtkFixture {
	t.Helper()
	tb := rtkTarball(t, script)
	if sum == "" {
		h := sha256.Sum256(tb)
		sum = hex.EncodeToString(h[:])
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1.2.3/"+rtkAssetAMD64, func(w http.ResponseWriter, _ *http.Request) { w.Write(tb) })
	mux.HandleFunc("/v1.2.3/checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "%s  %s\n", sum, rtkAssetAMD64)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	home := t.TempDir()
	status := filepath.Join(t.TempDir(), "install.tsv")
	if err := os.WriteFile(status, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	return &rtkFixture{home: home, status: status, out: out, env: Env{
		Home: home, StatusPath: status, Pins: map[string]string{"rtk": "1.2.3"},
		Arch: "amd64", RTKBaseURL: srv.URL, Client: srv.Client(),
		FreeMiB: func(string) (int64, bool) { return 100000, true },
		Stdout:  out, Stderr: out, RunTimeout: 10 * time.Second,
	}}
}

func (f *rtkFixture) bin() string { return filepath.Join(f.home, ".local", "bin", "rtk") }
func (f *rtkFixture) statusText() string {
	b, _ := os.ReadFile(f.status)
	return string(b)
}

const goodRTK = "#!/bin/sh\necho rtk 1.2.3\n"

func TestRTKInstalls(t *testing.T) {
	f := newRTK(t, goodRTK, "")
	if rc := RTK(t.Context(), f.env); rc != ExitOK {
		t.Fatalf("rc=%d out=%s", rc, f.out)
	}
	if _, err := os.Stat(f.bin()); err != nil {
		t.Fatal(err)
	}
	if got := f.statusText(); got != "rtk\tok\t\n" {
		t.Fatalf("status %q", got)
	}
	if !strings.Contains(f.out.String(), "boot-install rtk 1.2.3") {
		t.Fatalf("out %s", f.out)
	}
}

// The point of the move: a wrong digest fails the install, installs nothing and is recorded as
// a checksum failure.
func TestRTKWrongSha256FailsInstall(t *testing.T) {
	f := newRTK(t, goodRTK, strings.Repeat("0", 64))
	if rc := RTK(t.Context(), f.env); rc != ExitFailed {
		t.Fatalf("rc=%d, want %d; out=%s", rc, ExitFailed, f.out)
	}
	if _, err := os.Lstat(f.bin()); err == nil {
		t.Fatal("binary installed despite a wrong digest")
	}
	if got := f.statusText(); got != "rtk\tfailed\tchecksum\n" {
		t.Fatalf("status %q", got)
	}
	if strings.Contains(f.out.String(), "boot-install rtk 1.2.3\n") {
		t.Fatalf("success logged: %s", f.out)
	}
}

func TestRTKCandidateThatDoesNotRunKeepsWorkingCopy(t *testing.T) {
	f := newRTK(t, "#!/bin/sh\nexit 1\n", "")
	if err := os.MkdirAll(filepath.Dir(f.bin()), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(f.bin(), []byte("#!/bin/sh\necho rtk 0.0.1\n"), 0o755)
	f.env.Repin = true
	if rc := RTK(t.Context(), f.env); rc != ExitFailed {
		t.Fatalf("rc=%d out=%s", rc, f.out)
	}
	b, _ := os.ReadFile(f.bin())
	if !strings.Contains(string(b), "0.0.1") {
		t.Fatal("working copy displaced")
	}
	if m, _ := filepath.Glob(f.bin() + ".af-new.*"); len(m) != 0 {
		t.Fatalf("temp candidate left: %v", m)
	}
}

func TestRTKDownloadFailureRecordsNetwork(t *testing.T) {
	f := newRTK(t, goodRTK, "")
	f.env.RTKBaseURL = "http://127.0.0.1:1"
	if rc := RTK(t.Context(), f.env); rc != ExitFailed {
		t.Fatalf("rc=%d", rc)
	}
	if got := f.statusText(); got != "rtk\tfailed\tnetwork\n" {
		t.Fatalf("status %q", got)
	}
}

func TestRTKSkipsWhenPresentAndCurrent(t *testing.T) {
	f := newRTK(t, goodRTK, strings.Repeat("0", 64)) // would fail if it tried to install
	os.MkdirAll(filepath.Dir(f.bin()), 0o755)
	os.WriteFile(f.bin(), []byte(goodRTK), 0o755)
	f.env.Repin = true
	if rc := RTK(t.Context(), f.env); rc != ExitOK {
		t.Fatalf("rc=%d out=%s", rc, f.out)
	}
	if got := f.statusText(); got != "rtk\tskipped\t\n" {
		t.Fatalf("status %q", got)
	}
}

func TestRTKReinstallsBrokenCopy(t *testing.T) {
	f := newRTK(t, goodRTK, "")
	os.MkdirAll(filepath.Dir(f.bin()), 0o755)
	os.WriteFile(f.bin(), []byte("#!/bi"), 0o755)
	if rc := RTK(t.Context(), f.env); rc != ExitOK {
		t.Fatalf("rc=%d out=%s", rc, f.out)
	}
	if b, _ := os.ReadFile(f.bin()); string(b) != goodRTK {
		t.Fatalf("not replaced: %q", b)
	}
}

func TestRTKNoSpaceSkipsAndSignals(t *testing.T) {
	f := newRTK(t, goodRTK, "")
	f.env.FreeMiB = func(string) (int64, bool) { return 10, true }
	if rc := RTK(t.Context(), f.env); rc != ExitNoSpace {
		t.Fatalf("rc=%d", rc)
	}
	if got := f.statusText(); got != "rtk\tfailed\tno_space\n" {
		t.Fatalf("status %q", got)
	}
}

func TestClassify(t *testing.T) {
	if classify(fmt.Errorf("write: no space left on device")) != ReasonNoSpace {
		t.Fatal("no_space")
	}
	if classify(fmt.Errorf("%w: x", ErrChecksum)) != ReasonChecksum {
		t.Fatal("checksum")
	}
	if classify(fmt.Errorf("boom")) != ReasonOther {
		t.Fatal("other")
	}
}

// A dangling ~/.local/bin/rtk is absent (the shell's `[ -e ]` followed links) and is reinstalled.
func TestRTKReinstallsDanglingLink(t *testing.T) {
	f := newRTK(t, goodRTK, "")
	os.MkdirAll(filepath.Dir(f.bin()), 0o755)
	if err := os.Symlink(filepath.Join(f.home, "gone"), f.bin()); err != nil {
		t.Fatal(err)
	}
	if rc := RTK(t.Context(), f.env); rc != ExitOK {
		t.Fatalf("rc=%d out=%s", rc, f.out)
	}
	if b, _ := os.ReadFile(f.bin()); string(b) != goodRTK {
		t.Fatalf("not reinstalled: %q", b)
	}
}

// A link planted at a predictable candidate name must not redirect the write into the copy
// being replaced.
func TestRTKCandidateNameIsNotPredictable(t *testing.T) {
	f := newRTK(t, "#!/bin/sh\nexit 1\n", "")
	os.MkdirAll(filepath.Dir(f.bin()), 0o755)
	os.WriteFile(f.bin(), []byte("#!/bin/sh\necho rtk 0.0.1\n"), 0o755)
	if err := os.Symlink(f.bin(), fmt.Sprintf("%s.af-new.%d", f.bin(), os.Getpid())); err != nil {
		t.Fatal(err)
	}
	f.env.Repin = true
	if rc := RTK(t.Context(), f.env); rc != ExitFailed { // the candidate was really tried and refused
		t.Fatalf("rc=%d out=%s", rc, f.out)
	}
	if b, _ := os.ReadFile(f.bin()); !strings.Contains(string(b), "0.0.1") {
		t.Fatalf("current copy overwritten through a planted link: %q", b)
	}
}

func TestChecksumForRejectsConflictingLines(t *testing.T) {
	good, bad := strings.Repeat("a", 64), strings.Repeat("b", 64)
	for name, body := range map[string]string{
		"conflict":  good + "  x.tgz\n" + bad + "  x.tgz\n",
		"malformed": "zz  x.tgz\n",
		"missing":   good + "  y.tgz\n",
	} {
		p := filepath.Join(t.TempDir(), "c.txt")
		os.WriteFile(p, []byte(body), 0o600)
		if _, err := checksumFor(p, "x.tgz"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	p := filepath.Join(t.TempDir(), "c.txt")
	os.WriteFile(p, []byte(good+"  x.tgz\n"+good+" *x.tgz\n"), 0o600)
	if got, err := checksumFor(p, "x.tgz"); err != nil || got != good {
		t.Fatalf("got %q %v", got, err)
	}
}

func TestRTKTransientStatusRetriedAndTruncationIsNetwork(t *testing.T) {
	f := newRTK(t, goodRTK, "")
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits == 1 {
			w.WriteHeader(http.StatusRequestTimeout)
			return
		}
		w.Header().Set("Content-Length", "100")
		w.Write([]byte("short")) // body cut short
	}))
	defer srv.Close()
	f.env.RTKBaseURL = srv.URL
	f.env.Client = srv.Client()
	if rc := RTK(t.Context(), f.env); rc != ExitFailed {
		t.Fatalf("rc=%d", rc)
	}
	if hits < 2 {
		t.Fatal("408 was not retried")
	}
	if got := f.statusText(); got != "rtk\tfailed\tnetwork\n" {
		t.Fatalf("status %q", got)
	}
}

func TestRTKNoPinKeepsSkippedForExistingCopy(t *testing.T) {
	f := newRTK(t, goodRTK, "")
	f.env.Pins = map[string]string{}
	os.MkdirAll(filepath.Dir(f.bin()), 0o755)
	os.WriteFile(f.bin(), []byte(goodRTK), 0o755)
	if rc := RTK(t.Context(), f.env); rc != ExitOK {
		t.Fatalf("rc=%d", rc)
	}
	if got := f.statusText(); got != "rtk\tskipped\t\n" {
		t.Fatalf("status %q", got)
	}
}

func TestRTKRepinSkippedForNoSpaceKeepsOldCopy(t *testing.T) {
	f := newRTK(t, goodRTK, "")
	os.MkdirAll(filepath.Dir(f.bin()), 0o755)
	old := "#!/bin/sh\necho rtk 0.0.1\n"
	os.WriteFile(f.bin(), []byte(old), 0o755)
	f.env.Repin = true
	f.env.FreeMiB = func(string) (int64, bool) { return 10, true }
	if rc := RTK(t.Context(), f.env); rc != ExitNoSpace {
		t.Fatalf("rc=%d", rc)
	}
	if got := f.statusText(); got != "rtk\tskipped\tno_space\n" {
		t.Fatalf("status %q", got)
	}
	if b, _ := os.ReadFile(f.bin()); string(b) != old {
		t.Fatal("old copy changed")
	}
}

// A wrapper whose child keeps our output pipes open must not stall the probe past its deadline.
func TestProbeBoundedWhenChildHoldsPipes(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "rtk")
	os.WriteFile(p, []byte("#!/bin/sh\nsleep 30 &\necho rtk 1.2.3\nsleep 30\n"), 0o755)
	e := Env{RunTimeout: 500 * time.Millisecond}
	start := time.Now()
	e.probe(p)
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("probe took %v", d)
	}
}

// A linker warning on stderr must not be mistaken for the version line.
func TestRTKRepinReadsVersionFromStdoutOnly(t *testing.T) {
	f := newRTK(t, goodRTK, strings.Repeat("0", 64)) // an install attempt would fail
	os.MkdirAll(filepath.Dir(f.bin()), 0o755)
	os.WriteFile(f.bin(), []byte("#!/bin/sh\necho 'ERROR: ld.so: object x cannot be preloaded' >&2\necho rtk 1.2.3\n"), 0o755)
	f.env.Repin = true
	if rc := RTK(t.Context(), f.env); rc != ExitOK {
		t.Fatalf("rc=%d out=%s", rc, f.out)
	}
	if got := f.statusText(); got != "rtk\tskipped\t\n" {
		t.Fatalf("status %q", got)
	}
}

func TestRTKLocalWriteFailureIsNotNetwork(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores directory modes")
	}
	f := newRTK(t, goodRTK, "")
	bin := filepath.Dir(f.bin())
	os.MkdirAll(bin, 0o755)
	os.Chmod(bin, 0o555)
	t.Cleanup(func() { os.Chmod(bin, 0o755) })
	if rc := RTK(t.Context(), f.env); rc != ExitFailed {
		t.Fatalf("rc=%d out=%s", rc, f.out)
	}
	if got := f.statusText(); got != "rtk\tfailed\tother\n" {
		t.Fatalf("status %q", got)
	}
}
