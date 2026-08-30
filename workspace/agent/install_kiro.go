package main

// install_kiro.go — on-demand installer for the Kiro CLI (kind="kiro", docs/log/43
// Track B / decisions/0026).
//
// Every OTHER agent CLI is either baked into the image (/usr/local, BAKE_AGENT_CLIS=1)
// or universally boot-installed into every user's ~/.local by the entrypoint on the
// lean variant. Kiro is deliberately NOT: its bundle extracts to ~855MB (kiro-cli +
// kiro-cli-chat + kiro-cli-term), an order of magnitude larger than the others, so we
// do not push it onto users who never touch it (decision §4-2). Instead it lands in
// the per-user home ONLY when that user actually uses kiro:
//   - the kiro launch program runs `workspace-agent install-kiro --if-needed` on every
//     launch (first-use auto-install and later pin catch-up — progress shows in the
//     session pane; silent when the pinned version is already there),
//   - the connection card's "install" button (Track C) calls installKiro() over HTTP.
//
// Pinned by versions.json (kiro + kiro_sha256, arch-specific) — the same "version we
// verified" contract as the baked image and the other boot-installs. That pin is also
// enforced on EVERY launch, not just the first one: the ~/.local copy lives on the home
// volume and survives image rebuilds, so a pin bump (2.14.1 → 2.14.2) would otherwise
// leave the user on the old version forever — kiro has no self-update path here (we pin
// app.disableAutoupdates off) and no boot-install to refresh it, unlike every other CLI.
// A drift between the installed version and the pin therefore triggers a re-install
// (see kiroCheck) — upgrade AND downgrade, because the pin is the version we verified.
//
// The install is minutes long and the most natural user reaction to a slow first
// launch is to stop the session (kill the tmux pane) — so it MUST be crash-safe:
//   - Placement is atomic (staging → rename), with kiro-cli (the presence marker the
//     launch guard and idempotency checks probe) moved into place LAST. A kill at any
//     earlier point leaves kiro-cli absent, so the next launch re-runs the install
//     rather than treating a half-copied binary as "installed" (no self-heal path).
//   - The staged kiro-cli is sanity-checked (`--version`) before placement, so a
//     truncated / arch-incompatible binary is never promoted (mirrors the Dockerfile
//     bake's `kiro-cli --version` gate).
//   - A cross-process advisory lock (flock) serialises concurrent installs — two
//     panes launched at once would otherwise each pull 554MB and race on writes into
//     the same ~/.local/bin (ETXTBSY on the running binary). The second waiter wakes,
//     sees kiro-cli present, and skips.
//   - Staging lives on the home volume (same filesystem as ~/.local/bin, so rename is
//     atomic) under one deterministic dir that is wiped at the start of every run —
//     bounding leftover to a single in-flight install instead of piling ~1.4GB of
//     residue into /tmp on each interrupted attempt.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

// kiroAsset returns the manifest asset name for this arch. x86_64 uses the gnu
// build (install.sh requires glibc >= 2.34; Debian 12 ships 2.36); aarch64 uses the
// **musl** build because the aarch64 gnu build requires glibc >= 2.39, newer than
// Debian 12's 2.36 (docs/log/43 §2.1 — verified). One image is single-arch, so the
// versions.json kiro_sha256 (written per build arch) matches the asset chosen here.
func kiroAsset() (string, error) {
	switch runtime.GOARCH {
	case "amd64":
		return "kirocli-x86_64-linux.zip", nil
	case "arm64":
		return "kirocli-aarch64-linux-musl.zip", nil
	default:
		return "", fmt.Errorf("unsupported arch %q", runtime.GOARCH)
	}
}

// kiroInstallStaging is the deterministic per-user staging root for an on-demand
// install. It sits on the home volume (same filesystem as ~/.local/bin) so the final
// placement is an atomic rename, and it is wiped at the start of each install so a
// killed prior attempt leaves at most one install's worth of residue behind.
func kiroInstallStaging() string {
	return filepath.Join(agentFleetShareDir(), "kiro-install")
}

// kiroInstallLockPath is the flock file guarding concurrent installs (B-2).
func kiroInstallLockPath() string {
	return filepath.Join(agentFleetShareDir(), ".kiro-install.lock")
}

// runInstallKiro handles `workspace-agent install-kiro`.
//
// `--if-needed` is the launch-guard mode (kiro/program.go prepends it to every kiro
// pane program): identical behaviour, but it says nothing at all when the pinned
// version is already installed. The guard runs on EVERY launch — including the common
// "nothing to do" case — so it must not print a line into the pane before the TUI
// takes the screen. A real install/upgrade still reports progress there, as before.
func runInstallKiro(args []string) {
	quiet := false
	for _, a := range args {
		if a == "--if-needed" {
			quiet = true
		}
	}
	if err := installKiro(quiet); err != nil {
		fmt.Fprintf(os.Stderr, "[install-kiro] %v\n", err)
		os.Exit(1)
	}
}

// kiroInstallLock takes the cross-process exclusive lock, returning an unlock func.
// Shared by both entry points (the CLI subcommand the launch guard runs, and the HTTP
// route's installKiro() goroutine) because both flow through installKiro(). A
// non-blocking try first lets us tell the user we're waiting before blocking.
func kiroInstallLock() (func(), error) {
	if err := os.MkdirAll(agentFleetShareDir(), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(kiroInstallLockPath(), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		fmt.Fprintln(os.Stderr, "[install-kiro] another install is in progress; waiting for it to finish ...")
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

// kiroPresent reports whether a usable kiro-cli is already installed (home first,
// then a baked /usr/local via PATH) and returns the binary path to pin settings on.
func kiroPresent(binDir string) (string, bool) {
	if home := filepath.Join(binDir, "kiro-cli"); fileExecutable(home) {
		return home, true
	}
	if p, err := exec.LookPath("kiro-cli"); err == nil {
		return p, true
	}
	return "", false
}

// kiroVersionMarkerPath records which pin the last install placed, so the per-launch
// check is a stat instead of an exec of the 855MB binary (same trick as agy's
// .agy.version marker in the entrypoint). It is only ever a FAST PATH for "current":
// any mismatch falls through to probing the binary itself, so a stale/absent marker
// (e.g. an install from before this file grew one, or a baked /usr/local kiro) can
// never trigger a bogus 554MB re-download.
func kiroVersionMarkerPath(binDir string) string {
	return filepath.Join(binDir, ".kiro.version")
}

// kiroState is the verdict of kiroCheck.
type kiroState int

const (
	kiroMissing    kiroState = iota // no kiro-cli anywhere → install
	kiroCurrent                     // installed version == pin → nothing to do
	kiroStale                       // installed version != pin → re-install at the pin
	kiroUnknownVer                  // present but `--version` gave nothing parsable
)

// kiroCheck compares the installed kiro-cli against the versions.json pin and returns
// the binary path, the version it reports (best effort) and the verdict.
//
// want=="" (no pin readable — a hand-built image without versions.json) degrades to the
// pre-pin-check behaviour: a present binary is left alone rather than treated as stale,
// since there is nothing to compare it against.
func kiroCheck(binDir, want string) (string, string, kiroState) {
	p, ok := kiroPresent(binDir)
	if !ok {
		return "", "", kiroMissing
	}
	if want == "" {
		return p, "", kiroCurrent
	}
	if b, err := os.ReadFile(kiroVersionMarkerPath(binDir)); err == nil &&
		strings.TrimSpace(string(b)) == want && p == filepath.Join(binDir, "kiro-cli") {
		return p, want, kiroCurrent
	}
	cur := kiroBinVersion(p)
	switch {
	case cur == "":
		return p, "", kiroUnknownVer
	case cur == want:
		return p, cur, kiroCurrent
	default:
		return p, cur, kiroStale
	}
}

// kiroInstallCurrent reports whether the pinned kiro is already in place, i.e. an
// install request has nothing to do. Present-but-STALE is deliberately false: the HTTP
// route then runs the upgrade instead of answering "done" (kiro_install_http.go).
func kiroInstallCurrent() bool {
	binDir := filepath.Join(homeDir(), ".local", "bin")
	_, _, st := kiroCheck(binDir, readBuildPins()["kiro"])
	return st == kiroCurrent || st == kiroUnknownVer
}

// kiroBinVersion returns the version `<bin> --version` reports ("kiro-cli 2.14.2" →
// "2.14.2"), or "" when it can't be determined (probe failed / unparsable output).
func kiroBinVersion(bin string) string {
	tb := probeVersion(bin, nil) // 5s timeout, nil when the path is gone
	if tb == nil || !verNumRe.MatchString(tb.Version) {
		return ""
	}
	return tb.Version
}

// kiroSkipInstall reports whether the download can be skipped, printing the reason.
// who annotates the message for the post-lock re-check. quiet suppresses the "already
// current" line only (warnings and the upgrade notice always print).
func kiroSkipInstall(binDir, want, who string, quiet bool) bool {
	p, cur, st := kiroCheck(binDir, want)
	switch st {
	case kiroCurrent:
		if !quiet {
			fmt.Fprintf(os.Stderr, "[install-kiro] kiro-cli %s already installed%s (%s); skip\n", cur, who, p)
		}
		// (Re)pin settings: ensureSettings can't have set them on the first-ever launch
		// (the binary was absent when it ran). No-op once cli.json already carries them.
		pinKiroSettings(p)
		if cur != "" && p == filepath.Join(binDir, "kiro-cli") {
			writeKiroVersionMarker(binDir, cur)
		}
		return true
	case kiroUnknownVer:
		// Don't churn a 554MB re-download over an unreadable version — a binary that
		// can't report one is an environment problem, and the launch will surface it.
		fmt.Fprintf(os.Stderr, "[install-kiro] WARN: kiro-cli at %s reports no parsable version; leaving it as-is (pin %s)\n", p, want)
		pinKiroSettings(p)
		return true
	case kiroStale:
		fmt.Fprintf(os.Stderr, "[install-kiro] kiro-cli %s is installed (%s) but the pinned version is %s; re-installing ...\n", cur, p, want)
	}
	return false
}

// writeKiroVersionMarker records the pin now living in ~/.local/bin (best effort).
func writeKiroVersionMarker(binDir, ver string) {
	_ = os.WriteFile(kiroVersionMarkerPath(binDir), []byte(ver+"\n"), 0o644)
}

func installKiro(quiet bool) error {
	binDir := filepath.Join(homeDir(), ".local", "bin")
	pins := readBuildPins()
	ver, sha := pins["kiro"], pins["kiro_sha256"]

	// Fast path (pre-lock): the pinned version is already there → just (re)pin settings.
	if kiroSkipInstall(binDir, ver, "", quiet) {
		return nil
	}
	if ver == "" || sha == "" {
		return fmt.Errorf("no kiro pin in versions.json (kiro=%q kiro_sha256=%q)", ver, sha)
	}

	// Serialise across processes (B-2): a second concurrent launch blocks here rather
	// than racing a duplicate 554MB download and writes into ~/.local/bin.
	unlock, err := kiroInstallLock()
	if err != nil {
		return err
	}
	defer unlock()

	// Re-check under the lock: another installer may have finished while we waited.
	if kiroSkipInstall(binDir, ver, " by a concurrent run", quiet) {
		return nil
	}

	asset, err := kiroAsset()
	if err != nil {
		return err
	}

	// Deterministic staging on the home volume: wipe any residue from a killed prior
	// run, then rebuild. defer cleans it on normal / error exit.
	staging := kiroInstallStaging()
	_ = os.RemoveAll(staging)
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return err
	}
	defer os.RemoveAll(staging)

	zipPath := filepath.Join(staging, "kiro.zip")
	url := fmt.Sprintf("https://prod.download.cli.kiro.dev/stable/%s/%s", ver, asset)
	fmt.Fprintf(os.Stderr, "[install-kiro] downloading Kiro CLI %s (%s, ~554MB) ...\n", ver, asset)
	if err := runCmd("curl", "-fSL", "--retry", "3", "--retry-delay", "2", "--retry-connrefused", "-o", zipPath, url); err != nil {
		return fmt.Errorf("download: %w", err)
	}
	if err := verifySha256(zipPath, sha); err != nil {
		return err
	}
	extract := filepath.Join(staging, "x")
	if err := runCmd("unzip", "-q", zipPath, "-d", extract); err != nil {
		return fmt.Errorf("extract: %w", err)
	}
	// Free the 554MB zip immediately so peak disk on the home volume stays near one
	// copy of the extracted binaries rather than two.
	_ = os.Remove(zipPath)

	// The zip lays out kirocli/bin/{kiro-cli,kiro-cli-chat,kiro-cli-term,q,qchat}. We
	// place the same 3 binaries the bundled install.sh does (the q/qchat shims are not
	// used by AF). Move — not copy — them into ~/.local/bin so there is no transient
	// double of the 855MB payload and each placement is an atomic same-filesystem rename.
	srcBin := filepath.Join(extract, "kirocli", "bin")
	stagedKiro := filepath.Join(srcBin, "kiro-cli")
	if !fileExecutable(stagedKiro) {
		if err := os.Chmod(stagedKiro, 0o755); err != nil {
			return fmt.Errorf("extracted archive has no kiro-cli: %w", err)
		}
	}
	// Sanity-check the staged binary before it is ever visible under ~/.local/bin, so a
	// truncated or arch-incompatible download is never promoted (mirrors the bake gate).
	if err := kiroSanityCheck(stagedKiro); err != nil {
		return fmt.Errorf("staged kiro-cli failed sanity check: %w", err)
	}
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return err
	}
	// Drop a stale marker BEFORE touching the binaries: from here on the tree is
	// mid-swap, and an interrupted upgrade must never leave a marker claiming the new
	// pin over a half-old install (the next launch re-probes and finishes the job).
	_ = os.Remove(kiroVersionMarkerPath(binDir))
	// Place the support binaries first, then kiro-cli LAST: kiro-cli is the presence
	// marker the launch guard / idempotency checks probe, so it must appear only once
	// the whole install is otherwise complete. A kill before this final rename leaves
	// kiro-cli absent → the next launch re-installs (self-heal) instead of bricking.
	// On an UPGRADE the renames land on existing files: rename only swaps the directory
	// entry, so a kiro session running the old binary keeps its inode and is unaffected
	// (no ETXTBSY, unlike writing in place). Peak disk on the home volume is the old
	// install plus the freshly extracted one until these renames free the old inodes.
	for _, name := range []string{"kiro-cli-chat", "kiro-cli-term"} {
		src := filepath.Join(srcBin, name)
		_ = os.Chmod(src, 0o755)
		if err := os.Rename(src, filepath.Join(binDir, name)); err != nil {
			return fmt.Errorf("place %s: %w", name, err)
		}
	}
	installed := filepath.Join(binDir, "kiro-cli")
	if err := os.Rename(stagedKiro, installed); err != nil {
		return fmt.Errorf("place kiro-cli: %w", err)
	}
	pinKiroSettings(installed)
	writeKiroVersionMarker(binDir, ver)
	fmt.Fprintf(os.Stderr, "[install-kiro] installed Kiro CLI %s -> %s\n", ver, binDir)
	return nil
}

// kiroSanityCheck runs `<bin> --version` with a timeout. A native binary that is
// truncated or built for an incompatible libc fails to exec here, so the caller can
// refuse to promote it. `kiro-cli 2.14.1` is the expected shape; we only require a
// clean exit (the version pin itself is asserted by e2e-smoke, not at install time).
func kiroSanityCheck(bin string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w (output: %s)", err, string(out))
	}
	return nil
}

// pinKiroSettings fixes the two fleet-required kiro settings, best effort (same as
// the kiro package's ensureSettings, applied here because on a first-ever launch the
// agent's ensureSettings ran while the binary was still absent — a no-op):
//   - app.disableAutoupdates=true    — version is managed by image rebuild / this
//     pinned install; stop the background self-updater (kiro has no build-time ENV
//     knob, so suppression is runtime-only).
//   - chat.disableTrustAllConfirmation=true — suppress the --trust-all-tools danger
//     dialog that otherwise wedges the launch pane on first run.
//
// Writes to ~/.kiro/settings/cli.json (plaintext JSON). Failure only warns.
//
// Skipped entirely when cli.json already carries both settings: this now runs on every
// kiro launch (the guard calls install-kiro --if-needed each time), and two execs of the
// 855MB binary per launch is a cost worth avoiding when the file already says what we want.
func pinKiroSettings(binPath string) {
	if kiroSettingsPinned() {
		return
	}
	for _, kv := range [][2]string{
		{"app.disableAutoupdates", "true"},
		{"chat.disableTrustAllConfirmation", "true"},
	} {
		if err := exec.Command(binPath, "settings", kv[0], kv[1]).Run(); err != nil {
			fmt.Fprintf(os.Stderr, "[install-kiro] WARN: settings %s: %v\n", kv[0], err)
		}
	}
}

// kiroSettingsPinned reports whether ~/.kiro/settings/cli.json already holds both
// fleet-required settings as true. Unreadable / unexpected shape → false, i.e. fall
// back to asking the CLI (which is the authority on its own settings format).
func kiroSettingsPinned() bool {
	b, err := os.ReadFile(filepath.Join(homeDir(), ".kiro", "settings", "cli.json"))
	if err != nil {
		return false
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return false
	}
	for _, k := range []string{"app.disableAutoupdates", "chat.disableTrustAllConfirmation"} {
		if v, ok := m[k].(bool); !ok || !v {
			return false
		}
	}
	return true
}
