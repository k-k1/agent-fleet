package bootinstall

import (
	"context"
	"fmt"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/installx"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// rtkNeedMiB is the free space an rtk install asks of home: the tarball plus the unpacked binary.
const rtkNeedMiB = 150

// rtkAsset names the release asset for an architecture (the Dockerfile bake uses the same ones).
func rtkAsset(arch string) (string, error) {
	switch arch {
	case "amd64", "x86_64":
		return "rtk-oss-x86_64-unknown-linux-musl.tar.gz", nil
	case "arm64", "aarch64":
		return "rtk-oss-aarch64-unknown-linux-gnu.tar.gz", nil
	}
	return "", fmt.Errorf("unsupported arch: %s", arch)
}

// rtkCurrent is the installed version, "" when it cannot be read.
func (e Env) rtkCurrent(bin string) string {
	out, err := exec.Command(bin, "--version").Output()
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(string(out), "\n")
	if f := strings.Fields(line); len(f) >= 2 {
		return f[1]
	}
	return ""
}

// RTK installs the pinned rtk build into ~/.local/bin when it is missing, broken, or (with
// Repin) not at the pin. It returns the process exit code and records the outcome.
func RTK(ctx context.Context, e Env) int {
	pin := e.Pins["rtk"]
	if pin == "" {
		return ExitOK
	}
	bin := filepath.Join(e.localBin(), "rtk")
	need := false
	switch {
	case !e.cliPresent("rtk"):
		need = true
	case func() bool { _, err := os.Stat(bin); return err == nil && !e.runs(bin) }():
		need = true // present but does not answer --version: counts as not present
	case e.Repin && usable(bin):
		if cur := e.rtkCurrent(bin); cur != "" && cur != pin {
			need = true
		}
	}
	if !need {
		if e.cliPresent("rtk") {
			e.note("rtk", statusSkip, "")
			fmt.Fprintln(e.Stdout, "[entrypoint] boot-install: rtk already present (skip)")
		}
		return ExitOK
	}
	if free, ok := e.FreeMiB(e.Home); ok && free < rtkNeedMiB {
		fmt.Fprintf(e.Stdout, "[entrypoint] WARN: only %d MiB free in home, %d MiB needed for rtk; skipping (keeping what is installed)\n", free, rtkNeedMiB)
		// A tool with a usable copy keeps working: its skipped upgrade is "skipped", not "failed".
		if e.cliUsable("rtk") {
			e.note("rtk", statusSkip, ReasonNoSpace)
		} else {
			e.note("rtk", statusFailed, ReasonNoSpace)
		}
		return ExitNoSpace
	}
	if err := e.installRTK(ctx, pin); err != nil {
		fmt.Fprintln(e.Stderr, err)
		fmt.Fprintln(e.Stdout, "[entrypoint] WARN: rtk boot-install failed (retrying next start)")
		e.note("rtk", statusFailed, classify(err))
		return ExitFailed
	}
	fmt.Fprintf(e.Stdout, "[entrypoint] boot-install rtk %s\n", pin)
	e.note("rtk", statusOK, "")
	return ExitOK
}

func (e Env) installRTK(ctx context.Context, ver string) error {
	asset, err := rtkAsset(e.Arch)
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "af-rtk-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	base := fmt.Sprintf("%s/v%s", e.RTKBaseURL, ver)
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
	// A matching checksum says nothing about whether the binary's runtime libraries match this
	// image (the arm64 release is a gnu build that needs a recent glibc), so the candidate is run
	// before it replaces anything.
	return e.installAtomic(bin, filepath.Join(e.localBin(), "rtk"))
}
