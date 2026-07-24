package main

// install_kiro.go — on-demand installer for the Kiro CLI (kind="kiro", docs/43
// Track B / decisions/0026).
//
// Every OTHER agent CLI is either baked into the image (/usr/local, BAKE_AGENT_CLIS=1)
// or universally boot-installed into every user's ~/.local by the entrypoint on the
// lean variant. Kiro is deliberately NOT: its bundle extracts to ~855MB (kiro-cli +
// kiro-cli-chat + kiro-cli-term), an order of magnitude larger than the others, so we
// do not push it onto users who never touch it (decision §4-2). Instead it lands in
// the per-user home ONLY when that user actually uses kiro:
//   - the kiro launch program runs `workspace-agent install-kiro` when the binary is
//     missing (first-use auto-install — progress shows in the session pane),
//   - the connection card's "install" button (Track C) calls installKiro() over HTTP.
//
// Pinned by versions.json (kiro + kiro_sha256, arch-specific) — the same "version we
// verified" contract as the baked image and the other boot-installs.
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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
)

// kiroAsset returns the manifest asset name for this arch. x86_64 uses the gnu
// build (install.sh requires glibc >= 2.34; Debian 12 ships 2.36); aarch64 uses the
// **musl** build because the aarch64 gnu build requires glibc >= 2.39, newer than
// Debian 12's 2.36 (docs/43 §2.1 — verified). One image is single-arch, so the
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
func runInstallKiro(args []string) {
	if err := installKiro(); err != nil {
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

func installKiro() error {
	binDir := filepath.Join(homeDir(), ".local", "bin")
	// Fast path (pre-lock): already present → just (re)pin settings. ensureSettings
	// can't have set them on the first-ever launch (the binary was absent when it ran).
	if p, ok := kiroPresent(binDir); ok {
		fmt.Fprintf(os.Stderr, "[install-kiro] kiro-cli already installed (%s); skip\n", p)
		pinKiroSettings(p)
		return nil
	}

	// Serialise across processes (B-2): a second concurrent launch blocks here rather
	// than racing a duplicate 554MB download and writes into ~/.local/bin.
	unlock, err := kiroInstallLock()
	if err != nil {
		return err
	}
	defer unlock()

	// Re-check under the lock: another installer may have finished while we waited.
	if p, ok := kiroPresent(binDir); ok {
		fmt.Fprintf(os.Stderr, "[install-kiro] kiro-cli installed by a concurrent run (%s); skip\n", p)
		pinKiroSettings(p)
		return nil
	}

	pins := readBuildPins()
	ver, sha := pins["kiro"], pins["kiro_sha256"]
	if ver == "" || sha == "" {
		return fmt.Errorf("no kiro pin in versions.json (kiro=%q kiro_sha256=%q)", ver, sha)
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
	// Place the support binaries first, then kiro-cli LAST: kiro-cli is the presence
	// marker the launch guard / idempotency checks probe, so it must appear only once
	// the whole install is otherwise complete. A kill before this final rename leaves
	// kiro-cli absent → the next launch re-installs (self-heal) instead of bricking.
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
func pinKiroSettings(binPath string) {
	for _, kv := range [][2]string{
		{"app.disableAutoupdates", "true"},
		{"chat.disableTrustAllConfirmation", "true"},
	} {
		if err := exec.Command(binPath, "settings", kv[0], kv[1]).Run(); err != nil {
			fmt.Fprintf(os.Stderr, "[install-kiro] WARN: settings %s: %v\n", kv[0], err)
		}
	}
}
