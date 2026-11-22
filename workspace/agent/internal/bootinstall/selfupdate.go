package bootinstall

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
// stays. Status lines come only from the npm verify pass (NPMVerify records ok / failed for the
// npm CLIs it checks); agy, rtk and cursor write none. Settings > Toolchains otherwise reports
// the pinned boot-install, not this opt-in.

const (
	rtkLatestPage     = "https://github.com/rtk-ai/rtk/releases/latest"
	rtkLatestDownload = "https://github.com/rtk-ai/rtk/releases/latest/download"
	agyManifestFmt    = "https://antigravity-cli-auto-updater-974169037036.us-central1.run.app/manifests/linux_%s.json"
	agyInstallScript  = "https://antigravity.google/cli/install.sh"
	cursorInstallURL  = "https://cursor.com/install"
	// installScriptTimeout bounds one upstream install script (it downloads the CLI itself).
	installScriptTimeout = 10 * time.Minute
	// maxScriptBytes is the largest install script we accept. A longer body is refused, never cut
	// short: bash would run the truncated prefix as if it were the whole script.
	maxScriptBytes = 4 << 20
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

// ErrScriptTooLarge marks an upstream script longer than maxScriptBytes.
var ErrScriptTooLarge = errors.New("install script exceeds the size limit")

// readCapped reads all of r and fails with ErrScriptTooLarge when it holds more than max bytes.
func readCapped(r io.Reader, max int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, ErrScriptTooLarge
	}
	return b, nil
}

// fetchText GETs a small text document ("" on any failure, an over-long body included: latest is
// then unknown and the caller updates, as the shell did).
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
	b, err := readCapped(resp.Body, maxScriptBytes)
	if err != nil {
		return ""
	}
	return string(b)
}

// runScript runs an upstream install script with HOME = the real home.
func (e Env) runScript(ctx context.Context, url string, args ...string) error {
	return e.runScriptHome(ctx, e.Home, url, args...)
}

// runScriptHome fetches an upstream install script and runs it with bash under a deadline (the
// whole group is killed at it), with HOME=home. Output is dropped, as the shell's `>/dev/null
// 2>&1` did. A script over maxScriptBytes is an error and nothing runs.
func (e Env) runScriptHome(ctx context.Context, home, url string, args ...string) error {
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
	script, err := readCapped(resp.Body, maxScriptBytes)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "bash", append([]string{"-s", "--"}, args...)...)
	cmd.Stdin = bytes.NewReader(script)
	cmd.Env = append(os.Environ(), "HOME="+home)
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
	// Held to the end, and handed to every npm this runs: the writer and the recovery of the next
	// start exclude each other even when only this process dies.
	e, release, ok := e.lockNPM()
	if !ok {
		e.npmBusy("agent CLI update")
		return
	}
	defer release()
	e.npmRecoverAside(true)
	if _, err := os.Lstat(e.npmMarker()); err == nil {
		// Recovery could not settle the earlier update's backups: starting another one would
		// overwrite their record. The installed copies stay.
		fmt.Fprintln(e.Stdout, "[entrypoint] WARN: agent CLI update skipped (an earlier update's backups are not settled yet)")
		return
	}
	prefixed, lsOK := e.npmLs()
	need := !lsOK // an `npm ls` that failed cannot vouch for the tree: update
	for _, c := range npmCLIs {
		if need {
			break
		}
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
	// The installed trees are moved aside first and put back when the install fails or a new copy
	// does not run: an ENOSPC or a kill mid-install must not cost the working copy.
	moved, merr := e.npmMoveAside()
	if merr != nil {
		fmt.Fprintln(e.Stderr, merr)
		fmt.Fprintln(e.Stdout, "[entrypoint] WARN: agent CLI update failed (kept the installed versions)")
		return
	}
	var stuck []npmAside // entries whose previous copy could not be put back
	verify := names      // the CLIs the pin-repairing verify pass may look at
	if stderr, err := e.npmInstall(specs); err != nil {
		fmt.Fprint(e.Stderr, stderr)
		stuck = e.npmRestoreAsideFailed(moved) // only the entries that really stayed behind
		moved = nil
		if usable(filepath.Join(e.bakedBin(), "opencode")) {
			fmt.Fprintln(e.Stdout, "[entrypoint] WARN: agent CLI update failed (using baked versions)")
		} else {
			fmt.Fprintln(e.Stdout, "[entrypoint] WARN: agent CLI update failed (kept the installed versions)")
		}
	} else {
		// Each CLI that had a previous copy is judged once, here: a new copy that runs is accepted
		// and is not handed to the verify pass (which would "repair" it to the pin on one slow
		// probe and cost us the previous copy); one that does not run goes back to the previous
		// copy. Only CLIs without a previous copy are left to the verify pass.
		bad := e.npmNotRunning(moved)
		var keep []npmAside
		hadOld := map[string]bool{}
		for _, m := range moved {
			hadOld[m.cli] = true
			if bad[m.cli] {
				if !e.npmRestoreAside([]npmAside{m}) {
					stuck = append(stuck, m)
				}
				continue
			}
			keep = append(keep, m)
		}
		verify = nil
		for _, c := range names {
			switch {
			case !hadOld[c] || bad[c]:
				verify = append(verify, c)
				if bad[c] {
					fmt.Fprintf(e.Stdout, "[entrypoint] WARN: updated %s does not run; kept the previous copy\n", c)
				}
			default:
				e.note(c, statusOK, "")
			}
		}
		fmt.Fprintln(e.Stdout, "[entrypoint] agent CLIs updated (~/.local)")
		moved = keep
	}
	// A failed install can leave a half-replaced tree, or none, so check what has no previous copy.
	// A CLI whose previous copy could not be put back is waiting for the next start's recovery:
	// the pin repair would delete the old link already restored, so it is not looked at here.
	var held []string
	for _, c := range npmCLINames(stuck) {
		held = append(held, c)
		e.note(c, statusFailed, ReasonOther)
	}
	var check []string
	for _, c := range verify {
		isHeld := false
		for _, h := range held {
			isHeld = isHeld || h == c
		}
		if !isHeld {
			check = append(check, c)
		}
	}
	if len(check) > 0 {
		e.NPMVerify(check, false)
	}
	switch {
	case len(stuck) > 0:
		// A previous copy could not be put back: its backup and the marker stay, so the next
		// start retries. The accepted CLIs' backups go now, or the pending marker's restore would
		// put them over their new copies; the marker lists only the stuck CLIs once they are gone.
		e.npmNarrowMarker(npmCLINames(stuck), npmCLINames(moved))
	case len(moved) > 0:
		// Accepted: the marker says so, and the backups stay until the start's final verify pass
		// (`npm-verify --present`) has looked at the new copies once more; it, or the next
		// start's recovery, deletes them. A kill in between cannot restore them over the new copies.
		_ = e.npmWriteMarker("accepted", npmCLINames(moved))
	default:
		os.Remove(e.npmMarker())
	}
}

// npmNotRunning is the set of CLIs among the moved entries whose ~/.local copy does not answer
// --version; each CLI is asked once, however many of its paths were moved.
func (e Env) npmNotRunning(moved []npmAside) map[string]bool {
	asked, bad := map[string]bool{}, map[string]bool{}
	for _, m := range moved {
		if asked[m.cli] {
			continue
		}
		asked[m.cli] = true
		if !e.runs(filepath.Join(e.localBin(), m.cli)) {
			bad[m.cli] = true
		}
	}
	return bad
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
	release, ok, lockErr := e.lockCursor()
	if !ok {
		if lockErr != nil {
			fmt.Fprintf(e.Stdout, "[entrypoint] WARN: cursor update skipped (cannot open %s: %v)\n", e.cursorLockPath(), lockErr)
			return
		}
		fmt.Fprintln(e.Stdout, "[entrypoint] WARN: cursor update skipped (another cursor update is running)")
		return
	}
	defer release()
	// Before the latest check: a killed update's half-switched state would read as current.
	if err := e.cursorRecover(); err != nil {
		fmt.Fprintln(e.Stderr, err)
		fmt.Fprintln(e.Stdout, "[entrypoint] WARN: cursor update skipped (an earlier update could not be rolled back)")
		return
	}
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
	if err := e.installCursorLatest(ctx, script); errors.Is(err, errCursorUnsettled) {
		fmt.Fprintln(e.Stderr, err)
		fmt.Fprintf(e.Stdout, "[entrypoint] WARN: cursor update verified at %s but not settled (the next start decides)\n", link)
		return
	} else if err != nil {
		fmt.Fprintln(e.Stderr, err)
		prev := "baked"
		if _, err := os.Stat(link); err == nil {
			prev = "previous"
		}
		fmt.Fprintf(e.Stdout, "[entrypoint] WARN: cursor update failed (using %s version)\n", prev)
		return
	}
	fmt.Fprintf(e.Stdout, "[entrypoint] cursor updated: %s\n", e.pathVersion("cursor-agent", "--disable-auto-update", "--version"))
}
