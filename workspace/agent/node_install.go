package main

// node_install.go — on-demand Node.js installer for 設定 → ツールチェーン.
//
// ## Why this exists
//
// The node picker had exactly the bug jdk_install_http.go was written to fix, and the
// comment there ("Java is the one toolchain the picker could offer without being able
// to deliver") had quietly stopped being true. nodeOptions is a FIXED list — 18/20/22/24
// — not "installed ∪ installable, so a member could pick 24 with only 22 on disk;
// resolvedToolchains() then globbed ~/.nvm/versions/node/v24.* , found nothing, injected
// nothing, and every session kept running the old node. No error, no warning: the
// selection simply did nothing until someone happened to Stop → Start the workspace,
// because the only thing that ever installed node was the entrypoint's `nvm install`.
//
// ## Why a tarball rather than nvm
//
// nvm is a shell function; driving it from the agent would mean spawning bash and
// sourcing nvm.sh. The other on-demand installers (install-go / install-jdk /
// install-chromium) all follow one idiom instead — download → verify sha256 → staging
// dir → atomic rename — and node publishes everything that needs
// (dist/index.json for "latest patch of major", SHASUMS256.txt per release).
//
// The install lands in ~/.nvm/versions/node/v<full>/ , which is nvm's own layout, so
// the entrypoint's nvm, `nvm use`, and nodeBinFor() all see the same tree. Nothing here
// requires nvm to be present, but nothing here breaks it either.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// nvmNodeRoot is where nvm keeps installed versions — the layout this installer
// deliberately matches so the two agree on what is installed.
func nvmNodeRoot() string {
	return filepath.Join(homeDir(), ".nvm", "versions", "node")
}

// nodeDistArch maps GOARCH to the name in nodejs.org's file list.
func nodeDistArch() string {
	if runtime.GOARCH == "arm64" {
		return "arm64"
	}
	return "x64"
}

// installedNodeMajors lists the majors present under the nvm dir, newest-major first.
// Feeds the picker so the Console can say which offered versions are already on disk.
func installedNodeMajors() []string {
	ents, err := os.ReadDir(nvmNodeRoot())
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, e := range ents {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "v") {
			continue
		}
		v := parseDotted(strings.TrimPrefix(e.Name(), "v"))
		if len(v) == 0 {
			continue
		}
		major := fmt.Sprint(v[0])
		if !seen[major] {
			seen[major] = true
			out = append(out, major)
		}
	}
	return out
}

// latestNodePatch resolves a major ("22") to the newest released version ("v22.23.2")
// that actually ships a linux tarball for this architecture.
//
// ⚠️ Compare numerically, for the same reason nodeBinFor does: dist/index.json is
// ordered newest-first today, but relying on that would make this quietly wrong the day
// it is not, and a string compare would put v22.9.0 above v22.23.2.
func latestNodePatch(major string) (string, error) {
	cl := &http.Client{Timeout: 30 * time.Second}
	resp, err := cl.Get("https://nodejs.org/dist/index.json")
	if err != nil {
		return "", fmt.Errorf("fetch node index: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch node index: HTTP %d", resp.StatusCode)
	}
	var releases []struct {
		Version string   `json:"version"`
		Files   []string `json:"files"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&releases); err != nil {
		return "", fmt.Errorf("parse node index: %w", err)
	}
	want := "linux-" + nodeDistArch()
	best, bestVer := "", []int(nil)
	for _, r := range releases {
		v := parseDotted(strings.TrimPrefix(r.Version, "v"))
		if len(v) == 0 || fmt.Sprint(v[0]) != major {
			continue
		}
		hasBuild := false
		for _, f := range r.Files {
			if f == want {
				hasBuild = true
				break
			}
		}
		if !hasBuild {
			continue
		}
		if bestVer == nil || compareDotted(v, bestVer) > 0 {
			best, bestVer = r.Version, v
		}
	}
	if best == "" {
		return "", fmt.Errorf("no linux-%s build published for node %s", nodeDistArch(), major)
	}
	return best, nil
}

// nodeSha256 reads the published checksum for one release file out of that release's
// SHASUMS256.txt (lines are "<sha256>  <filename>").
func nodeSha256(version, fname string) (string, error) {
	cl := &http.Client{Timeout: 30 * time.Second}
	resp, err := cl.Get("https://nodejs.org/dist/" + version + "/SHASUMS256.txt")
	if err != nil {
		return "", fmt.Errorf("fetch node checksums: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch node checksums: HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[1] == fname {
			return f[0], nil
		}
	}
	return "", fmt.Errorf("no checksum for %s in SHASUMS256.txt", fname)
}

// installNode installs the newest patch of `major` into the nvm layout. Idempotent:
// an existing install of that exact patch is left alone.
func installNode(major string) (string, error) {
	version, err := latestNodePatch(major)
	if err != nil {
		return "", err
	}
	root := nvmNodeRoot()
	dest := filepath.Join(root, version)
	if _, err := os.Stat(filepath.Join(dest, "bin", "node")); err == nil {
		fmt.Fprintf(os.Stderr, "[install-node] node %s already installed at %s\n", version, dest)
		return version, nil
	}
	fname := fmt.Sprintf("node-%s-linux-%s.tar.xz", version, nodeDistArch())
	sum, err := nodeSha256(version, fname)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	txz := filepath.Join(root, "."+fname+".part")
	defer os.Remove(txz)
	url := "https://nodejs.org/dist/" + version + "/" + fname
	fmt.Fprintf(os.Stderr, "[install-node] downloading %s ...\n", url)
	if err := runCmd("curl", "-fsSL", "--proto", "=https", "--proto-redir", "=https",
		"--retry", "3", "--retry-delay", "2", "--retry-connrefused", "-o", txz, url); err != nil {
		return "", fmt.Errorf("download: %w", err)
	}
	if err := verifySha256(txz, sum); err != nil {
		return "", err
	}
	staging, err := os.MkdirTemp(root, ".install-"+strings.TrimPrefix(version, "v")+"-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(staging)
	// The tarball's top-level node-<version>-linux-<arch>/ is stripped so bin/ lands
	// directly under staging — that is what nvm's layout expects.
	if err := runCmd("tar", "-xJf", txz, "--strip-components=1", "-C", staging); err != nil {
		return "", fmt.Errorf("extract: %w", err)
	}
	// ⚠️ Verify the extracted tree before swapping it in. A truncated or unexpected
	// tarball would otherwise be renamed into place and only fail when a session tried
	// to run it — the same "installer reports success, the binary cannot start" shape
	// docs/log/70 §70.9.2 recorded for rtk.
	if _, err := os.Stat(filepath.Join(staging, "bin", "node")); err != nil {
		return "", fmt.Errorf("extracted tree has no bin/node")
	}
	if err := os.RemoveAll(dest); err != nil {
		return "", err
	}
	if err := os.Rename(staging, dest); err != nil {
		return "", err
	}
	fmt.Fprintf(os.Stderr, "[install-node] installed at %s\n", dest)
	return version, nil
}

// runInstallNode is `workspace-agent install-node <major>` — the CLI face, used by the
// entrypoint and available in a terminal.
func runInstallNode(args []string) {
	if len(args) < 1 || !majorOnlyRe.MatchString(args[0]) {
		fmt.Fprintln(os.Stderr, "usage: workspace-agent install-node <major>   (e.g. 22)")
		os.Exit(2)
	}
	if _, err := installNode(args[0]); err != nil {
		fmt.Fprintln(os.Stderr, "[install-node]", err)
		os.Exit(1)
	}
}

// --- HTTP face (設定 → ツールチェーン の「導入」ボタン) -------------------------

type nodeInstall struct {
	mu    sync.Mutex
	state string // "" (idle) | "installing" | "done" | "error"
	major string
	err   string
}

var nodeInstaller nodeInstall

func (n *nodeInstall) snapshot() (state, major, errMsg string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	state = n.state
	if state == "" {
		state = "idle"
	}
	return state, n.major, n.err
}

// nodeInstallStatus answers both verbs, so one round trip tells the caller the job
// state AND what is actually on disk (same contract as jdkInstallStatus).
func nodeInstallStatus() map[string]any {
	state, major, errMsg := nodeInstaller.snapshot()
	return map[string]any{
		"state":          state,
		"major":          major,
		"error":          errMsg,
		"node_installed": installedNodeMajors(),
		"node_available": nodeOptions,
	}
}

// handleNodeInstall drives the on-demand node install. POST /env/node-install
// {"major":"22"} starts it; GET reports the state.
func handleNodeInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		httpx.WriteJSON(w, http.StatusOK, nodeInstallStatus())
		return
	}
	var req struct {
		Major string `json:"major"`
	}
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	// The major reaches a URL and a directory name, so accept only the digits the
	// picker offers — never a free-form string.
	if !majorOnlyRe.MatchString(req.Major) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_major", "invalid node major version")
		return
	}
	offered := false
	for _, v := range nodeOptions {
		if v == req.Major {
			offered = true
			break
		}
	}
	if !offered {
		httpx.WriteErr(w, http.StatusBadRequest, "unsupported_major", "node "+req.Major+" is not offered by this workspace")
		return
	}
	nodeInstaller.mu.Lock()
	if nodeInstaller.state == "installing" {
		// One download at a time; a second request reports the running job rather than
		// starting a competing one (they share the nvm dir).
		nodeInstaller.mu.Unlock()
		httpx.WriteJSON(w, http.StatusOK, nodeInstallStatus())
		return
	}
	nodeInstaller.state = "installing"
	nodeInstaller.major = req.Major
	nodeInstaller.err = ""
	nodeInstaller.mu.Unlock()

	major := req.Major
	go func() {
		_, err := installNode(major)
		nodeInstaller.mu.Lock()
		if err != nil {
			nodeInstaller.state, nodeInstaller.err = "error", err.Error()
		} else {
			nodeInstaller.state, nodeInstaller.err = "done", ""
		}
		nodeInstaller.mu.Unlock()
	}()
	httpx.WriteJSON(w, http.StatusOK, nodeInstallStatus())
}
