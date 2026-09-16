package main

// install_postgres.go — workspace-agent install-postgres [<major>]
//
// Downloads the Zonky embedded-postgres binaries for this container's arch.
// Supported majors: 16, 17, 18 (default 17).
//
// Default major: pinned version from versions.json ("postgres" + "postgres_sha256").
// Other majors: latest release fetched from Maven Central maven-metadata.xml,
// sha256 verified against Maven Central's .sha256 sidecar.
//
// Install path: ~/.local/share/agent-fleet/postgres/<major>/{bin,lib,share}
// AF_DB_POSTGRES_ROOT overrides the per-major dir for tests.
//
// Exit codes: 0 ok, 2 usage, 3 sha mismatch (message names URL + both shas).

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

const pgDefaultMajor = "17"

// zonkyBaseURL is the Maven Central Zonky postgres artifact root.
// Overridable in tests via package variable so httptest servers can be used.
var zonkyBaseURL = "https://repo1.maven.org/maven2/io/zonky/test/postgres"

// zonkyArtifact returns the Maven artifact classifier and txz member name.
// amd64: classifier="linux-amd64", member="postgres-linux-x86_64.txz"
// arm64: classifier="linux-arm64v8", member="postgres-linux-arm_64.txz"
func zonkyArtifact() (classifier, member string, err error) {
	switch runtime.GOARCH {
	case "amd64":
		return "linux-amd64", "postgres-linux-x86_64.txz", nil
	case "arm64":
		return "linux-arm64v8", "postgres-linux-arm_64.txz", nil
	default:
		return "", "", fmt.Errorf("unsupported arch %q", runtime.GOARCH)
	}
}

func zonkyJarURL(classifier, ver string) string {
	art := "embedded-postgres-binaries-" + classifier
	return fmt.Sprintf("%s/%s/%s/%s-%s.jar", zonkyBaseURL, art, ver, art, ver)
}

// pgInstallDir returns the install root for a given major. Respects
// AF_DB_POSTGRES_ROOT (used by tests pointing at the retained af-pgtest dist).
func pgInstallDir(major string) string {
	if r := os.Getenv("AF_DB_POSTGRES_ROOT"); r != "" {
		return r
	}
	return filepath.Join(agentFleetShareDir(), "postgres", major)
}

// pgInstallStaging returns the fixed staging path for a postgres major.
// Same filesystem as agentFleetShareDir() → os.Rename is atomic.
// Wiped at the start of each install run so a SIGKILL leaves at most one
// install-worth of residue rather than accumulating per attempt (kiro's
// kiroInstallStaging follows the same convention).
func pgInstallStaging(major string) string {
	return filepath.Join(agentFleetShareDir(), "pg-"+major+"-install")
}

func pgInstallLockPath(major string) string {
	return filepath.Join(agentFleetShareDir(), ".pg-"+major+"-install.lock")
}

// pgInstallLock takes the exclusive cross-process flock for this major.
// L2 calls workspace-agent install-postgres from subprocesses (af-db url
// ensureInstalled); without this two concurrent callers race on os.RemoveAll
// and os.Rename in doInstallPostgres. Returns an unlock function.
func pgInstallLock(major string) (func(), error) {
	if err := os.MkdirAll(agentFleetShareDir(), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(pgInstallLockPath(major), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		fmt.Fprintf(os.Stderr, "[install-postgres] another install of postgres %s is in progress; waiting ...\n", major)
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

// pgSHAMismatch is returned when the downloaded jar's sha256 does not match
// the expected value. runInstallPostgres exits with code 3 for this error.
type pgSHAMismatch struct{ msg string }

func (e *pgSHAMismatch) Error() string { return e.msg }

func runInstallPostgres(args []string) {
	major := pgDefaultMajor
	if len(args) > 0 && args[0] != "" && !strings.HasPrefix(args[0], "-") {
		major = args[0]
	}
	switch major {
	case "16", "17", "18":
	default:
		fmt.Fprintln(os.Stderr, "usage: workspace-agent install-postgres [16|17|18]")
		os.Exit(2)
	}
	if err := installPostgres(major); err != nil {
		if _, ok := err.(*pgSHAMismatch); ok {
			fmt.Fprintf(os.Stderr, "[install-postgres] %v\n", err)
			os.Exit(3)
		}
		fmt.Fprintf(os.Stderr, "[install-postgres] %v\n", err)
		os.Exit(1)
	}
}

func installPostgres(major string) error {
	classifier, member, err := zonkyArtifact()
	if err != nil {
		return err
	}

	dest := pgInstallDir(major)

	// Fast path (pre-lock): already installed.
	if fileExecutable(filepath.Join(dest, "bin", "initdb")) {
		fmt.Fprintf(os.Stderr, "[install-postgres] postgres %s already installed at %s\n", major, dest)
		return nil
	}

	// Resolve version and sha before taking the lock to avoid holding it
	// during network calls. For the default major the pin avoids any network.
	var ver, sha string
	pins := readBuildPins()
	if major == pgDefaultMajor && pins["postgres"] != "" && pins["postgres_sha256"] != "" {
		ver = pins["postgres"]
		sha = pins["postgres_sha256"]
	} else {
		ver, err = zonkyLatestVersion(classifier, major)
		if err != nil {
			return err
		}
		sha, err = fetchRemoteSHA256(zonkyJarURL(classifier, ver) + ".sha256")
		if err != nil {
			return err
		}
	}

	// Serialise across concurrent installs.
	unlock, err := pgInstallLock(major)
	if err != nil {
		return err
	}
	defer unlock()

	// Re-check under lock: another process may have finished while we waited.
	if fileExecutable(filepath.Join(dest, "bin", "initdb")) {
		fmt.Fprintf(os.Stderr, "[install-postgres] postgres %s already installed at %s (by a concurrent run)\n", major, dest)
		return nil
	}

	return doInstallPostgres(major, classifier, member, ver, sha, dest)
}

func doInstallPostgres(major, classifier, member, ver, sha, dest string) error {
	url := zonkyJarURL(classifier, ver)

	shareDir := agentFleetShareDir()
	if err := os.MkdirAll(shareDir, 0o755); err != nil {
		return err
	}
	// Fixed staging dir wiped at start: a SIGKILL leaves at most one install's
	// worth of residue. defer cleans it on normal or error exit.
	staging := pgInstallStaging(major)
	_ = os.RemoveAll(staging)
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return err
	}
	defer os.RemoveAll(staging)

	jarPath := filepath.Join(staging, "pg.jar")
	fmt.Fprintf(os.Stderr, "[install-postgres] downloading postgres %s (%s) ...\n", ver, url)
	if err := runCmd("curl", "-fsSL", "--retry", "3", "--retry-delay", "2",
		"--retry-connrefused", "-o", jarPath, url); err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}

	// Compute sha256 ourselves so the error message can include both values.
	got, err := fileSHA256(jarPath)
	if err != nil {
		return err
	}
	if got != sha {
		return &pgSHAMismatch{fmt.Sprintf(
			"sha256 mismatch for %s\n  got:  %s\n  want: %s", url, got, sha,
		)}
	}

	// Extract the .txz member from the jar (zip archive).
	jarDir := filepath.Join(staging, "jar")
	if err := os.MkdirAll(jarDir, 0o755); err != nil {
		return err
	}
	if err := runCmd("unzip", "-q", jarPath, member, "-d", jarDir); err != nil {
		return fmt.Errorf("extract jar member %s: %w", member, err)
	}
	_ = os.Remove(jarPath)

	// Extract the .txz; Zonky lays out bin/ lib/ share/ at the archive root.
	txzPath := filepath.Join(jarDir, member)
	distDir := filepath.Join(staging, "dist")
	if err := os.MkdirAll(distDir, 0o755); err != nil {
		return err
	}
	if err := runCmd("tar", "-xJf", txzPath, "-C", distDir); err != nil {
		return fmt.Errorf("extract txz: %w", err)
	}
	_ = os.Remove(txzPath)

	if !fileExecutable(filepath.Join(distDir, "bin", "initdb")) {
		return fmt.Errorf("extracted tree has no bin/initdb")
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	_ = os.RemoveAll(dest)
	if err := os.Rename(distDir, dest); err != nil {
		return err
	}

	out, err := exec.Command(filepath.Join(dest, "bin", "initdb"), "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("initdb --version: %w\n%s", err, string(out))
	}
	fmt.Fprintf(os.Stderr, "[install-postgres] installed at %s: %s\n",
		dest, strings.TrimSpace(string(out)))
	return nil
}

// zonkyLatestVersion fetches maven-metadata.xml and returns the highest plain
// release (no hyphen suffix) for the given major prefix.
func zonkyLatestVersion(classifier, major string) (string, error) {
	art := "embedded-postgres-binaries-" + classifier
	metaURL := fmt.Sprintf("%s/%s/maven-metadata.xml", zonkyBaseURL, art)
	cl := &http.Client{Timeout: 30 * time.Second}
	resp, err := cl.Get(metaURL)
	if err != nil {
		return "", fmt.Errorf("fetch %s: %w", metaURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch %s: HTTP %d", metaURL, resp.StatusCode)
	}
	var meta struct {
		Versioning struct {
			Versions []string `xml:"versions>version"`
		} `xml:"versioning"`
	}
	if err := xml.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&meta); err != nil {
		return "", fmt.Errorf("parse %s: %w", metaURL, err)
	}
	prefix := major + "."
	latest := ""
	for _, v := range meta.Versioning.Versions {
		if !strings.HasPrefix(v, prefix) || strings.Contains(v, "-") {
			continue
		}
		if pgVersionGT(v, latest) {
			latest = v
		}
	}
	if latest == "" {
		return "", fmt.Errorf("no Zonky release for major %s in %s", major, metaURL)
	}
	return latest, nil
}

// pgVersionGT reports whether "X.Y.Z" version string a is greater than b.
func pgVersionGT(a, b string) bool {
	if b == "" {
		return true
	}
	ap := strings.SplitN(a, ".", 3)
	bp := strings.SplitN(b, ".", 3)
	for i := 0; i < 3 && i < len(ap) && i < len(bp); i++ {
		if ai, bi := parseUint(ap[i]), parseUint(bp[i]); ai != bi {
			return ai > bi
		}
	}
	return len(ap) > len(bp)
}

func parseUint(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// fetchRemoteSHA256 fetches a .sha256 sidecar and returns the trimmed hex digest.
func fetchRemoteSHA256(url string) (string, error) {
	cl := &http.Client{Timeout: 15 * time.Second}
	resp, err := cl.Get(url)
	if err != nil {
		return "", fmt.Errorf("fetch sha256 from %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch sha256 from %s: HTTP %d", url, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 256))
	if err != nil {
		return "", fmt.Errorf("read sha256 from %s: %w", url, err)
	}
	return strings.TrimSpace(string(b)), nil
}

// fileSHA256 returns the hex-encoded SHA-256 digest of the file at path.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
