package main

// install_muse.go — on-demand installer for Muse Code (kind="muse", ADR 0095 decision 8).
//
// Muse Code is proprietary, so the distributed image does not contain it — the rule Claude
// Code, Copilot CLI and Antigravity already live under (`Dockerfile`'s BAKE_AGENT_CLIS=0
// default, and NOTICE says so to the reader). At ≈299 MiB it is also in kiro's size class, so
// it takes kiro's shape rather than a boot-install: it lands in the per-user home only when
// that member actually wants muse.
//
//   - `workspace-agent install-muse` (and `--if-needed`, the quiet mode) — the CLI route.
//   - the connection card's install button calls installMuse() over HTTP
//     (muse_install_http.go).
//
// There is no launch guard, and that difference from kiro is deliberate: muse is managed-only,
// so there is no pane program to prepend a guard to. The driver's own Resume refuses with a
// message naming this subcommand instead (internal/agents/muse/driver.go).
//
// What AF downloads is **the binary**, not the vendor's bash launcher. That is the whole reason
// this is safe to pin: the launcher polls the release channel hourly and rewrites itself
// (MUSE_UPDATE_INTERVAL_SECONDS=3600, measured), while the manifest artifact runs standalone and
// has no self-update path at all. `MUSE_NO_AUTO_UPDATE=1` in the image env is the belt for a
// launcher a member installed themselves.
//
// 🔴 **The shadow check is version identity, never the path.** The vendor's installer default
// target is `~/.local/bin/muse` — which is also where AF puts it — so a path check would report
// AF's own binary as a shadow. What matters is provenance, and the observable proxy is whether
// `muse --version` matches the pin.
//
// 🔴 **And the version comparison must take the parenthesised build id.** `muse --version`
// prints `Muse Code 1.3.0 (1.3.0-R3401.1)` while the pin is `1.3.0-R3401.1`. The bare-semver
// idiom the agy block uses (and `extractVer`'s own regexp) yields `1.3.0`, which mismatches the
// pin on every single check — a permanent false "stale", i.e. a 299 MiB re-download per launch.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"time"
)

// museAsset returns the release-manifest asset name for this arch (ADR 0095 decision 8). One
// image is single-arch, so the versions.json muse_sha256 written for the build arch matches the
// asset chosen here — the same contract kiro's pin uses.
func museAsset() (string, error) {
	switch runtime.GOARCH {
	case "amd64":
		return "muse-x86-linux", nil
	case "arm64":
		return "muse-aarch64-linux", nil
	default:
		return "", fmt.Errorf("unsupported arch %q", runtime.GOARCH)
	}
}

// museDownloadURL is the version-addressed artifact URL. The manifest that publishes it (and
// the sha256 this verifies against) is fetchable anonymously, which is what makes an on-demand
// install viable for a proprietary CLI at all.
//
// It is a var only so a test can point the download at a local file and exercise the checksum
// gate for real; never reassigned at runtime. That gate needs a live test rather than a reading:
// a checksum verification that is simply absent looks exactly like one that passes, and this
// repository has already shipped five of those at once (the boot-install `set -e` incident).
var museDownloadURL = func(ver, asset string) string {
	return "https://lookaside.facebook.com/lookaside/muse/download/" +
		"?channel=muse&version=" + ver + "&file=" + asset
}

// museInstallStaging is the deterministic per-user staging root. It sits on the home volume
// (same filesystem as ~/.local/bin) so placement is an atomic rename, and it is wiped at the
// start of each run so a killed attempt leaves at most one install's residue.
func museInstallStaging() string {
	return filepath.Join(agentFleetShareDir(), "muse-install")
}

// museInstallLockPath is the flock file serialising concurrent installs.
func museInstallLockPath() string {
	return filepath.Join(agentFleetShareDir(), ".muse-install.lock")
}

// runInstallMuse handles `workspace-agent install-muse`. `--if-needed` only silences the
// "already installed" line; warnings and a real install still report progress.
func runInstallMuse(args []string) {
	quiet := false
	for _, a := range args {
		if a == "--if-needed" {
			quiet = true
		}
	}
	if err := installMuse(quiet); err != nil {
		fmt.Fprintf(os.Stderr, "[install-muse] %v\n", err)
		os.Exit(1)
	}
}

// museInstallLock takes the cross-process exclusive lock, returning an unlock func. A
// non-blocking try first lets us say we are waiting before we block.
func museInstallLock() (func(), error) {
	if err := os.MkdirAll(agentFleetShareDir(), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(museInstallLockPath(), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		fmt.Fprintln(os.Stderr, "[install-muse] another install is in progress; waiting for it to finish ...")
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
			f.Close()
			return nil, fmt.Errorf("flock: %w", err)
		}
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

// musePresent reports whether a usable muse is installed (home first, then a baked
// /usr/local via PATH) and returns the path to probe.
func musePresent(binDir string) (string, bool) {
	if home := filepath.Join(binDir, "muse"); fileExecutable(home) {
		return home, true
	}
	if p, err := exec.LookPath("muse"); err == nil {
		return p, true
	}
	return "", false
}

// museVersionMarkerPath records which pin the last install placed, so the common check is a
// stat rather than an exec of a 299 MiB binary. It is only ever a FAST PATH for "current": any
// mismatch falls through to probing the binary, so a stale or absent marker can never trigger a
// bogus re-download.
func museVersionMarkerPath(binDir string) string {
	return filepath.Join(binDir, ".muse.version")
}

// museBuildIDRe takes the BUILD ID out of `muse --version`, and museSemverRe is the fallback.
//
// Measured on 1.3.0-R3401.1, the line is `Muse Code 1.3.0 (1.3.0-R3401.1)` and the release
// manifest's version — the pin — is the parenthesised form. Matching bare semver instead gives
// `1.3.0`, which never equals the pin: every check would read "stale" and re-download 299 MiB.
//
// ⚠️ They are two regexps and tried in order on purpose. One alternation (`\(…\)|semver`) does
// NOT work: the bare version sits to the LEFT of the parenthesised one on the real line, and a
// match is chosen by position before alternative, so the semver branch wins and the build id is
// dropped — the exact defect this function exists to prevent. Written that way first; the table
// test caught it.
var (
	museBuildIDRe = regexp.MustCompile(`\(([0-9][^)\s]*)\)`)
	museSemverRe  = regexp.MustCompile(`[0-9]+\.[0-9]+(?:\.[0-9]+)?`)
)

// museParseVersion extracts the comparable version from a `muse --version` line: the
// parenthesised build id when there is one, else a bare version (so a future release that stops
// printing one still compares equal to a pin in the same shape).
func museParseVersion(raw string) string {
	raw = strings.TrimSpace(raw)
	if m := museBuildIDRe.FindStringSubmatch(raw); m != nil {
		return m[1]
	}
	return museSemverRe.FindString(raw)
}

// museState is the verdict of museCheck.
type museState int

const (
	museMissing    museState = iota // no muse anywhere → install
	museCurrent                     // installed version == pin → nothing to do
	museStale                       // installed version != pin → re-install at the pin
	museUnknownVer                  // present but `--version` gave nothing parsable
)

// museCheck compares the installed muse against the versions.json pin and returns the path,
// the version it reports (best effort) and the verdict.
//
// This is also the shadow guard of decision 8. A member who ran the vendor's one-line installer
// has an unmanaged, self-updating build at exactly the path AF uses, so "is there a binary at
// ~/.local/bin/muse?" cannot tell the two apart — a drifted VERSION can, and the repair is the
// re-install this verdict triggers. Gate A measured both directions: a shadow reporting a
// drifted version was replaced, and one reporting the pinned version was left alone.
//
// want=="" (no pin readable — a hand-built image with no versions.json) degrades to leaving a
// present binary alone: there is nothing to compare it against.
func museCheck(binDir, want string) (string, string, museState) {
	p, ok := musePresent(binDir)
	if !ok {
		return "", "", museMissing
	}
	if want == "" {
		return p, "", museCurrent
	}
	if b, err := os.ReadFile(museVersionMarkerPath(binDir)); err == nil &&
		strings.TrimSpace(string(b)) == want && p == filepath.Join(binDir, "muse") {
		return p, want, museCurrent
	}
	cur := museBinVersion(p)
	switch {
	case cur == "":
		return p, "", museUnknownVer
	case cur == want:
		return p, cur, museCurrent
	default:
		return p, cur, museStale
	}
}

// museInstallCurrent reports whether the pinned muse is already in place. Present-but-STALE is
// deliberately false, so the HTTP route performs the upgrade instead of answering "done".
func museInstallCurrent() bool {
	binDir := filepath.Join(homeDir(), ".local", "bin")
	_, _, st := museCheck(binDir, readBuildPins()["muse"])
	return st == museCurrent || st == museUnknownVer
}

// museBinVersion returns the build id `<bin> --version` reports, or "" when it cannot be
// determined. It does NOT go through probeVersion's extractVer, which would strip the build id.
func museBinVersion(bin string) string {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--version").Output()
	if err != nil && len(out) == 0 {
		return ""
	}
	return museParseVersion(strings.SplitN(string(out), "\n", 2)[0])
}

// museSkipInstall reports whether the download can be skipped, printing the reason. who
// annotates the message for the post-lock re-check; quiet suppresses the "already current"
// line only.
func museSkipInstall(binDir, want, who string, quiet bool) bool {
	p, cur, st := museCheck(binDir, want)
	switch st {
	case museCurrent:
		if !quiet {
			fmt.Fprintf(os.Stderr, "[install-muse] Muse Code %s already installed%s (%s); skip\n", cur, who, p)
		}
		if cur != "" && p == filepath.Join(binDir, "muse") {
			writeMuseVersionMarker(binDir, cur)
		}
		return true
	case museUnknownVer:
		// Don't churn a 299 MiB re-download over an unreadable version — a binary that
		// cannot report one is an environment problem, and the launch will surface it.
		fmt.Fprintf(os.Stderr, "[install-muse] WARN: muse at %s reports no parsable version; leaving it as-is (pin %s)\n", p, want)
		return true
	case museStale:
		// The shadow case as well as a pin bump: either way the pinned version is what AF runs.
		fmt.Fprintf(os.Stderr, "[install-muse] muse %s is installed (%s) but the pinned version is %s; re-installing ...\n", cur, p, want)
	}
	return false
}

// writeMuseVersionMarker records the pin now living in ~/.local/bin (best effort).
func writeMuseVersionMarker(binDir, ver string) {
	_ = os.WriteFile(museVersionMarkerPath(binDir), []byte(ver+"\n"), 0o644)
}

func installMuse(quiet bool) error {
	binDir := filepath.Join(homeDir(), ".local", "bin")
	pins := readBuildPins()
	ver, sha := pins["muse"], pins["muse_sha256"]

	// Fast path (pre-lock): the pinned version is already there.
	if museSkipInstall(binDir, ver, "", quiet) {
		return nil
	}
	if ver == "" || sha == "" {
		return fmt.Errorf("no muse pin in versions.json (muse=%q muse_sha256=%q)", ver, sha)
	}
	asset, err := museAsset()
	if err != nil {
		return err
	}

	// Serialise across processes: two install requests would otherwise each pull 299 MiB and
	// race on the write into the same ~/.local/bin.
	unlock, err := museInstallLock()
	if err != nil {
		return err
	}
	defer unlock()

	// Re-check under the lock: another installer may have finished while we waited.
	if museSkipInstall(binDir, ver, " by a concurrent run", quiet) {
		return nil
	}

	// Deterministic staging on the home volume: wipe residue from a killed prior run, then
	// rebuild. The defer cleans it on normal and error exit alike.
	staging := museInstallStaging()
	_ = os.RemoveAll(staging)
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return err
	}
	defer os.RemoveAll(staging)

	staged := filepath.Join(staging, "muse")
	fmt.Fprintf(os.Stderr, "[install-muse] downloading Muse Code %s (%s, ~299MB) ...\n", ver, asset)
	if err := runCmd("curl", "-fSL", "--retry", "3", "--retry-delay", "2", "--retry-connrefused",
		"-o", staged, museDownloadURL(ver, asset)); err != nil {
		return fmt.Errorf("download: %w", err)
	}
	if err := verifySha256(staged, sha); err != nil {
		return err
	}
	if err := os.Chmod(staged, 0o755); err != nil {
		return err
	}
	// Sanity-check before the binary is ever visible under ~/.local/bin, so a truncated or
	// arch-incompatible download is never promoted (the same gate the bake path applies).
	if err := museSanityCheck(staged); err != nil {
		return fmt.Errorf("staged muse failed sanity check: %w", err)
	}
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return err
	}
	// Drop a stale marker BEFORE touching the binary: from here the tree is mid-swap, and an
	// interrupted upgrade must never leave a marker claiming the new pin over the old install.
	_ = os.Remove(museVersionMarkerPath(binDir))
	// One atomic same-filesystem rename. On an upgrade it lands on an existing file, and
	// rename only swaps the directory entry — a running muse host keeps its inode and is
	// unaffected (no ETXTBSY, unlike writing in place).
	installed := filepath.Join(binDir, "muse")
	if err := os.Rename(staged, installed); err != nil {
		return fmt.Errorf("place muse: %w", err)
	}
	writeMuseVersionMarker(binDir, ver)
	fmt.Fprintf(os.Stderr, "[install-muse] installed Muse Code %s -> %s\n", ver, installed)
	return nil
}

// museSanityCheck runs `<bin> --version` with a timeout, so the caller can refuse to promote a
// binary that cannot exec. It requires a parsable build id rather than just a clean exit: this
// is also the only chance to catch the vendor changing the version line, which would otherwise
// surface as a permanent "stale" and a re-download on every check.
func museSanityCheck(bin string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	raw := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	if museParseVersion(raw) == "" {
		return fmt.Errorf("no version in %q — the version line changed shape, so the pin comparison would never match", raw)
	}
	return nil
}
