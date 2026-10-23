package main

// install_opencode2.go — the on-demand installer and the switch for the opencode 2 opt-in.
//
// The opt-in exists for an image that pins opencode 1.x (moving the pin switches every member at
// once). Once the pin is 2.x it has nothing to add: the pinned opencode 2 owns the main database,
// so every function here answers with a no-op and a message (opencode.PinnedV2). While the pin is
// 1.x this lets one workspace run 2.x next to it: `install-opencode2` puts `@opencode/cli` into a private npm
// prefix under ~/.local/share/agent-fleet (the package's `opencode` and `opencode2` bins are
// never put on PATH, so the pinned `opencode` is not shadowed), and `opencode-major 2` makes af
// launch it, with its own database (opencode.OptIn* in internal/agents/opencode/optin.go).
//
// Measured on 2.0.28 (linux x64): `npm install` leaves 806 MB, because the package pulls every
// libc/baseline variant of the binary as an optional dependency and its postinstall copies the
// matching one over bin/opencode.exe. Only the copy is used, so the sibling platform packages
// are pruned after the check: 202 MB stay.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
)

// runInstallOpencode2 handles `workspace-agent install-opencode2 [<version>]`.
func runInstallOpencode2(args []string) {
	if opencode.PinnedV2() {
		fmt.Fprintln(os.Stderr, "[install-opencode2] "+pinnedV2Message)
		return
	}
	ver := opencode.V2Version()
	if len(args) > 0 && args[0] != "" {
		ver = args[0]
	}
	if err := installOpencode2(ver, npmInstallInto); err != nil {
		fmt.Fprintf(os.Stderr, "[install-opencode2] %v\n", err)
		os.Exit(1)
	}
}

// npmInstallInto is `npm install --prefix <dir> @opencode/cli@<ver>`; a var for tests.
func npmInstallInto(dir, ver string) error {
	cmd := exec.Command("npm", "install", "--prefix", dir, "--no-audit", "--no-fund", "--no-update-notifier",
		"@opencode/cli@"+ver)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	return cmd.Run()
}

// installOpencode2 installs version ver into opencode.V2Prefix(). It is idempotent, and the
// prefix is replaced only by a tree that passed the version check: a staged install that fails
// leaves the previous one in place.
func installOpencode2(ver string, install func(dir, ver string) error) error {
	prefix := opencode.V2Prefix()
	parent := filepath.Dir(prefix)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	lock, err := os.OpenFile(prefix+".lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("flock: %w", err)
	}
	if bin := opencode.V2Binary(); bin != "" && opencode2Version(bin) == ver {
		fmt.Fprintf(os.Stderr, "[install-opencode2] opencode %s already installed at %s\n", ver, prefix)
		return nil
	}
	staging := prefix + ".staging"
	if err := os.RemoveAll(staging); err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "[install-opencode2] installing @opencode/cli@%s (about 200 MB after pruning) ...\n", ver)
	if err := install(staging, ver); err != nil {
		return fmt.Errorf("npm install: %w", err)
	}
	bin := filepath.Join(staging, "node_modules", "@opencode", "cli", "bin", "opencode.exe")
	if fi, err := os.Lstat(bin); err != nil || !fi.Mode().IsRegular() {
		return fmt.Errorf("the package did not leave a binary at %s (its postinstall failed or was skipped)", bin)
	}
	if got := opencode2Version(bin); got != ver {
		return fmt.Errorf("installed binary reports %q, want %q", got, ver)
	}
	// The postinstall copied the matching platform binary over bin/opencode.exe (a regular file,
	// checked above), so the platform packages beside it only cost disk.
	if ents, err := filepath.Glob(filepath.Join(staging, "node_modules", "@opencode", "cli-*")); err == nil {
		for _, e := range ents {
			_ = os.RemoveAll(e)
		}
	}
	old := prefix + ".old"
	_ = os.RemoveAll(old)
	if _, err := os.Stat(prefix); err == nil {
		if err := os.Rename(prefix, old); err != nil {
			return err
		}
	}
	if err := os.Rename(staging, prefix); err != nil {
		_ = os.Rename(old, prefix)
		return err
	}
	_ = os.RemoveAll(old)
	fmt.Fprintf(os.Stderr, "[install-opencode2] installed opencode %s at %s\n"+
		"Switch to it with: workspace-agent opencode-major 2\n", ver, prefix)
	return nil
}

// opencode2Version runs `<bin> --version` ("opencode v2.0.28") and returns "2.0.28"; "" on failure.
// The home is a throwaway one so the probe cannot touch (or migrate) the member's data.
func opencode2Version(bin string) string {
	tmp, err := os.MkdirTemp("", "af-oc2-ver-")
	if err != nil {
		return ""
	}
	defer os.RemoveAll(tmp)
	cmd := exec.Command(bin, "--version")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + tmp, "OPENCODE_DISABLE_AUTOUPDATE=true",
		"OPENCODE_DB=" + filepath.Join(tmp, "probe.db")}
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(strings.TrimSpace(string(out)), "opencode v")
}

// runOpencodeMajor handles `workspace-agent opencode-major [1|2]`: no argument reports the
// state; 2 switches this workspace to the installed opencode 2, 1 switches back.
func runOpencodeMajor(args []string) {
	if len(args) == 0 {
		fmt.Println(opencodeMajorStatus())
		return
	}
	if opencode.PinnedV2() {
		// "1" still clears a leftover switch file; "2" changes nothing. Neither can move the
		// pinned opencode, so both end in the status that says why.
		switch args[0] {
		case "1":
			if err := opencode.SetWantsV2(false); err != nil {
				fmt.Fprintf(os.Stderr, "[opencode-major] %v\n", err)
				os.Exit(1)
			}
		case "2":
		default:
			fmt.Fprintln(os.Stderr, "usage: workspace-agent opencode-major [1|2]")
			os.Exit(2)
		}
		fmt.Println(opencodeMajorStatus())
		return
	}
	switch args[0] {
	case "2":
		if opencode.V2Binary() == "" {
			fmt.Fprintln(os.Stderr, "[opencode-major] opencode 2 is not installed; run: workspace-agent install-opencode2")
			os.Exit(1)
		}
		warnLiveTerminals()
		if err := opencode.SetWantsV2(true); err != nil {
			fmt.Fprintf(os.Stderr, "[opencode-major] %v\n", err)
			os.Exit(1)
		}
		// With the switch on, opencode 2 commands af runs would start the shared per-user
		// background service; seed the setting that keeps them private (what the Agent does at boot).
		if err := opencode.EnsureServiceDisabled(); err != nil {
			fmt.Fprintf(os.Stderr, "[opencode-major] service.json: %v (left as is)\n", err)
		}
	case "1":
		warnLiveTerminals()
		if err := opencode.SetWantsV2(false); err != nil {
			fmt.Fprintf(os.Stderr, "[opencode-major] %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintln(os.Stderr, "usage: workspace-agent opencode-major [1|2]")
		os.Exit(2)
	}
	fmt.Println(opencodeMajorStatus())
}

// pinnedV2Message is what the opt-in commands say when the image's opencode is already 2.x.
const pinnedV2Message = "this workspace's opencode is already 2.x, so there is nothing to install or switch: " +
	"it uses the main database (~/.local/share/opencode/opencode.db). To go back to opencode 1.x, " +
	"re-pin the image (see the release notes)."

func opencodeMajorStatus() string {
	if opencode.PinnedV2() {
		msg := "opencode 2 opt-in: not used (" + pinnedV2Message + ")"
		if opencode.V2Binary() != "" || opencode.WantsV2() {
			msg += "\nLeftovers of an earlier opt-in are ignored, not merged into the main database: " +
				opencode.V2Prefix() + " (the private install) and " + filepath.Dir(opencode.V2DBPath()) +
				" (its database). Sessions that only exist there are not visible to the pinned opencode; " +
				"delete the two directories when you no longer need them."
		}
		return msg
	}
	state := "off (pinned opencode 1.x)"
	switch {
	case opencode.UseV2():
		state = "on (opencode 2: " + opencode.V2Binary() + ", database " + opencode.V2DBPath() + ")"
	case opencode.WantsV2():
		state = "requested, but opencode 2 is not installed: still on the pinned 1.x"
	}
	if os.Getenv(opencode.MajorEnv) != "" {
		state += " [" + opencode.MajorEnv + "=" + os.Getenv(opencode.MajorEnv) + " in this environment]"
	}
	return "opencode 2 opt-in: " + state + "\nThe running Agent picks the switch up at its next opencode launch; a running opencode serve is drained and restarted then."
}

// warnLiveTerminals says what a switch cannot carry over: an open Terminal (CLI) pane keeps the
// opencode it was launched with, and an opencode 2 pane loses its server when the serve is
// replaced.
func warnLiveTerminals() {
	fmt.Fprintln(os.Stderr, "[opencode-major] note: open Terminal (CLI) opencode sessions keep the opencode they started with; relaunch them to move to the new one.")
}
