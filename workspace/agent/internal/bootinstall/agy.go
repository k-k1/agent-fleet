package bootinstall

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/hostcaps"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/installx"
)

// agyNeedMiB is the free space an agy install asks of home: the tarball plus the ~190 MB binary.
const agyNeedMiB = 400

const agyMarker = ".agy.version"

// agyAsset names the pinned object for an architecture, below <base>/<version>-<build>/.
func agyAsset(arch string) (string, error) {
	switch arch {
	case "amd64", "x86_64":
		return "linux-x64/cli_linux_x64.tar.gz", nil
	case "arm64", "aarch64":
		return "linux-arm/cli_linux_arm64.tar.gz", nil
	}
	return "", fmt.Errorf("unsupported arch: %s", arch)
}

func (e Env) agyEnv() []string {
	if e.AgyEnv != nil {
		return e.AgyEnv()
	}
	return hostcaps.AgyRDRANDMask()
}

// AgyEffectiveVersion is the version of the agy that is installed in ~/.local now, "" when
// there is none or it does not answer.
//
// The marker (.agy.version) is the version AF last installed, not the one present now: agy can
// rewrite itself, and an explicit `agy update`, the self-update opt-in and homes populated by
// older images move the binary without moving the marker. Deciding from the marker alone gets
// stuck at "marker == pin, binary differs" and the session quietly runs a different build. So
// the binary is asked, behind the host's RDRAND mask (docs/decisions/0008); the mask is applied
// per call, never exported. The marker answers only when there is no binary to ask: a binary
// that exists but is silent is broken (truncated by a full disk, say) and the marker, which
// says what AF wrote there, must not vouch for it.
func (e Env) AgyEffectiveVersion() string {
	bin := filepath.Join(e.localBin(), "agy")
	v := ""
	if usable(bin) {
		v = e.agyAnswer(bin)
	}
	if v == "" {
		if _, err := os.Stat(bin); err != nil {
			b, _ := os.ReadFile(filepath.Join(e.localBin(), agyMarker))
			v = strings.TrimSpace(strings.SplitN(string(b), "\n", 2)[0])
		}
	}
	return v
}

// agyAnswer is the version a binary reports behind the host mask ("" when it prints nothing parsable): the digits and dots of the first line, as the shell's tr -dc did.
func (e Env) agyAnswer(p string) string {
	out, _, _ := e.probeEnv(p, e.agyEnv()) // the exit status is not read: a version on stdout is what the shell accepted
	return agyParseVersion(out)
}

func agyParseVersion(out string) string {
	line, _, _ := strings.Cut(out, "\n")
	return strings.Map(func(r rune) rune {
		if r == '.' || (r >= '0' && r <= '9') {
			return r
		}
		return -1
	}, line)
}

// AGY installs the pinned agy build (versions.json's agy + agy_build + agy_sha256) into
// ~/.local/bin when it is missing, silent, or (with Repin) not at the pin, and writes the
// version marker last, so the self-update does not re-fetch the pin it was just given.
func AGY(ctx context.Context, e Env) int {
	ver, build, sum := e.Pins["agy"], e.Pins["agy_build"], e.Pins["agy_sha256"]
	bin := filepath.Join(e.localBin(), "agy")
	need := false
	if ver != "" && build != "" && sum != "" {
		_, statErr := os.Stat(bin)
		switch {
		case !e.cliPresent("agy"):
			need = true
		case statErr == nil && e.AgyEffectiveVersion() == "":
			need = true // present but silent: counts as not present
		case e.Repin && usable(bin) && e.AgyEffectiveVersion() != ver:
			need = true
		}
	}
	if !need {
		if e.cliPresent("agy") {
			e.note("agy", statusSkip, "")
			fmt.Fprintln(e.Stdout, "[entrypoint] boot-install: agy already present (skip)")
		}
		return ExitOK
	}
	if !e.room(agyNeedMiB, []string{"agy"}, "agy") {
		return ExitNoSpace
	}
	if err := e.installAGY(ctx, ver, build, sum); err != nil {
		fmt.Fprintln(e.Stderr, err)
		fmt.Fprintln(e.Stdout, "[entrypoint] WARN: agy boot-install failed (retrying next start)")
		e.note("agy", statusFailed, classify(err))
		return ExitFailed
	}
	fmt.Fprintf(e.Stdout, "[entrypoint] boot-install agy %s\n", ver)
	e.note("agy", statusOK, "")
	return ExitOK
}

func (e Env) installAGY(ctx context.Context, ver, build, sum string) error {
	asset, err := agyAsset(e.Arch)
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "af-agy-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	archive := filepath.Join(tmp, "agy.tgz")
	if err := e.get(ctx, fmt.Sprintf("%s/%s-%s/%s", e.AgyBaseURL, ver, build, asset), archive); err != nil {
		return err
	}
	if err := installx.VerifySha256(archive, strings.ToLower(sum)); err != nil {
		return fmt.Errorf("%w: %v", ErrChecksum, err)
	}
	cand := filepath.Join(tmp, "antigravity")
	if err := extractMember(archive, "antigravity", cand); err != nil {
		return err
	}
	// Exit 0 with no version is not "runs": the next start would judge it silent and fetch again.
	has := func(out string) bool { return agyParseVersion(out) != "" }
	if err := e.installAtomicWith(cand, filepath.Join(e.localBin(), "agy"), e.agyEnv(), has); err != nil {
		return err
	}
	marker := filepath.Join(e.localBin(), agyMarker)
	if err := os.WriteFile(marker, []byte(ver+"\n"), 0o644); err != nil {
		os.Remove(marker)
		return err
	}
	return nil
}
