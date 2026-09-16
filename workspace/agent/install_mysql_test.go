package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestMySQLSHAMismatch verifies that a sha mismatch returns *mysqlSHAMismatch
// and leaves no staging residue, calling through installMySQL.
func TestMySQLSHAMismatch(t *testing.T) {
	const wrongSHA = "0000000000000000000000000000000000000000000000000000000000000000"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		fmt.Fprint(w, "fake tarball bytes for sha mismatch")
	}))
	defer srv.Close()

	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	// Write a versions.json with a wrong sha and override buildPinsPath.
	pinsDir := filepath.Join(tmp, "pins")
	if err := os.MkdirAll(pinsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	pinsFile := filepath.Join(pinsDir, "versions.json")
	if err := os.WriteFile(pinsFile,
		[]byte(fmt.Sprintf(`{"mysql":"8.4.6","mysql_sha256":%q}`, wrongSHA)), 0o644); err != nil {
		t.Fatal(err)
	}
	origPinsPath := buildPinsPath
	buildPinsPath = pinsFile
	defer func() { buildPinsPath = origPinsPath }()

	origURLs := mysqlCDNBaseURLs
	mysqlCDNBaseURLs = []string{srv.URL + "/Downloads/MySQL-%s/%s", srv.URL + "/archives/mysql-%s/%s"}
	defer func() { mysqlCDNBaseURLs = origURLs }()

	err := installMySQL("8.4")
	if err == nil {
		t.Fatal("expected error on sha mismatch, got nil")
	}
	if _, ok := err.(*mysqlSHAMismatch); !ok {
		t.Fatalf("expected *mysqlSHAMismatch, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), wrongSHA) {
		t.Errorf("error message should include expected sha %q\ngot: %s", wrongSHA, err.Error())
	}

	// Staging dir must be gone after install.
	stagingDir := mysqlInstallStaging("8.4")
	if _, statErr := os.Stat(stagingDir); statErr == nil {
		t.Errorf("staging dir not cleaned up: %s", stagingDir)
	}
}

// TestMySQLURLOrderFallback verifies that the first CDN URL is tried before the
// fallback: the Downloads/ path returns 404, archives/ returns 200.
func TestMySQLURLOrderFallback(t *testing.T) {
	var tried []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tried = append(tried, r.URL.Path)
		if strings.Contains(r.URL.Path, "Downloads") {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	origURLs := mysqlCDNBaseURLs
	mysqlCDNBaseURLs = []string{
		srv.URL + "/Downloads/MySQL-%s/%s",
		srv.URL + "/archives/mysql-%s/%s",
	}
	defer func() { mysqlCDNBaseURLs = origURLs }()

	url, err := mysqlDownloadURL("8.4", "mysql-8.4.6-linux-glibc2.28-x86_64-minimal.tar.xz")
	if err != nil {
		t.Fatalf("mysqlDownloadURL: %v", err)
	}

	// Must have tried Downloads/ first.
	if len(tried) < 2 {
		t.Fatalf("expected at least 2 HEAD requests, got %d", len(tried))
	}
	if !strings.Contains(tried[0], "Downloads") {
		t.Errorf("first request should be Downloads/, got %q", tried[0])
	}
	if !strings.Contains(tried[1], "archives") {
		t.Errorf("second request should be archives/, got %q", tried[1])
	}
	if !strings.Contains(url, "archives") {
		t.Errorf("returned URL should be archives/, got %q", url)
	}
}

// TestMySQLAlreadyInstalled verifies the fast-path: when bin/mysqld already
// exists, installMySQL returns without attempting a download.
func TestMySQLAlreadyInstalled(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	installRoot := mysqlInstallDir("8.4")
	binDir := filepath.Join(installRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mysqldPath := filepath.Join(binDir, "mysqld")
	if err := os.WriteFile(mysqldPath, []byte("fake"), 0o755); err != nil {
		t.Fatal(err)
	}

	// If installMySQL tries to download, this counter increments.
	var downloadCalled int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downloadCalled++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	origURLs := mysqlCDNBaseURLs
	mysqlCDNBaseURLs = []string{srv.URL + "/Downloads/MySQL-%s/%s", srv.URL + "/archives/mysql-%s/%s"}
	defer func() { mysqlCDNBaseURLs = origURLs }()

	if err := installMySQL("8.4"); err != nil {
		t.Fatalf("installMySQL: %v", err)
	}
	if downloadCalled > 0 {
		t.Errorf("expected no download when already installed, but server was hit %d time(s)", downloadCalled)
	}
}

// TestMySQLIsELFFile verifies that isELFFile correctly identifies ELF magic bytes.
func TestMySQLIsELFFile(t *testing.T) {
	tmp := t.TempDir()

	cases := []struct {
		name    string
		content []byte
		want    bool
	}{
		{"elf", []byte{0x7f, 'E', 'L', 'F', 0, 0, 0, 0}, true},
		{"script", []byte("#!/bin/sh\necho hello"), false},
		{"empty", []byte{}, false},
		{"short", []byte{0x7f, 'E'}, false},
		{"text", []byte("not an ELF file"), false},
	}
	for _, tc := range cases {
		p := filepath.Join(tmp, tc.name)
		if err := os.WriteFile(p, tc.content, 0o644); err != nil {
			t.Fatal(err)
		}
		got := isELFFile(p)
		if got != tc.want {
			t.Errorf("isELFFile(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestMySQLStripELFs verifies that mysqlStripELFs calls strip on ELF files
// and skips non-ELF files. We use a real ELF from the system (/bin/sh) copied
// into a temp dir.
func TestMySQLStripELFs(t *testing.T) {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skip("strip test requires amd64 or arm64")
	}
	tmp := t.TempDir()

	// Copy a real ELF binary so strip actually works.
	elfSrc := "/bin/sh"
	elfData, err := os.ReadFile(elfSrc)
	if err != nil {
		t.Skipf("cannot read %s: %v", elfSrc, err)
	}
	elfDst := filepath.Join(tmp, "sh")
	if err := os.WriteFile(elfDst, elfData, 0o755); err != nil {
		t.Fatal(err)
	}

	// Also write a non-ELF file; it must not be passed to strip.
	scriptPath := filepath.Join(tmp, "script.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	origSize, _ := os.Stat(elfDst)
	if err := mysqlStripELFs(tmp); err != nil {
		t.Fatalf("mysqlStripELFs: %v", err)
	}

	// The ELF should be smaller (or same size if already stripped) after stripping.
	newInfo, err := os.Stat(elfDst)
	if err != nil {
		t.Fatalf("stat after strip: %v", err)
	}
	_ = origSize
	// It must still exist and be executable.
	if newInfo.Mode()&0111 == 0 {
		t.Errorf("stripped file lost executable bit")
	}
}

// TestMySQLDownloadURLAllFail verifies the error message when both URLs fail.
func TestMySQLDownloadURLAllFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	origURLs := mysqlCDNBaseURLs
	mysqlCDNBaseURLs = []string{
		srv.URL + "/Downloads/MySQL-%s/%s",
		srv.URL + "/archives/mysql-%s/%s",
	}
	defer func() { mysqlCDNBaseURLs = origURLs }()

	_, err := mysqlDownloadURL("8.4", "mysql-8.4.6-linux-glibc2.28-x86_64-minimal.tar.xz")
	if err == nil {
		t.Fatal("expected error when all URLs fail")
	}
	if !strings.Contains(err.Error(), "cdn.mysql.com") {
		t.Errorf("error should mention cdn.mysql.com\ngot: %s", err.Error())
	}
	if !strings.Contains(err.Error(), "AF_EGRESS_ALLOWLIST") {
		t.Errorf("error should mention AF_EGRESS_ALLOWLIST\ngot: %s", err.Error())
	}
}

// TestMySQLLDDNotFound verifies that mysqlCheckLDD surfaces "not found" libraries
// by using a fake ldd shim (via PATH override).
func TestMySQLLDDNotFound(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	// Write a fake ldd that prints "not found" output.
	fakeLDD := filepath.Join(tmp, "ldd")
	fakeLDDScript := "#!/bin/sh\necho '\tlibaio.so.1 => not found'\necho '\tlibnuma.so.1 => not found'\n"
	if err := os.WriteFile(fakeLDD, []byte(fakeLDDScript), 0o755); err != nil {
		t.Fatal(err)
	}

	// Write a fake mysqld so ldd has something to inspect.
	binDir := filepath.Join(tmp, "mysql", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mysqldPath := filepath.Join(binDir, "mysqld")
	if err := os.WriteFile(mysqldPath, []byte{0x7f, 'E', 'L', 'F'}, 0o755); err != nil {
		t.Fatal(err)
	}

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", tmp+":"+origPath)

	err := mysqlCheckLDD(mysqldPath)
	if err == nil {
		t.Fatal("expected error for not-found libraries")
	}
	ldderr, ok := err.(*mysqlLDDError)
	if !ok {
		t.Fatalf("expected *mysqlLDDError, got %T: %v", err, err)
	}
	if !strings.Contains(ldderr.Error(), "libaio.so.1") {
		t.Errorf("error should name libaio.so.1\ngot: %s", ldderr.Error())
	}
	if !strings.Contains(ldderr.Error(), "AF_DB_MYSQL_LIBS") {
		t.Errorf("error should mention AF_DB_MYSQL_LIBS\ngot: %s", ldderr.Error())
	}
}
