package main

// install_tools.go — on-demand pinned installers (docs/log/35 §35.7.2 items 4-6).
// The lean rootfs (BAKE_OPTIONAL_TOOLS=0) ships without chromium, Go, the AWS
// CLI + Session Manager plugin and the ops MCP binaries; each is installed into
// the persistent home on first use, pinned by /usr/local/share/agent-fleet/
// versions.json (the same "version we verified" contract as the baked images).
// All installers follow the install-jdk idiom: download → staging dir → atomic
// rename, so a failed download never corrupts an existing install.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"time"
)

// agentFleetShareDir is the per-user root for on-demand tool installs
// (~/.local/share/agent-fleet — home volume, persists across restarts, shared
// between the docker and native runtimes).
func agentFleetShareDir() string {
	return filepath.Join(homeDir(), ".local", "share", "agent-fleet")
}

// --- chromium (browser pane) -------------------------------------------------

// chromiumCFTBases serve linux-x64 chromium as version-immutable Chrome for
// Testing zips, keyed by browser version (versions.json `chromium_cft`).
// playwright 1.61 moved x64 off builds/chromium (which now only carries arm64)
// — the P4 gate found the old x64 URL 404s on the bucket and 400s via PRSS
// (docs/log/35 §35.9-7(a)). The two entry points are genuinely independent
// (playwright's Azure edge vs Google's official CfT bucket).
var chromiumCFTBases = []string{
	"https://cdn.playwright.dev/builds/cft/%s/linux64/chrome-linux64.zip",
	"https://storage.googleapis.com/chrome-for-testing-public/%s/linux64/chrome-linux64.zip",
}

// chromiumDLBases are the playwright CDN mirrors for the legacy builds/chromium
// layout, still the supply for linux-arm64 (versions.json `chromium_dl` build
// number). All dbazure entrances converge on PRSS, so the bucket-direct URL is
// kept last as the one truly distinct fallback.
var chromiumDLBases = []string{
	"https://cdn.playwright.dev/dbazure/download/playwright/builds/chromium/%s/%s",
	"https://playwright.download.prss.microsoft.com/dbazure/download/playwright/builds/chromium/%s/%s",
	"https://cdn.playwright.dev/builds/chromium/%s/%s",
}

// notoCJKURL is the pinned Noto Sans CJK variable-font OTC (Japanese coverage for
// the browser pane) served from the notofonts/noto-cjk repo at an immutable tag.
const notoCJKURL = "https://raw.githubusercontent.com/notofonts/noto-cjk/%s/Sans/Variable/OTC/NotoSansCJK-VF.otf.ttc"

// chromiumPinRoot returns the install dir for one pinned chromium build. Distinct
// from ~/.cache/ms-playwright on purpose: a user-managed playwright must never
// mix with (or be mistaken for) the pane's verified build.
func chromiumPinRoot(pin string) string {
	return filepath.Join(agentFleetShareDir(), "chromium", pin)
}

// chromiumZipSubdirs are the top-level dirs the supported zips extract to:
// chrome-linux64 (Chrome for Testing, x64) and the legacy playwright layouts.
var chromiumZipSubdirs = []string{"chrome-linux64", "chrome-linux", "chrome-linux-arm64"}

// chromeUnderDir returns the chrome executable under one pin dir, or "".
func chromeUnderDir(dir string) string {
	for _, sub := range chromiumZipSubdirs {
		p := filepath.Join(dir, sub, "chrome")
		if st, err := os.Stat(p); err == nil && st.Mode()&0111 != 0 {
			return p
		}
	}
	return ""
}

// chromiumDefaultPin picks the pin for this arch: x64 downloads Chrome for
// Testing by browser version, arm64 the legacy playwright build number.
func chromiumDefaultPin() string {
	pins := readBuildPins()
	if runtime.GOARCH == "arm64" {
		return pins["chromium_dl"]
	}
	return pins["chromium_cft"]
}

// chromiumPinnedBinary resolves the pane's pinned chromium executable, or ""
// when not installed. With no pin recorded (dev build without versions.json) the
// newest installed build is used.
func chromiumPinnedBinary() string {
	root := filepath.Join(agentFleetShareDir(), "chromium")
	dirs := []string{}
	pins := readBuildPins()
	for _, key := range []string{"chromium_cft", "chromium_dl"} {
		if pin := pins[key]; pin != "" {
			dirs = append(dirs, filepath.Join(root, pin))
		}
	}
	if len(dirs) == 0 {
		if m, _ := filepath.Glob(filepath.Join(root, "*")); len(m) > 0 {
			sort.Strings(m)
			for i := len(m) - 1; i >= 0; i-- {
				dirs = append(dirs, m[i])
			}
		}
	}
	for _, dir := range dirs {
		if p := chromeUnderDir(dir); p != "" {
			return p
		}
	}
	return ""
}

// runInstallChromium handles `workspace-agent install-chromium [<pin>]`:
// downloads the pinned chromium build (x64 = Chrome for Testing browser
// version, arm64 = playwright build number) into the per-user pin dir and
// installs the pinned Noto CJK font into ~/.local/share/fonts (fontconfig user
// dir). Font failure is a warning — the pane works, CJK rendering degrades.
func runInstallChromium(args []string) {
	pin := chromiumDefaultPin()
	if len(args) > 0 && args[0] != "" {
		pin = args[0]
	}
	if pin == "" {
		fmt.Fprintln(os.Stderr, "[install-chromium] no chromium pin in versions.json (pass a version explicitly)")
		os.Exit(2)
	}
	if err := installChromium(pin); err != nil {
		fmt.Fprintf(os.Stderr, "[install-chromium] %v\n", err)
		os.Exit(1)
	}
}

func installChromium(pin string) error {
	dest := chromiumPinRoot(pin)
	if chromeUnderDir(dest) != "" {
		fmt.Fprintf(os.Stderr, "[install-chromium] build %s already installed at %s\n", pin, dest)
		installNotoCJK() // font may still be missing (e.g. interrupted earlier run)
		return nil
	}
	// x64 = Chrome for Testing keyed by browser version; arm64 = legacy
	// playwright builds/chromium keyed by build number (see the base lists).
	var urls []string
	if runtime.GOARCH == "arm64" {
		for _, base := range chromiumDLBases {
			urls = append(urls, fmt.Sprintf(base, pin, "chromium-linux-arm64.zip"))
		}
	} else {
		for _, base := range chromiumCFTBases {
			urls = append(urls, fmt.Sprintf(base, pin))
		}
	}
	root := filepath.Dir(dest)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	zipPath := filepath.Join(root, ".dl-"+pin+".zip")
	defer os.Remove(zipPath)
	var lastErr error
	ok := false
	for _, url := range urls {
		fmt.Fprintf(os.Stderr, "[install-chromium] downloading build %s (%s) ...\n", pin, url)
		if err := runCmd("curl", "-fsSL", "--proto", "=https", "--proto-redir", "=https", "--retry", "3", "--retry-delay", "2", "--retry-connrefused", "-o", zipPath, url); err != nil {
			lastErr = fmt.Errorf("download %s: %w", url, err)
			continue
		}
		ok = true
		break
	}
	if !ok {
		return lastErr
	}
	staging, err := os.MkdirTemp(root, ".install-"+pin+"-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	if err := runCmd("unzip", "-q", zipPath, "-d", staging); err != nil {
		return fmt.Errorf("extract: %w", err)
	}
	// The zip lays out chrome-linux[-arm64]/chrome; require the binary before the
	// atomic swap so a truncated archive never becomes the pin dir.
	if chromeUnderDir(staging) == "" {
		return fmt.Errorf("extracted archive has no chrome binary")
	}
	if err := os.RemoveAll(dest); err != nil {
		return err
	}
	if err := os.Rename(staging, dest); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "[install-chromium] installed build %s at %s\n", pin, dest)
	installNotoCJK()
	return nil
}

// installNotoCJK drops the pinned Noto Sans CJK font into the fontconfig user
// dir. Best-effort: a missing pin or failed download only warns.
func installNotoCJK() {
	pin := readBuildPins()["noto_cjk"]
	if pin == "" {
		return
	}
	fontDir := filepath.Join(homeDir(), ".local", "share", "fonts")
	dest := filepath.Join(fontDir, "NotoSansCJK-VF.otf.ttc")
	if _, err := os.Stat(dest); err == nil {
		return
	}
	if err := os.MkdirAll(fontDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "[install-chromium] WARN: font dir: %v\n", err)
		return
	}
	tmp := dest + ".part"
	defer os.Remove(tmp)
	url := fmt.Sprintf(notoCJKURL, pin)
	fmt.Fprintf(os.Stderr, "[install-chromium] downloading Noto Sans CJK (%s) ...\n", pin)
	if err := runCmd("curl", "-fsSL", "--proto", "=https", "--proto-redir", "=https", "--retry", "3", "--retry-delay", "2", "--retry-connrefused", "-o", tmp, url); err != nil {
		fmt.Fprintf(os.Stderr, "[install-chromium] WARN: CJK font download failed (CJK pages will lack glyphs): %v\n", err)
		return
	}
	if err := os.Rename(tmp, dest); err != nil {
		fmt.Fprintf(os.Stderr, "[install-chromium] WARN: font install: %v\n", err)
		return
	}
	// Refresh the fontconfig cache so the running pane picks the font up without
	// a restart; absent fc-cache the next cold start scans it anyway.
	if _, err := exec.LookPath("fc-cache"); err == nil {
		_ = runCmd("fc-cache", "-f", fontDir)
	}
	fmt.Fprintf(os.Stderr, "[install-chromium] installed Noto Sans CJK -> %s\n", dest)
}

// --- Go toolchain ------------------------------------------------------------

// goHomeRoot is the per-user on-demand Go toolchain root; each version keeps its
// own GOROOT at <root>/<ver> (e.g. .../go/1.26.4/bin/go).
func goHomeRoot() string {
	return filepath.Join(agentFleetShareDir(), "go")
}

// installedGoVersions lists on-demand Go versions present in the home dir.
func installedGoVersions() []string {
	m, _ := filepath.Glob(filepath.Join(goHomeRoot(), "*", "bin", "go"))
	out := make([]string, 0, len(m))
	for _, p := range m {
		out = append(out, filepath.Base(filepath.Dir(filepath.Dir(p))))
	}
	sort.Strings(out)
	return out
}

// goRootFor resolves a selected Go version to a GOROOT: the on-demand dir first,
// then the baked /usr/local/go when its pinned version matches. "" = absent.
func goRootFor(ver string) string {
	if ver == "" || ver == "system" {
		return ""
	}
	if p := filepath.Join(goHomeRoot(), ver); fileExecutable(filepath.Join(p, "bin", "go")) {
		return p
	}
	if readBuildPins()["go"] == ver {
		if st, err := os.Stat("/usr/local/go/bin/go"); err == nil && !st.IsDir() {
			return "/usr/local/go"
		}
	}
	return ""
}

// runInstallGo handles `workspace-agent install-go [<version>]` (default: the
// versions.json pin). The tarball comes from go.dev/dl, which keeps every past
// release at an immutable URL and publishes sha256 sums — verified here.
func runInstallGo(args []string) {
	ver := readBuildPins()["go"]
	if len(args) > 0 && args[0] != "" {
		ver = args[0]
	}
	if ver == "" {
		fmt.Fprintln(os.Stderr, "usage: workspace-agent install-go <version>   (no go pin in versions.json)")
		os.Exit(2)
	}
	if err := installGo(ver); err != nil {
		fmt.Fprintf(os.Stderr, "[install-go] %v\n", err)
		os.Exit(1)
	}
}

func installGo(ver string) error {
	dest := filepath.Join(goHomeRoot(), ver)
	if _, err := os.Stat(filepath.Join(dest, "bin", "go")); err == nil {
		fmt.Fprintf(os.Stderr, "[install-go] go %s already installed at %s\n", ver, dest)
		return nil
	}
	fname := fmt.Sprintf("go%s.linux-%s.tar.gz", ver, runtime.GOARCH)
	sum, err := goDLSha256(ver, fname)
	if err != nil {
		return err
	}
	root := goHomeRoot()
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	tgz := filepath.Join(root, "."+fname+".part")
	defer os.Remove(tgz)
	url := "https://go.dev/dl/" + fname
	fmt.Fprintf(os.Stderr, "[install-go] downloading %s ...\n", url)
	if err := runCmd("curl", "-fsSL", "--proto", "=https", "--proto-redir", "=https", "--retry", "3", "--retry-delay", "2", "--retry-connrefused", "-o", tgz, url); err != nil {
		return fmt.Errorf("download: %w", err)
	}
	if err := verifySha256(tgz, sum); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(root, ".install-"+ver+"-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	// The tarball's top-level go/ dir is stripped so bin/ lands under staging.
	if err := runCmd("tar", "-xzf", tgz, "--strip-components=1", "-C", staging); err != nil {
		return fmt.Errorf("extract: %w", err)
	}
	if _, err := os.Stat(filepath.Join(staging, "bin", "go")); err != nil {
		return fmt.Errorf("extracted tree has no bin/go")
	}
	if err := os.RemoveAll(dest); err != nil {
		return err
	}
	if err := os.Rename(staging, dest); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "[install-go] installed at %s\n", dest)
	return nil
}

// goDLSha256 looks up the published sha256 for one release file via the go.dev
// download manifest (covers every past release).
func goDLSha256(ver, fname string) (string, error) {
	cl := &http.Client{Timeout: 30 * time.Second}
	resp, err := cl.Get("https://go.dev/dl/?mode=json&include=all")
	if err != nil {
		return "", fmt.Errorf("fetch go.dev manifest: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch go.dev manifest: HTTP %d", resp.StatusCode)
	}
	var releases []struct {
		Version string `json:"version"`
		Files   []struct {
			Filename string `json:"filename"`
			Sha256   string `json:"sha256"`
		} `json:"files"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&releases); err != nil {
		return "", fmt.Errorf("parse go.dev manifest: %w", err)
	}
	for _, r := range releases {
		if r.Version != "go"+ver {
			continue
		}
		for _, f := range r.Files {
			if f.Filename == fname && f.Sha256 != "" {
				return f.Sha256, nil
			}
		}
	}
	return "", fmt.Errorf("go %s (%s) not found in go.dev manifest", ver, fname)
}

// --- AWS CLI + Session Manager plugin (ssm sessions) -------------------------

// runInstallAWSCLI handles `workspace-agent install-awscli`: installs the pinned
// AWS CLI v2 (official installer, custom dirs — no root) and the pinned Session
// Manager plugin (deb payload extracted manually — no root) into the home.
// Idempotent per component. Run on demand by the ssm session launch (its pane
// shows this progress) and available directly.
func runInstallAWSCLI(args []string) {
	pins := readBuildPins()
	if err := installAWSCLI(pins["awscli"]); err != nil {
		fmt.Fprintf(os.Stderr, "[install-awscli] %v\n", err)
		os.Exit(1)
	}
	if err := installSMP(pins["session_manager_plugin"]); err != nil {
		fmt.Fprintf(os.Stderr, "[install-awscli] session-manager-plugin: %v\n", err)
		os.Exit(1)
	}
}

func awsArch() (string, error) {
	switch runtime.GOARCH {
	case "amd64":
		return "x86_64", nil
	case "arm64":
		return "aarch64", nil
	default:
		return "", fmt.Errorf("unsupported arch %q", runtime.GOARCH)
	}
}

func installAWSCLI(ver string) error {
	if _, err := exec.LookPath("aws"); err == nil {
		fmt.Fprintln(os.Stderr, "[install-awscli] aws already on PATH; skip")
		return nil
	}
	if ver == "" {
		return fmt.Errorf("no awscli pin in versions.json")
	}
	arch, err := awsArch()
	if err != nil {
		return err
	}
	share := agentFleetShareDir()
	binDir := filepath.Join(homeDir(), ".local", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return err
	}
	staging, err := os.MkdirTemp("", "awscli-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	zipPath := filepath.Join(staging, "awscliv2.zip")
	url := fmt.Sprintf("https://awscli.amazonaws.com/awscli-exe-linux-%s-%s.zip", arch, ver)
	fmt.Fprintf(os.Stderr, "[install-awscli] downloading AWS CLI %s ...\n", ver)
	if err := runCmd("curl", "-fsSL", "--proto", "=https", "--proto-redir", "=https", "--retry", "3", "--retry-delay", "2", "--retry-connrefused", "-o", zipPath, url); err != nil {
		return fmt.Errorf("download: %w", err)
	}
	if err := runCmd("unzip", "-q", zipPath, "-d", staging); err != nil {
		return fmt.Errorf("extract: %w", err)
	}
	// The official installer supports fully unprivileged custom-dir installs;
	// --update makes reruns after a partial install converge.
	if err := runCmd(filepath.Join(staging, "aws", "install"),
		"--install-dir", filepath.Join(share, "aws"), "--bin-dir", binDir, "--update"); err != nil {
		return fmt.Errorf("aws installer: %w", err)
	}
	fmt.Fprintf(os.Stderr, "[install-awscli] installed AWS CLI %s -> %s\n", ver, binDir)
	return nil
}

func installSMP(ver string) error {
	if _, err := exec.LookPath("session-manager-plugin"); err == nil {
		fmt.Fprintln(os.Stderr, "[install-awscli] session-manager-plugin already on PATH; skip")
		return nil
	}
	if ver == "" {
		return fmt.Errorf("no session_manager_plugin pin in versions.json")
	}
	smparch := "64bit"
	if runtime.GOARCH == "arm64" {
		smparch = "arm64"
	}
	binDir := filepath.Join(homeDir(), ".local", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return err
	}
	staging, err := os.MkdirTemp("", "smp-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	deb := filepath.Join(staging, "smp.deb")
	url := fmt.Sprintf("https://s3.amazonaws.com/session-manager-downloads/plugin/%s/ubuntu_%s/session-manager-plugin.deb", ver, smparch)
	fmt.Fprintf(os.Stderr, "[install-awscli] downloading session-manager-plugin %s ...\n", ver)
	if err := runCmd("curl", "-fsSL", "--proto", "=https", "--proto-redir", "=https", "--retry", "3", "--retry-delay", "2", "--retry-connrefused", "-o", deb, url); err != nil {
		return fmt.Errorf("download: %w", err)
	}
	// dpkg-deb -x unpacks the payload without root (no maintainer scripts run —
	// the plugin is a self-contained binary, so none are needed).
	if err := runCmd("dpkg-deb", "-x", deb, staging); err != nil {
		return fmt.Errorf("unpack: %w", err)
	}
	src := filepath.Join(staging, "usr", "local", "sessionmanagerplugin", "bin", "session-manager-plugin")
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("deb payload has no session-manager-plugin binary")
	}
	dest := filepath.Join(binDir, "session-manager-plugin")
	if err := copyFile(src, dest, 0o755); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "[install-awscli] installed session-manager-plugin %s -> %s\n", ver, dest)
	return nil
}

// --- mcp-grafana (ops MCP) ---------------------------------------------------

// installGrafanaMCP downloads the pinned mcp-grafana release into the per-user
// bin (same asset + layout as the Dockerfile bake). Called by mcp-run grafana
// when neither the baked nor a home binary exists (lean rootfs).
func installGrafanaMCP(ver string) (string, error) {
	if ver == "" {
		return "", fmt.Errorf("no mcp_grafana pin in versions.json")
	}
	mgarch := "x86_64"
	if runtime.GOARCH == "arm64" {
		mgarch = "arm64"
	}
	binDir := filepath.Join(agentFleetShareDir(), "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return "", err
	}
	staging, err := os.MkdirTemp("", "mcp-grafana-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(staging)
	tgz := filepath.Join(staging, "mcp-grafana.tgz")
	url := fmt.Sprintf("https://github.com/grafana/mcp-grafana/releases/download/v%s/mcp-grafana_Linux_%s.tar.gz", ver, mgarch)
	fmt.Fprintf(os.Stderr, "[mcp-run grafana] downloading mcp-grafana %s ...\n", ver)
	if err := runCmd("curl", "-fsSL", "--proto", "=https", "--proto-redir", "=https", "--retry", "3", "--retry-delay", "2", "--retry-connrefused", "-o", tgz, url); err != nil {
		return "", fmt.Errorf("download: %w", err)
	}
	if err := runCmd("tar", "-C", staging, "-xzf", tgz, "mcp-grafana"); err != nil {
		return "", fmt.Errorf("extract: %w", err)
	}
	dest := filepath.Join(binDir, "mcp-grafana")
	if err := copyFile(filepath.Join(staging, "mcp-grafana"), dest, 0o755); err != nil {
		return "", err
	}
	fmt.Fprintf(os.Stderr, "[mcp-run grafana] installed -> %s\n", dest)
	return dest, nil
}

// --- shared helpers ----------------------------------------------------------

// fileExecutable reports whether path is an executable regular file.
func fileExecutable(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir() && st.Mode()&0111 != 0
}

// verifySha256 compares a file's digest against the expected hex sum.
func verifySha256(path, want string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != want {
		return fmt.Errorf("sha256 mismatch: got %s, want %s", got, want)
	}
	return nil
}

// copyFile writes src to dest atomically (temp + rename) with the given mode.
func copyFile(src, dest string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dest + ".part"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dest)
}
