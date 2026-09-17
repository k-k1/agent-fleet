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
// After unpacking, SONAMEs that Debian's 64-bit time_t transition renamed
// (libaio.so.1 → libaio.so.1t64) are symlinked into lib/private, which is on the
// binaries' RUNPATH; then ldd bin/mysqld is run and any "not found" → exit 3.
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

// mysqlGOARCH mirrors runtime.GOARCH and is overridable in tests to exercise
// the arm64 extraction and strip paths on an x86_64 host.
var mysqlGOARCH = runtime.GOARCH

// mysqlTarballName returns the tarball file name for this arch and version.
// x86_64 uses the "minimal" variant; arm64 uses the full tarball (no minimal exists).
func mysqlTarballName(ver string) (string, error) {
	switch mysqlGOARCH {
	case "amd64":
		return fmt.Sprintf("mysql-%s-linux-glibc2.28-x86_64-minimal.tar.xz", ver), nil
	case "arm64":
		return fmt.Sprintf("mysql-%s-linux-glibc2.28-aarch64.tar.xz", ver), nil
	default:
		return "", fmt.Errorf("unsupported arch %q", mysqlGOARCH)
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
		return fmt.Errorf("no mysql_sha256 pin in versions.json — this image pre-dates the MySQL pin; rebuild the workspace image to install MySQL")
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

	if mysqlGOARCH == "arm64" {
		if err := mysqlStripELFs(distDir); err != nil {
			return err
		}
	}

	// Bridge the SONAMEs Debian's 64-bit time_t transition renamed, then check
	// ldd for what is still unresolved, before promoting.
	for _, link := range mysqlLinkSonameCompat(distDir) {
		fmt.Fprintf(os.Stderr, "[install-mysql] %s\n", link)
	}
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

	vCmd := exec.Command(filepath.Join(dest, "bin", "mysqld"), "--version")
	vCmd.Env = mysqlLibsEnv()
	out, err := vCmd.CombinedOutput()
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
	if mysqlGOARCH == "arm64" {
		return mysqlExtractSubset(tarPath, distDir)
	}
	return runCmd("tar", "-xJf", tarPath, "--strip-components=1", "-C", distDir)
}

// mysqlExtractSubset extracts only the af-db subset from the arm64 full tarball.
// GNU tar's --wildcards --no-anchored matches paths anywhere in the archive without
// knowing the exact top-level directory name (which embeds the version).
//
// Note: --wildcards-match-slash is on by default in GNU tar, so "*.so*" crosses "/".
// That means "lib/plugin/*.so" would include lib/plugin/debug/*.so, which is 30 files
// and 41 MB of debug symbols we don't need. After extraction we remove that subdirectory.
//
// The same crossing is WANTED under lib/private: it pulls in the nested plugin
// directories (sasl2/) that mysqld dlopens for authentication, which a top-level-only
// pattern would leave behind. The measured arm64 result is 147 MB installed, so the
// extra files are not what makes this tree big.
func mysqlExtractSubset(tarPath, distDir string) error {
	if err := runCmd("tar", "-xJf", tarPath,
		"--strip-components=1", "-C", distDir,
		"--wildcards", "--no-anchored",
		"bin/mysqld", "bin/mysql", "bin/mysqladmin", "bin/mysqldump",
		"lib/private/*.so*",
		"lib/private/icudt*l",
		"lib/plugin/*.so",
		"share",
	); err != nil {
		return err
	}
	// Remove the debug plugin directory included transitively by "lib/plugin/*.so".
	_ = os.RemoveAll(filepath.Join(distDir, "lib", "plugin", "debug"))
	return nil
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
		out, err := exec.Command(stripBin, path).CombinedOutput()
		if err != nil {
			firstLine := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
			fmt.Fprintf(os.Stderr, "[install-mysql] WARN: strip %s: %v: %s\n", path, err, firstLine)
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

// mysqlLibsEnv returns os.Environ() with AF_DB_MYSQL_LIBS prepended to
// LD_LIBRARY_PATH when set. Used by mysqlCheckLDD and the post-install
// mysqld --version call so both see the same library path.
func mysqlLibsEnv() []string {
	env := os.Environ()
	libs := os.Getenv("AF_DB_MYSQL_LIBS")
	if libs == "" {
		return env
	}
	filtered := make([]string, 0, len(env))
	for _, e := range env {
		if !strings.HasPrefix(e, "LD_LIBRARY_PATH=") {
			filtered = append(filtered, e)
		}
	}
	ldPath := libs
	if existing := os.Getenv("LD_LIBRARY_PATH"); existing != "" {
		ldPath = libs + ":" + existing
	}
	return append(filtered, "LD_LIBRARY_PATH="+ldPath)
}

// mysqlSonameCompatDir is where the compatibility symlinks below go, relative to
// the install root. It is on the binaries' RUNPATH ($ORIGIN/../lib/private), so
// the loader finds them without anyone setting LD_LIBRARY_PATH at run time.
const mysqlSonameCompatDir = "lib/private"

// mysqlLinkSonameCompat bridges the SONAMEs that Debian's 64-bit time_t
// transition renamed, and returns one message per link it made.
//
// MySQL's binaries ask for libaio.so.1, while trixie's libaio1t64 ships
// libaio.so.1t64 and no compatibility symlink — so ldd reports "not found" even
// on an image that has the package installed. On 64-bit architectures the t64
// library is ABI-identical to the one it replaced, so the symlink is the whole
// fix. Whatever this cannot resolve is left for mysqlCheckLDD to report.
func mysqlLinkSonameCompat(distDir string) []string {
	cache := mysqlLdconfigCache()
	if len(cache) == 0 {
		return nil
	}
	compat := filepath.Join(distDir, filepath.FromSlash(mysqlSonameCompatDir))
	var msgs []string
	linked := map[string]bool{}
	for _, name := range []string{"mysqld", "mysql", "mysqladmin", "mysqldump"} {
		bin := filepath.Join(distDir, "bin", name)
		if _, err := os.Stat(bin); err != nil {
			continue
		}
		missing, err := mysqlMissingLibs(bin)
		if err != nil {
			return msgs
		}
		for _, soname := range missing {
			target, ok := cache[soname+"t64"]
			if !ok || linked[soname] {
				continue
			}
			if err := os.MkdirAll(compat, 0o755); err != nil {
				continue
			}
			link := filepath.Join(compat, soname)
			_ = os.Remove(link)
			if err := os.Symlink(target, link); err != nil {
				continue
			}
			linked[soname] = true
			msgs = append(msgs, fmt.Sprintf("linked %s -> %s (Debian t64 soname)", soname, target))
		}
	}
	return msgs
}

// mysqlLdconfigCache returns the loader cache as soname -> absolute path. An
// empty map means ldconfig is unavailable or said nothing; callers read that as
// "no compatibility link possible", not as an error.
func mysqlLdconfigCache() map[string]string {
	for _, prog := range []string{"ldconfig", "/sbin/ldconfig", "/usr/sbin/ldconfig"} {
		out, err := exec.Command(prog, "-p").Output()
		if err == nil {
			return parseLdconfigCache(string(out))
		}
	}
	return nil
}

// parseLdconfigCache parses `ldconfig -p` lines of the form
//
//	libaio.so.1t64 (libc6,x86-64) => /lib/x86_64-linux-gnu/libaio.so.1t64
func parseLdconfigCache(out string) map[string]string {
	cache := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		name, path, ok := strings.Cut(strings.TrimSpace(line), " => ")
		if !ok {
			continue
		}
		if i := strings.Index(name, " ("); i >= 0 {
			name = name[:i]
		}
		name, path = strings.TrimSpace(name), strings.TrimSpace(path)
		if name == "" || !strings.HasPrefix(path, "/") {
			continue
		}
		if _, dup := cache[name]; !dup {
			cache[name] = path
		}
	}
	return cache
}

// mysqlMissingLibs runs ldd on bin and returns the SONAMEs it reports as
// "not found". AF_DB_MYSQL_LIBS is prepended to LD_LIBRARY_PATH when set (the
// escape hatch for an image whose libraries live somewhere else).
func mysqlMissingLibs(bin string) ([]string, error) {
	cmd := exec.Command("ldd", bin)
	cmd.Env = mysqlLibsEnv()
	out, cmdErr := cmd.CombinedOutput()
	if cmdErr != nil {
		if _, ok := cmdErr.(*exec.ExitError); !ok {
			// ldd itself could not be executed (not in PATH or permission denied).
			return nil, fmt.Errorf("ldd not found in PATH; cannot verify shared libraries for %s: %w", bin, cmdErr)
		}
		// ldd exits non-zero for statically linked binaries; the output still tells us.
	}
	var missing []string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "not found") {
			missing = append(missing, strings.TrimSpace(strings.SplitN(line, "=>", 2)[0]))
		}
	}
	return missing, nil
}

// mysqlCheckLDD returns a mysqlLDDError when mysqldBin still has unresolved
// libraries after mysqlLinkSonameCompat has done what it can.
func mysqlCheckLDD(mysqldBin string) error {
	missing, err := mysqlMissingLibs(mysqldBin)
	if err != nil {
		return err
	}
	if len(missing) == 0 {
		return nil
	}
	return &mysqlLDDError{fmt.Sprintf(
		"ldd %s: unresolved libraries: %s\n"+
			"No t64-renamed library answered for these either.\n"+
			"Add the directory containing them to AF_DB_MYSQL_LIBS\n"+
			"(e.g. AF_DB_MYSQL_LIBS=~/.local/share/af-dbtest/libs/usr/lib/x86_64-linux-gnu)",
		mysqldBin, strings.Join(missing, ", "),
	)}
}
