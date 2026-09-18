package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
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

// TestMySQLStripELFs verifies that mysqlStripELFs calls the strip shim exactly
// on ELF files, skips non-ELF files, and returns nil (with a stderr warning)
// when strip is absent from PATH.
func TestMySQLStripELFs(t *testing.T) {
	// Sub-test: strip is on PATH — verify it is called on the ELF and not the script.
	t.Run("called_on_elf_only", func(t *testing.T) {
		binDir := t.TempDir()
		calledFile := filepath.Join(binDir, "strip_args.txt")
		// Fake strip records each argument in a file.
		fakeStrip := filepath.Join(binDir, "strip")
		script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$1\" >> %q\n", calledFile)
		if err := os.WriteFile(fakeStrip, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

		filesDir := t.TempDir()
		elfPath := filepath.Join(filesDir, "libfoo.so")
		if err := os.WriteFile(elfPath, []byte{0x7f, 'E', 'L', 'F', 0, 0, 0, 0}, 0o755); err != nil {
			t.Fatal(err)
		}
		scriptPath := filepath.Join(filesDir, "run.sh")
		if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}

		if err := mysqlStripELFs(filesDir); err != nil {
			t.Fatalf("mysqlStripELFs: %v", err)
		}

		data, err := os.ReadFile(calledFile)
		if err != nil {
			t.Fatal("fake strip was never called (calledFile not written)")
		}
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		if len(lines) != 1 || lines[0] != elfPath {
			t.Errorf("strip called with %v, want [%q]", lines, elfPath)
		}
	})

	// Sub-test: strip absent → returns nil (not an error), warns to stderr.
	t.Run("absent_returns_nil", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir()) // empty dir — no strip
		if err := mysqlStripELFs(t.TempDir()); err != nil {
			t.Fatalf("expected nil when strip absent, got %v", err)
		}
	})
}

// TestMySQLExtractSubset exercises mysqlExtractSubset via the mysqlGOARCH seam.
// It builds a synthetic arm64-shaped tarball and asserts that exactly the expected
// subset is extracted: bin/{mysqld,mysql,mysqladmin,mysqldump}, lib/private/*.so*,
// lib/private/icudt*l, lib/plugin/*.so (minus debug/), share — and nothing else.
func TestMySQLExtractSubset(t *testing.T) {
	if _, err := exec.LookPath("tar"); err != nil {
		t.Skip("tar not available")
	}
	if _, err := exec.LookPath("xz"); err != nil {
		t.Skip("xz not available")
	}

	// Build the synthetic source tree under a versioned top-level dir (like the real tarball).
	srcDir := t.TempDir()
	top := filepath.Join(srcDir, "mysql-8.4.6-linux-glibc2.28-aarch64")

	type entry struct {
		path    string
		include bool
	}
	entries := []entry{
		// bin subset — included
		{"bin/mysqld", true},
		{"bin/mysql", true},
		{"bin/mysqladmin", true},
		{"bin/mysqldump", true},
		// extra binary — excluded
		{"bin/mysqlbinlog", false},
		// lib/private shared objects — included
		{"lib/private/libprotobuf-lite.so.24.4.0", true},
		// sasl2 is under lib/private and matched by *.so* crossing / — included
		{"lib/private/sasl2/libsasldb.so", true},
		// ICU data directory — included (issue 2)
		{"lib/private/icudt77l/icudt77l.dat", true},
		// lib/plugin *.so — included
		{"lib/plugin/auth_socket.so", true},
		// lib/plugin/debug — excluded (issue 1)
		{"lib/plugin/debug/auth_socket.so", false},
		// share — included
		{"share/english/errmsg.sys", true},
		// man — excluded
		{"man/man1/mysql.1", false},
	}

	for _, e := range entries {
		p := filepath.Join(top, e.path)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(e.path), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	tarPath := filepath.Join(t.TempDir(), "mysql.tar.xz")
	if err := runCmd("tar", "-cJf", tarPath, "-C", srcDir, "mysql-8.4.6-linux-glibc2.28-aarch64"); err != nil {
		t.Fatalf("create test tarball: %v", err)
	}

	destDir := t.TempDir()
	if err := mysqlExtractSubset(tarPath, destDir); err != nil {
		t.Fatalf("mysqlExtractSubset: %v", err)
	}

	for _, e := range entries {
		p := filepath.Join(destDir, e.path)
		_, statErr := os.Stat(p)
		exists := statErr == nil
		if exists != e.include {
			if e.include {
				t.Errorf("expected %q in subset, not found", e.path)
			} else {
				t.Errorf("expected %q excluded from subset, but it exists", e.path)
			}
		}
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
// via a fake ldd shim, and that AF_DB_MYSQL_LIBS is forwarded as the leading
// segment of LD_LIBRARY_PATH to the ldd subprocess.
func TestMySQLLDDNotFound(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	// Fake ldd: records received LD_LIBRARY_PATH, then prints "not found" lines.
	envFile := filepath.Join(tmp, "ldd_env.txt")
	fakeLDD := filepath.Join(tmp, "ldd")
	fakeLDDScript := fmt.Sprintf(
		"#!/bin/sh\nprintf '%%s' \"$LD_LIBRARY_PATH\" > %q\necho '\tlibaio.so.1 => not found'\necho '\tlibnuma.so.1 => not found'\n",
		envFile,
	)
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

	const testLibsDir = "/fake/libs/dir"
	t.Setenv("AF_DB_MYSQL_LIBS", testLibsDir)
	t.Setenv("LD_LIBRARY_PATH", "")
	t.Setenv("PATH", tmp+":"+os.Getenv("PATH"))

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

	// Verify fake ldd received AF_DB_MYSQL_LIBS as the leading LD_LIBRARY_PATH segment.
	envData, err2 := os.ReadFile(envFile)
	if err2 != nil {
		t.Fatalf("ldd env file not written: %v", err2)
	}
	ldLibPath := string(envData)
	if !strings.HasPrefix(ldLibPath, testLibsDir) {
		t.Errorf("LD_LIBRARY_PATH passed to ldd = %q; want leading %q", ldLibPath, testLibsDir)
	}
}

// TestMySQLParseLdconfigCache verifies the `ldconfig -p` parser: the first entry
// for a soname wins, and lines without an absolute path are ignored.
func TestMySQLParseLdconfigCache(t *testing.T) {
	const out = "323 libs found in cache `/etc/ld.so.cache'\n" +
		"\tlibaio.so.1t64 (libc6,x86-64) => /lib/x86_64-linux-gnu/libaio.so.1t64\n" +
		"\tlibaio.so.1t64 (libc6) => /usr/lib/i386-linux-gnu/libaio.so.1t64\n" +
		"\tlibnuma.so.1 (libc6,x86-64) => /lib/x86_64-linux-gnu/libnuma.so.1\n" +
		"\tbroken.so.1 (libc6,x86-64) => relative/path.so\n"
	cache := parseLdconfigCache(out)
	if got, want := cache["libaio.so.1t64"], "/lib/x86_64-linux-gnu/libaio.so.1t64"; got != want {
		t.Errorf("libaio.so.1t64 = %q; want %q (first entry wins)", got, want)
	}
	if got, want := cache["libnuma.so.1"], "/lib/x86_64-linux-gnu/libnuma.so.1"; got != want {
		t.Errorf("libnuma.so.1 = %q; want %q", got, want)
	}
	if _, ok := cache["broken.so.1"]; ok {
		t.Error("entry with a relative path should be ignored")
	}
	if n := len(cache); n != 2 {
		t.Errorf("cache has %d entries; want 2", n)
	}
}

// TestMySQLLinkSonameCompat verifies that a SONAME ldd cannot resolve is
// symlinked into lib/private (the RUNPATH directory) when ldconfig knows a t64
// library by the same name, and that a SONAME with no t64 counterpart is left
// alone for mysqlCheckLDD to report.
func TestMySQLLinkSonameCompat(t *testing.T) {
	tmp := t.TempDir()
	shims := filepath.Join(tmp, "shims")
	if err := os.MkdirAll(shims, 0o755); err != nil {
		t.Fatal(err)
	}

	// The library the loader would find under its renamed soname.
	sysLib := filepath.Join(tmp, "lib", "libaio.so.1t64")
	if err := os.MkdirAll(filepath.Dir(sysLib), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sysLib, []byte{0x7f, 'E', 'L', 'F'}, 0o644); err != nil {
		t.Fatal(err)
	}

	// Fake ldd: libaio.so.1 has a t64 counterpart, libmystery.so.9 does not.
	fakeLDD := "#!/bin/sh\n" +
		"echo '\tlibaio.so.1 => not found'\n" +
		"echo '\tlibmystery.so.9 => not found'\n"
	if err := os.WriteFile(filepath.Join(shims, "ldd"), []byte(fakeLDD), 0o755); err != nil {
		t.Fatal(err)
	}
	fakeLdconfig := fmt.Sprintf("#!/bin/sh\necho '\tlibaio.so.1t64 (libc6,x86-64) => %s'\n", sysLib)
	if err := os.WriteFile(filepath.Join(shims, "ldconfig"), []byte(fakeLdconfig), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shims)
	t.Setenv("AF_DB_MYSQL_LIBS", "")

	distDir := filepath.Join(tmp, "dist")
	if err := os.MkdirAll(filepath.Join(distDir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"mysqld", "mysql"} {
		if err := os.WriteFile(filepath.Join(distDir, "bin", name), []byte{0x7f, 'E', 'L', 'F'}, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	msgs := mysqlLinkSonameCompat(distDir)
	if len(msgs) != 1 {
		t.Fatalf("messages = %v; want exactly one (libaio.so.1, linked once for two binaries)", msgs)
	}
	if !strings.Contains(msgs[0], "libaio.so.1") || !strings.Contains(msgs[0], sysLib) {
		t.Errorf("message should name the soname and the target\ngot: %s", msgs[0])
	}

	link := filepath.Join(distDir, "lib", "private", "libaio.so.1")
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("expected a symlink at %s: %v", link, err)
	}
	if target != sysLib {
		t.Errorf("symlink target = %q; want %q", target, sysLib)
	}
	if _, err := os.Lstat(filepath.Join(distDir, "lib", "private", "libmystery.so.9")); err == nil {
		t.Error("a soname with no t64 counterpart should not be linked")
	}
}

// TestMySQLLDDNotInPath verifies that mysqlCheckLDD returns a plain error (not
// *mysqlLDDError) when ldd itself is not in PATH.
func TestMySQLLDDNotInPath(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no ldd here
	mysqldPath := filepath.Join(t.TempDir(), "mysqld")
	if err := os.WriteFile(mysqldPath, []byte{0x7f, 'E', 'L', 'F'}, 0o755); err != nil {
		t.Fatal(err)
	}
	err := mysqlCheckLDD(mysqldPath)
	if err == nil {
		t.Fatal("expected error when ldd not in PATH")
	}
	if _, ok := err.(*mysqlLDDError); ok {
		t.Errorf("expected plain error (not *mysqlLDDError) when ldd absent, got *mysqlLDDError: %v", err)
	}
	if !strings.Contains(err.Error(), "ldd") {
		t.Errorf("error should mention ldd\ngot: %s", err.Error())
	}
}
