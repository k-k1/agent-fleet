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
// AF_DB_POSTGRES_ROOT overrides the per-major dir root for tests.
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
	"time"
)

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

const pgDefaultMajor = "17"

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
	return fmt.Sprintf(
		"https://repo1.maven.org/maven2/io/zonky/test/postgres/%s/%s/%s-%s.jar",
		art, ver, art, ver,
	)
}

// pgInstallDir returns the install root for a given major. Respects
// AF_DB_POSTGRES_ROOT (used by tests pointing at the retained af-pgtest dist).
func pgInstallDir(major string) string {
	if r := os.Getenv("AF_DB_POSTGRES_ROOT"); r != "" {
		return r
	}
	return filepath.Join(agentFleetShareDir(), "postgres", major)
}

// pgSHAMismatch is returned on sha256 verification failure so the caller
// can exit with code 3.
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
	if fileExecutable(filepath.Join(dest, "bin", "initdb")) {
		fmt.Fprintf(os.Stderr, "[install-postgres] postgres %s already installed at %s\n", major, dest)
		return nil
	}

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
		url := zonkyJarURL(classifier, ver)
		sha, err = fetchRemoteSHA256(url + ".sha256")
		if err != nil {
			return err
		}
	}

	return doInstallPostgres(major, classifier, member, ver, sha, dest)
}

func doInstallPostgres(major, classifier, member, ver, sha, dest string) error {
	url := zonkyJarURL(classifier, ver)

	shareDir := agentFleetShareDir()
	if err := os.MkdirAll(shareDir, 0o755); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(shareDir, ".pg-"+major+"-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)

	jarPath := filepath.Join(staging, "pg.jar")
	fmt.Fprintf(os.Stderr, "[install-postgres] downloading postgres %s (%s) ...\n", ver, url)
	if err := runCmd("curl", "-fsSL", "--retry", "3", "--retry-delay", "2",
		"--retry-connrefused", "-o", jarPath, url); err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}

	// Compute sha256 ourselves so we can include both values in the error.
	got, err := fileSHA256(jarPath)
	if err != nil {
		return err
	}
	if got != sha {
		return &pgSHAMismatch{fmt.Sprintf(
			"sha256 mismatch for %s\n  got:  %s\n  want: %s", url, got, sha,
		)}
	}

	// Extract the .txz member from the jar (which is a zip).
	jarDir := filepath.Join(staging, "jar")
	if err := os.MkdirAll(jarDir, 0o755); err != nil {
		return err
	}
	if err := runCmd("unzip", "-q", jarPath, member, "-d", jarDir); err != nil {
		return fmt.Errorf("extract jar member %s: %w", member, err)
	}
	_ = os.Remove(jarPath)

	// Extract the .txz into dist dir; the txz lays out bin/, lib/, share/ at root.
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

// zonkyLatestVersion fetches maven-metadata.xml and returns the highest release
// version whose major prefix matches (e.g. "17.11.0" for major "17").
// Hyphenated builds (e.g. "17.6.0-1") are skipped in favour of plain releases.
func zonkyLatestVersion(classifier, major string) (string, error) {
	art := "embedded-postgres-binaries-" + classifier
	metaURL := fmt.Sprintf(
		"https://repo1.maven.org/maven2/io/zonky/test/postgres/%s/maven-metadata.xml", art,
	)
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
			continue // skip other majors and hyphenated builds
		}
		if pgVersionGT(v, latest) {
			latest = v
		}
	}
	if latest == "" {
		return "", fmt.Errorf("no Zonky release version for major %s in %s", major, metaURL)
	}
	return latest, nil
}

// pgVersionGT reports whether version string a is greater than b (both "X.Y.Z").
func pgVersionGT(a, b string) bool {
	if b == "" {
		return true
	}
	aParts := strings.SplitN(a, ".", 3)
	bParts := strings.SplitN(b, ".", 3)
	for i := 0; i < 3 && i < len(aParts) && i < len(bParts); i++ {
		ai := parseUint(aParts[i])
		bi := parseUint(bParts[i])
		if ai != bi {
			return ai > bi
		}
	}
	return len(aParts) > len(bParts)
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

// fetchRemoteSHA256 fetches a .sha256 sidecar URL and returns the trimmed hex digest.
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
