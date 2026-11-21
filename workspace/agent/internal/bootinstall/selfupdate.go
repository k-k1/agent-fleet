package bootinstall

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/installx"
)

// The self-update opt-in's "latest" installs (AF_AGENT_SELF_UPDATE_ALLOWED and
// AF_AGENT_SELF_UPDATE both 1). The entrypoint decides whether to run it (opt-in, unattended
// start, free space) and calls `boot-install self-update`; each CLI here follows its own
// distribution and a failure of one never stops the others or the start: the installed copy
// stays. No status line is written: Settings > Toolchains reports the pinned boot-install, not
// this opt-in.

const (
	rtkLatestPage     = "https://github.com/rtk-ai/rtk/releases/latest"
	rtkLatestDownload = "https://github.com/rtk-ai/rtk/releases/latest/download"
	agyManifestFmt    = "https://antigravity-cli-auto-updater-974169037036.us-central1.run.app/manifests/linux_%s.json"
	agyInstallScript  = "https://antigravity.google/cli/install.sh"
	cursorInstallURL  = "https://cursor.com/install"
	// installScriptTimeout bounds one upstream install script (it downloads the CLI itself).
	installScriptTimeout = 10 * time.Minute
	// latestLookupTimeout bounds the small "what is latest" requests.
	latestLookupTimeout = 15 * time.Second
)

var (
	reCursorLatest = regexp.MustCompile(`lab/([0-9][0-9.]*-[a-f0-9]+)`)
	reRTKTag       = regexp.MustCompile(`/tag/v(.*)$`)
)

// SelfUpdate moves the four npm CLIs, agy, rtk and cursor to their latest releases under
// ~/.local, where each shadows a baked copy on PATH. Always returns 0: every step is soft.
func SelfUpdate(ctx context.Context, e Env) int {
	e.selfUpdateNPM()
	e.selfUpdateAGY(ctx)
	e.selfUpdateRTK(ctx)
	e.selfUpdateCursor(ctx)
	return ExitOK
}

func (e Env) urls() (rtkPage, rtkDL, agyManifest, agyScript, cursorScript string) {
	pick := func(v, def string) string {
		if v != "" {
			return v
		}
		return def
	}
	arch := e.Arch
	return pick(e.RTKLatestPage, rtkLatestPage), pick(e.RTKLatestDownload, rtkLatestDownload),
		pick(e.AgyManifestURL, fmt.Sprintf(agyManifestFmt, arch)), pick(e.AgyInstallURL, agyInstallScript),
		pick(e.CursorInstallURL, cursorInstallURL)
}

// fetchText GETs a small text document ("" on any failure: latest is then unknown and the
// caller updates, as the shell did).
func (e Env) fetchText(ctx context.Context, u string) string {
	ctx, cancel := context.WithTimeout(ctx, latestLookupTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return ""
	}
	resp, err := e.Client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return string(b)
}

// runScript fetches an upstream install script and runs it with bash under a deadline (the
// whole group is killed at it). Output is dropped, as the shell's `>/dev/null 2>&1` did.
func (e Env) runScript(ctx context.Context, url string, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, installScriptTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := e.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %w", url, httpStatusError(resp.StatusCode))
	}
	script, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "bash", append([]string{"-s", "--"}, args...)...)
	cmd.Stdin = bytes.NewReader(script)
	cmd.Env = append(os.Environ(), "HOME="+e.Home)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	return cmd.Run()
}

// pathVersion is the first line of `<name> <args>` as PATH resolves name ("" when it is not
// there or does not answer): the shadow or the baked copy, whichever a session would run.
func (e Env) pathVersion(name string, args ...string) string {
	p, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	out, _, err := e.probeArgs(p, args, nil)
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(out, "\n")
	return strings.TrimSpace(line)
}

func (e Env) selfUpdateNPM() {
	prefixed := e.npmVersions()
	need := false
	for _, c := range npmCLIs {
		latest := e.npmLatest(c.Pkg)
		if cur := prefixed[c.Pkg]; cur == "" || latest == "" || cur != latest {
			need = true
			break
		}
	}
	if !need {
		fmt.Fprintln(e.Stdout, "[entrypoint] agent CLIs already latest; skip")
		return
	}
	specs := make([]string, 0, len(npmCLIs))
	names := make([]string, 0, len(npmCLIs))
	for _, c := range npmCLIs {
		specs = append(specs, c.Pkg+"@latest")
		names = append(names, c.Name)
	}
	if stderr, err := e.npmInstall(specs); err != nil {
		fmt.Fprint(e.Stderr, stderr)
		if usable(filepath.Join(e.bakedBin(), "opencode")) {
			fmt.Fprintln(e.Stdout, "[entrypoint] WARN: agent CLI update failed (using baked versions)")
		} else {
			fmt.Fprintln(e.Stdout, "[entrypoint] WARN: agent CLI update failed (kept the installed versions)")
		}
	} else {
		fmt.Fprintln(e.Stdout, "[entrypoint] agent CLIs updated (~/.local)")
	}
	// A failed install can leave a half-replaced tree, or none, so check either way.
	e.NPMVerify(names, false)
}

func (e Env) npmLatest(pkg string) string {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, e.npmBin(), "view", pkg, "version")
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (e Env) selfUpdateAGY(ctx context.Context) {
	_, _, manifest, script, _ := e.urls()
	var m struct {
		Version string `json:"version"`
	}
	_ = json.Unmarshal([]byte(e.fetchText(ctx, manifest)), &m)
	latest := m.Version
	bin := filepath.Join(e.localBin(), "agy")
	marker := filepath.Join(e.localBin(), agyMarker)
	// Skip when unchanged: the binary is asked (the marker alone sticks while agy's own
	// self-update moves the binary, and goes stale the other way).
	if latest != "" && usable(bin) && e.AgyEffectiveVersion() == latest {
		fmt.Fprintf(e.Stdout, "[entrypoint] agy already latest (%s); skip\n", latest)
		return
	}
	prev := "baked"
	if usable(bin) {
		prev = "previous"
	}
	// install.sh exits 0 without updating when a binary already exists, so it installs into an
	// empty temp dir that is then swapped in: the previous shadow survives a failure.
	tmp, err := os.MkdirTemp("", "af-agy-up-")
	if err != nil {
		fmt.Fprintf(e.Stdout, "[entrypoint] WARN: agy update failed (using %s version)\n", prev)
		return
	}
	defer os.RemoveAll(tmp)
	cand := filepath.Join(tmp, "agy")
	if err := e.runScript(ctx, script, "--dir", tmp); err != nil || !usable(cand) {
		fmt.Fprintf(e.Stdout, "[entrypoint] WARN: agy update failed (using %s version)\n", prev)
		return
	}
	has := func(out string) bool { return agyParseVersion(out) != "" }
	if err := e.installAtomicWith(cand, bin, e.agyEnv(), has); err != nil {
		fmt.Fprintln(e.Stderr, err)
		fmt.Fprintln(e.Stdout, "[entrypoint] WARN: agy update failed (install, using previous version)")
		return
	}
	if latest == "" || os.WriteFile(marker, []byte(latest+"\n"), 0o644) != nil {
		os.Remove(marker)
		latest = "latest"
	}
	fmt.Fprintf(e.Stdout, "[entrypoint] agy updated: %s\n", latest)
}

// rtkLatest is the newest release tag, from the redirect of GitHub's /releases/latest.
func (e Env) rtkLatest(ctx context.Context, page string) string {
	ctx, cancel := context.WithTimeout(ctx, latestLookupTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, page, nil)
	if err != nil {
		return ""
	}
	c := *e.Client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := c.Do(req)
	if err != nil {
		return ""
	}
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	if m := reRTKTag.FindStringSubmatch(loc); m != nil {
		return m[1]
	}
	return ""
}

func (e Env) selfUpdateRTK(ctx context.Context) {
	page, dl, _, _, _ := e.urls()
	latest := e.rtkLatest(ctx, page)
	if cur := rtkVersion(e.pathVersion("rtk", "--version")); latest != "" && cur == latest {
		fmt.Fprintf(e.Stdout, "[entrypoint] rtk already latest (%s); skip\n", cur)
		return
	}
	if err := e.installRTKFrom(ctx, dl); err != nil {
		fmt.Fprintln(e.Stderr, err)
		fmt.Fprintln(e.Stdout, "[entrypoint] WARN: rtk update failed (using baked version)")
		return
	}
	out, _, _ := e.probe(filepath.Join(e.localBin(), "rtk"))
	line, _, _ := strings.Cut(out, "\n")
	fmt.Fprintf(e.Stdout, "[entrypoint] rtk updated: %s\n", line)
}

// installRTKFrom installs the release served under base (<base>/<asset>, <base>/checksums.txt).
func (e Env) installRTKFrom(ctx context.Context, base string) error {
	asset, err := rtkAsset(e.Arch)
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "af-rtk-up-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	archive, sums := filepath.Join(tmp, asset), filepath.Join(tmp, "checksums.txt")
	if err := e.get(ctx, base+"/"+asset, archive); err != nil {
		return err
	}
	if err := e.get(ctx, base+"/checksums.txt", sums); err != nil {
		return err
	}
	want, err := checksumFor(sums, asset)
	if err != nil {
		return err
	}
	if err := installx.VerifySha256(archive, want); err != nil {
		return fmt.Errorf("%w: %v", ErrChecksum, err)
	}
	bin := filepath.Join(tmp, "rtk")
	if err := extractMember(archive, "rtk", bin); err != nil {
		return err
	}
	return e.installAtomic(bin, filepath.Join(e.localBin(), "rtk"))
}

func (e Env) selfUpdateCursor(ctx context.Context) {
	_, _, _, _, script := e.urls()
	// The install script embeds the version it installs; asking the script saves the ~100 MB
	// download when PATH's cursor-agent (shadow or baked) already is it.
	latest := ""
	if m := reCursorLatest.FindStringSubmatch(e.fetchText(ctx, script)); m != nil {
		latest = m[1]
	}
	cur := e.pathVersion("cursor-agent", "--disable-auto-update", "--version")
	if latest != "" && cur == latest {
		fmt.Fprintf(e.Stdout, "[entrypoint] cursor already latest (%s); skip\n", cur)
		return
	}
	link := filepath.Join(e.localBin(), cursorTool)
	if err := e.runScript(ctx, script); err != nil || !usable(link) {
		prev := "baked"
		if _, err := os.Stat(link); err == nil {
			prev = "previous"
		}
		fmt.Fprintf(e.Stdout, "[entrypoint] WARN: cursor update failed (using %s version)\n", prev)
		return
	}
	fmt.Fprintf(e.Stdout, "[entrypoint] cursor updated: %s\n", e.pathVersion("cursor-agent", "--disable-auto-update", "--version"))
}
