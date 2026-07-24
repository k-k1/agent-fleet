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
//     missing (first-use auto-install — progress shows in the session pane), and
//   - the connection card's "install" button (Track C) calls the same command.
//
// Pinned by versions.json (kiro + kiro_sha256, arch-specific) — the same "version we
// verified" contract as the baked image and the other boot-installs. The install path
// mirrors the Dockerfile bake: download the pinned zip → verify sha256 → unzip → run
// the bundled install.sh (which drops the 3 binaries into ~/.local/bin; setup skipped).
// Follows the install-jdk idiom (download → staging → verified before use).

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

// runInstallKiro handles `workspace-agent install-kiro`.
func runInstallKiro(args []string) {
	if err := installKiro(); err != nil {
		fmt.Fprintf(os.Stderr, "[install-kiro] %v\n", err)
		os.Exit(1)
	}
}

func installKiro() error {
	binDir := filepath.Join(homeDir(), ".local", "bin")
	// Idempotent: a home install or a baked /usr/local binary means we're done —
	// still (re)pin the fleet settings, which the agent's ensureSettings can't have
	// set on the first-ever launch (the binary was absent when it ran).
	if fileExecutable(filepath.Join(binDir, "kiro-cli")) {
		fmt.Fprintf(os.Stderr, "[install-kiro] kiro-cli already installed at %s; skip\n", binDir)
		pinKiroSettings(filepath.Join(binDir, "kiro-cli"))
		return nil
	}
	if p, err := exec.LookPath("kiro-cli"); err == nil {
		fmt.Fprintf(os.Stderr, "[install-kiro] kiro-cli already on PATH (%s); skip\n", p)
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

	staging, err := os.MkdirTemp("", "kiro-")
	if err != nil {
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
	// The zip lays out kirocli/{BUILD-INFO,bin/*,install.sh}. Run the bundled
	// installer with setup skipped: it installs the 3 binaries into ~/.local/bin
	// (no dotfile edits, no `kiro-cli setup`). The musl asset's install.sh skips the
	// glibc guard; the gnu asset's guard passes on Debian 12 (2.36 >= 2.34).
	script := filepath.Join(extract, "kirocli", "install.sh")
	if _, err := os.Stat(script); err != nil {
		return fmt.Errorf("extracted archive has no kirocli/install.sh")
	}
	cmd := exec.Command("sh", script)
	cmd.Env = append(os.Environ(), "KIRO_CLI_SKIP_SETUP=1")
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("install.sh: %w", err)
	}
	installed := filepath.Join(binDir, "kiro-cli")
	if !fileExecutable(installed) {
		return fmt.Errorf("install.sh completed but %s is missing", installed)
	}
	pinKiroSettings(installed)
	fmt.Fprintf(os.Stderr, "[install-kiro] installed Kiro CLI %s -> %s\n", ver, binDir)
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
