package main

// install_mysql.go — workspace-agent install-mysql [8.4]
//
// Downloads the MySQL official tarball for this container's arch:
//   x86_64: "minimal" tarball (~63 MB compressed, ~446 MB extracted)
//   arm64:  full tarball (~909 MB compressed, 1,742 MB extracted)
//           — extracts only the subset needed for af-db, then strips ELFs.
//
// Supported majors: 8.4 (default).
//
// Install path: ~/.local/share/agent-fleet/mysql/<major>/{bin,lib,share}
// AF_DB_MYSQL_ROOT overrides the per-major dir for tests.
//
// URL order:
//   https://cdn.mysql.com/Downloads/MySQL-<major>/<file>
//   https://cdn.mysql.com/archives/mysql-<major>/<file>
//
// After unpacking, ldd bin/mysqld is run; any "not found" → exit 3.
// AF_DB_MYSQL_LIBS prepends a library directory to LD_LIBRARY_PATH for ldd.
//
// Exit codes: 0 ok, 2 usage, 3 sha mismatch / ldd not-found.

import (
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

const mysqlDefaultMajor = "8.4"

// mysqlCDNBaseURLs are tried in order for the tarball download.
// Downloads/ is tried first; archives/ is the fallback (measured: the 8.4.6
// minimal x86_64 and full arm64 tarballs are both in archives/ only, but the
// order matches the contract so future uploads to Downloads/ work automatically).
var mysqlCDNBaseURLs = []string{
	"https://cdn.mysql.com/Downloads/MySQL-%s/%s",
	"https://cdn.mysql.com/archives/mysql-%s/%s",
}

// mysqlTarballName returns the tarball file name for this arch and version.
// x86_64 uses the "minimal" variant; arm64 uses the full tarball (no minimal exists).
func mysqlTarballName(ver string) (string, error) {
	switch runtime.GOARCH {
	case "amd64":
		return fmt.Sprintf("mysql-%s-linux-glibc2.28-x86_64-minimal.tar.xz", ver), nil
	case "arm64":
		return fmt.Sprintf("mysql-%s-linux-glibc2.28-aarch64.tar.xz", ver), nil
	default:
		return "", fmt.Errorf("unsupported arch %q", runtime.GOARCH)
	}
}

// mysqlInstallDir returns the install root for a given major. Respects
// AF_DB_MYSQL_ROOT (used by tests pointing at a pre-extracted distribution).
func mysqlInstallDir(major string) string {
	if r := os.Getenv("AF_DB_MYSQL_ROOT"); r != "" {
		return r
	}
	return filepath.Join(agentFleetShareDir(), "mysql", major)
}

// mysqlInstallStaging returns the fixed staging path for a mysql major.
// Same filesystem as agentFleetShareDir() → os.Rename is atomic.
// Wiped at the start of each install run (kiro's kiroInstallStaging follows
// the same convention).
func mysqlInstallStaging(major string) string {
	return filepath.Join(agentFleetShareDir(), "mysql-"+major+"-install")
}

func mysqlInstallLockPath(major string) string {
	return filepath.Join(agentFleetShareDir(), ".mysql-"+major+"-install.lock")
}

// mysqlInstallLock takes the exclusive cross-process flock for this major.
// Two concurrent installs would otherwise race on RemoveAll / Rename.
func mysqlInstallLock(major string) (func(), error) {
	if err := os.MkdirAll(agentFleetShareDir(), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(mysqlInstallLockPath(major), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		fmt.Fprintf(os.Stderr, "[install-mysql] another install of mysql %s is in progress; waiting ...\n", major)
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("flock: %w", err)
		}
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

// mysqlSHAMismatch is returned when the tarball's sha256 does not match.
// runInstallMySQL exits with code 3 for this error.
type mysqlSHAMismatch struct{ msg string }

func (e *mysqlSHAMismatch) Error() string { return e.msg }

// mysqlLDDError is returned when ldd finds unresolved libraries.
// runInstallMySQL exits with code 3 for this error.
type mysqlLDDError struct{ msg string }

func (e *mysqlLDDError) Error() string { return e.msg }

func runInstallMySQL(args []string) {
	major := mysqlDefaultMajor
	if len(args) > 0 && args[0] != "" && !strings.HasPrefix(args[0], "-") {
		major = args[0]
	}
	if major != "8.4" {
		fmt.Fprintln(os.Stderr, "usage: workspace-agent install-mysql [8.4]")
		os.Exit(2)
	}
	if err := installMySQL(major); err != nil {
		switch err.(type) {
		case *mysqlSHAMismatch, *mysqlLDDError:
			fmt.Fprintf(os.Stderr, "[install-mysql] %v\n", err)
			os.Exit(3)
		}
		fmt.Fprintf(os.Stderr, "[install-mysql] %v\n", err)
		os.Exit(1)
	}
}

func installMySQL(major string) error {
	pins := readBuildPins()
	ver := pins["mysql"]
	sha := pins["mysql_sha256"]
	if ver == "" {
		ver = "8.4.6"
	}

	dest := mysqlInstallDir(major)

	// Fast path (pre-lock): already installed.
	if fileExecutable(filepath.Join(dest, "bin", "mysqld")) {
		fmt.Fprintf(os.Stderr, "[install-mysql] mysql %s already installed at %s\n", major, dest)
		return nil
	}

	tarball, err := mysqlTarballName(ver)
	if err != nil {
		return err
	}

	// Resolve SHA before taking the lock (no network needed — it's in the pin).
	if sha == "" {
		return fmt.Errorf("no mysql_sha256 pin in versions.json — cannot install safely")
	}

	// Serialise across concurrent installs.
	unlock, err := mysqlInstallLock(major)
	if err != nil {
		return err
	}
	defer unlock()

	// Re-check under lock.
	if fileExecutable(filepath.Join(dest, "bin", "mysqld")) {
		fmt.Fprintf(os.Stderr, "[install-mysql] mysql %s already installed at %s (by a concurrent run)\n", major, dest)
		return nil
	}

	return doInstallMySQL(major, ver, tarball, sha, dest)
}

// mysqlDownloadURL returns the first CDN URL that returns HTTP 200.
// Tries Downloads/ then archives/. If the download is refused (non-2xx),
// the error message names cdn.mysql.com and AF_EGRESS_ALLOWLIST.
func mysqlDownloadURL(major, tarball string) (string, error) {
	cl := &http.Client{Timeout: 10 * time.Second}
	for _, base := range mysqlCDNBaseURLs {
		u := fmt.Sprintf(base, major, tarball)
		resp, err := cl.Head(u)
		if err != nil {
			continue
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return u, nil
		}
	}
	urls := make([]string, len(mysqlCDNBaseURLs))
	for i, base := range mysqlCDNBaseURLs {
		urls[i] = fmt.Sprintf(base, major, tarball)
	}
	return "", fmt.Errorf(
		"mysql %s tarball not found at any URL:\n  %s\n"+
			"cdn.mysql.com may not be in AF_EGRESS_ALLOWLIST; add it or set AF_EGRESS_ALLOWLIST",
		tarball, strings.Join(urls, "\n  "),
	)
}

func doInstallMySQL(major, ver, tarball, sha, dest string) error {
	shareDir := agentFleetShareDir()
	if err := os.MkdirAll(shareDir, 0o755); err != nil {
		return err
	}

	// Fixed staging dir wiped at start; defer cleans it on normal or error exit.
	staging := mysqlInstallStaging(major)
	_ = os.RemoveAll(staging)
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return err
	}
	defer os.RemoveAll(staging)

	url, err := mysqlDownloadURL(major, tarball)
	if err != nil {
		return err
	}

	tarPath := filepath.Join(staging, "mysql.tar.xz")
	fmt.Fprintf(os.Stderr, "[install-mysql] downloading mysql %s (%s) ...\n", ver, url)
	if err := runCmd("curl", "-fsSL", "--retry", "3", "--retry-delay", "2",
		"--retry-connrefused", "-o", tarPath, url); err != nil {
		return fmt.Errorf("download %s: %w\n"+
			"(cdn.mysql.com must be in AF_EGRESS_ALLOWLIST)", url, err)
	}

	got, err := fileSHA256(tarPath)
	if err != nil {
		return err
	}
	if got != sha {
		return &mysqlSHAMismatch{fmt.Sprintf(
			"sha256 mismatch for %s\n  got:  %s\n  want: %s\n(URL: %s)",
			tarball, got, sha, url,
		)}
	}

	distDir := filepath.Join(staging, "dist")
	if err := os.MkdirAll(distDir, 0o755); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "[install-mysql] extracting ...\n")
	if err := mysqlExtract(tarPath, distDir); err != nil {
		return err
	}
	_ = os.Remove(tarPath)

	if runtime.GOARCH == "arm64" {
		if err := mysqlStripELFs(distDir); err != nil {
			return err
		}
	}

	// Check ldd for unresolved libraries before promoting.
	if err := mysqlCheckLDD(filepath.Join(distDir, "bin", "mysqld")); err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	_ = os.RemoveAll(dest)
	if err := os.Rename(distDir, dest); err != nil {
		return err
	}

	out, err := exec.Command(filepath.Join(dest, "bin", "mysqld"), "--version").CombinedOutput()
	if err != nil {
		// mysqld --version exits non-zero on some builds; the output is still useful.
		fmt.Fprintf(os.Stderr, "[install-mysql] mysqld --version: %v\n%s\n", err, string(out))
	} else {
		fmt.Fprintf(os.Stderr, "[install-mysql] installed at %s: %s\n",
			dest, strings.TrimSpace(string(out)))
	}
	return nil
}

// mysqlExtract unpacks the tarball into distDir.
//
// x86_64 (minimal tarball): extracts the entire archive with --strip-components=1.
// arm64  (full tarball):     extracts only the af-db subset with --strip-components=1.
//
// The subset for arm64 is:
//
//	bin/{mysqld,mysql,mysqladmin,mysqldump}
//	lib/private/*.so*
//	lib/plugin/*.so
//	share/
func mysqlExtract(tarPath, distDir string) error {
	if runtime.GOARCH == "arm64" {
		return mysqlExtractSubset(tarPath, distDir)
	}
	return runCmd("tar", "-xJf", tarPath, "--strip-components=1", "-C", distDir)
}

// mysqlExtractSubset extracts only the af-db subset from the arm64 full tarball.
// GNU tar's --wildcards --no-anchored lets us match paths without knowing the
// exact top-level directory name (which embeds the version).
func mysqlExtractSubset(tarPath, distDir string) error {
	return runCmd("tar", "-xJf", tarPath,
		"--strip-components=1", "-C", distDir,
		"--wildcards", "--no-anchored",
		"bin/mysqld", "bin/mysql", "bin/mysqladmin", "bin/mysqldump",
		"lib/private/*.so*",
		"lib/plugin/*.so",
		"share",
	)
}

// mysqlStripELFs strips debug symbols from every ELF file under root.
// On arm64 the full tarball ships 514 MB of debug sections in mysqld alone;
// strip reduces the install to ~200 MB.
// If `strip` is absent, files are left as-is and a warning is printed to stderr.
func mysqlStripELFs(root string) error {
	stripBin, err := exec.LookPath("strip")
	if err != nil {
		fmt.Fprintln(os.Stderr, "[install-mysql] WARN: strip not found; arm64 binaries not stripped (install will be larger)")
		return nil
	}
	var stripped int
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		if !isELFFile(path) {
			return nil
		}
		if err := exec.Command(stripBin, path).Run(); err != nil {
			fmt.Fprintf(os.Stderr, "[install-mysql] WARN: strip %s: %v\n", path, err)
		} else {
			stripped++
		}
		return nil
	})
	if stripped > 0 {
		fmt.Fprintf(os.Stderr, "[install-mysql] stripped %d ELF file(s)\n", stripped)
	}
	return nil
}

// isELFFile reports whether the file at path starts with the ELF magic bytes.
func isELFFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var magic [4]byte
	if _, err := f.Read(magic[:]); err != nil {
		return false
	}
	return magic[0] == 0x7f && magic[1] == 'E' && magic[2] == 'L' && magic[3] == 'F'
}

// mysqlCheckLDD runs ldd on mysqldBin and returns a mysqlLDDError if any
// library is listed as "not found". Prepends AF_DB_MYSQL_LIBS to LD_LIBRARY_PATH
// when set (the documented workaround for images that predate the libaio/libnuma/
// libncurses bake — this container: ~/.local/share/af-dbtest/libs/...).
func mysqlCheckLDD(mysqldBin string) error {
	env := os.Environ()
	if libs := os.Getenv("AF_DB_MYSQL_LIBS"); libs != "" {
		newEnv := make([]string, 0, len(env))
		for _, e := range env {
			if !strings.HasPrefix(e, "LD_LIBRARY_PATH=") {
				newEnv = append(newEnv, e)
			}
		}
		ldPath := libs
		if existing := os.Getenv("LD_LIBRARY_PATH"); existing != "" {
			ldPath = libs + ":" + existing
		}
		env = append(newEnv, "LD_LIBRARY_PATH="+ldPath)
	}

	cmd := exec.Command("ldd", mysqldBin)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		// ldd exits non-zero for statically linked binaries; check the output anyway.
		if !strings.Contains(string(out), "not found") {
			return nil
		}
	}

	var missing []string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "not found") {
			lib := strings.TrimSpace(strings.SplitN(line, "=>", 2)[0])
			missing = append(missing, lib)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return &mysqlLDDError{fmt.Sprintf(
		"ldd %s: unresolved libraries: %s\n"+
			"Add the directory containing these libraries to AF_DB_MYSQL_LIBS\n"+
			"(e.g. AF_DB_MYSQL_LIBS=~/.local/share/af-dbtest/libs/usr/lib/x86_64-linux-gnu)",
		mysqldBin, strings.Join(missing, ", "),
	)}
}
