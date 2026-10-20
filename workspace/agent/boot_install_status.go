package main

// What the entrypoint's boot-install did for each CLI on this start, so Settings › Toolchains
// can say "install failed: home disk full" where it used to show "—".
//
// entrypoint.sh writes one tab-separated line per CLI to bootInstallStatusPath, inside a private
// directory it created (/tmp is shared, so nothing is read from a path someone else could have
// planted):
//
//	<tool>\t<status>\t<reason>
//
// status is ok, skipped (already present) or failed; reason is set for failed only and is one
// of no_space, network, checksum, other. The file lives under /tmp, not in home: the failure
// being reported is usually a full home, and a status file that needed home to be written
// would vanish exactly then. /tmp is per container and wiped on restart, which matches what
// it describes: one boot's outcome. A missing or unreadable file means "unknown", never "ok".

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
)

// bootInstallStatusPath is a var only so tests can point it at a temp file. Empty (the normal
// case) means the entrypoint's AF_BOOT_INSTALL_STATUS: the entrypoint sets it only when it could
// record this start, so a start that could not never reads an earlier start's file.
var bootInstallStatusPath = ""

func bootInstallPath() string {
	if bootInstallStatusPath != "" {
		return bootInstallStatusPath
	}
	return os.Getenv("AF_BOOT_INSTALL_STATUS")
}

// privateEnough: nobody but the owner may write it, so another user cannot swap the contents.
func privateEnough(fi os.FileInfo) bool { return fi.Mode().Perm()&0o022 == 0 }

// bootInstallMaxBytes bounds the read: a handful of CLIs need a few hundred bytes. Anything
// larger is not what the entrypoint wrote.
const bootInstallMaxBytes = 8 << 10

// ownedBy reports whether fi is owned by the current user.
func ownedBy(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}

// readBootInstallFile returns the file's bytes only when the directory is a real directory of
// ours and the file is a small regular file of ours; anything else (symlink, FIFO, foreign owner,
// too large) is "unknown". It is called with the tool-versions cache lock held, so it must never
// block: O_NONBLOCK keeps a FIFO from stalling the open, and the type is checked on the open
// descriptor, not on the name.
func readBootInstallFile(path string) ([]byte, bool) {
	if path == "" {
		return nil, false
	}
	dir, err := os.Lstat(filepath.Dir(path))
	if err != nil || !dir.IsDir() || !ownedBy(dir) || !privateEnough(dir) {
		return nil, false
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || !ownedBy(fi) || !privateEnough(fi) || fi.Size() > bootInstallMaxBytes {
		return nil, false
	}
	// The file can still grow between the stat and the read: cap the read itself.
	b, err := io.ReadAll(io.LimitReader(f, bootInstallMaxBytes+1))
	if err != nil || len(b) > bootInstallMaxBytes {
		return nil, false
	}
	return b, true
}

// bootInstall is one CLI's outcome.
type bootInstall struct {
	Status string `json:"status"`           // ok | skipped | failed
	Reason string `json:"reason,omitempty"` // no_space | network | checksum | other (failed only)
}

var bootInstallStatuses = map[string]bool{"ok": true, "skipped": true, "failed": true}
var bootInstallReasons = map[string]bool{"no_space": true, "network": true, "checksum": true, "other": true}

// readBootInstallStatus parses the file; unknown statuses and reasons are dropped rather than
// passed on, so the Console only ever sees the closed set it has strings for.
func readBootInstallStatus() map[string]bootInstall {
	b, ok := readBootInstallFile(bootInstallPath())
	if !ok {
		return nil
	}
	out := map[string]bootInstall{}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(f) < 2 || !bootInstallStatuses[f[1]] {
			continue
		}
		bi := bootInstall{Status: f[1]}
		if bi.Status == "failed" {
			bi.Reason = "other"
			if len(f) > 2 && bootInstallReasons[f[2]] {
				bi.Reason = f[2]
			}
		}
		out[f[0]] = bi // a later line for the same tool wins
	}
	return out
}

// bootInstallFailed is the failed subset of readBootInstallStatus, tool to reason. nil when
// nothing failed and when the file is unknown: "skipped" and "ok" are never reported, and an
// unreadable file must not look like a failure.
//
// The tool name comes from a file the member's own user can write, so only a short CLI-name-shaped
// one is passed on, and at most bootInstallMaxTools of them: the CP stores and serves this.
func bootInstallFailed() map[string]string {
	var out map[string]string
	for tool, bi := range readBootInstallStatus() {
		if bi.Status != "failed" || !validBootTool(tool) || len(out) >= bootInstallMaxTools {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[tool] = bi.Reason
	}
	return out
}

const bootInstallMaxTools = 16

func validBootTool(s string) bool {
	if s == "" || len(s) > 32 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

// --- Refusing a start whose CLI did not install ---

// bootInstallRefusalMsg is the English cause of each reason, and bootInstallRefusalHint what the
// caller can do about it. The Console shows its own wording for the code (err.cli_install_<reason>);
// these reach callers that have no catalog (create_session, the scheduler).
var bootInstallRefusalMsg = map[string]string{
	"no_space": "the home disk is full",
	"network":  "a network error occurred",
	"checksum": "the download did not match its checksum",
	"other":    "an error occurred (see the workspace log)",
}

var bootInstallRefusalHint = map[string]string{
	"no_space": "Free some space and restart the workspace to try again.",
	"network":  "Restart the workspace to try again.",
	"checksum": "Restart the workspace to try again.",
	"other":    "See Settings > Toolchains and the workspace log.",
}

// bakedBinDir is where the image bakes CLIs; a var only so tests can point it elsewhere.
var bakedBinDir = "/usr/local/bin"

// execFile reports whether path (symlinks followed) is a regular file someone can execute: a
// partial install can leave a directory or a 0600 file behind, and neither launches.
func execFile(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0
}

// launchOverridden reports whether the operator pointed this kind's launch somewhere else. The
// agents read these variables themselves (internal/agents/*/program.go), and a launch that does
// not use the installed CLI is not made impossible by that CLI's failed install, so none of them
// is second-guessed here: a CMD override runs verbatim, a BIN override is honoured when it
// resolves to a real binary.
func launchOverridden(kind string) bool {
	up := strings.ToUpper(kind)
	if os.Getenv("AGENT_"+up+"_CMD") != "" {
		return true
	}
	if kind == "claude" && os.Getenv("AGENT_SESSION_CMD") != "" {
		return true
	}
	if bin := os.Getenv("AGENT_" + up + "_BIN"); bin != "" {
		if strings.Contains(bin, "/") {
			return execFile(bin)
		}
		_, err := exec.LookPath(bin)
		return err == nil
	}
	return false
}

// cliPresent reports whether kind's CLI can be run now. It looks at what a launch would run, not
// only at the status file: the failure recorded at start goes stale the moment the CLI is
// installed by hand or by a later start. The default binary is looked up where the entrypoint's own
// cli_present does plus PATH; an override of the launch and the opencode 2 opt-in binary count too.
func cliPresent(kind, cmd string) bool {
	if launchOverridden(kind) {
		return true
	}
	// opencode launches the private opt-in binary only while opencode.UseV2 says so (chosen,
	// installed and the PATH one still 1.x); otherwise the PATH one is what runs, and a private
	// install left behind by an earlier choice must not stand in for it.
	if kind == "opencode" && opencode.UseV2() {
		return execFile(opencode.V2Binary())
	}
	if _, err := exec.LookPath(cmd); err == nil {
		return true
	}
	return execFile(filepath.Join(bakedBinDir, cmd)) || execFile(filepath.Join(homeDir(), ".local", "bin", cmd))
}

// cliInstallRefusal refuses a start of kind only when this start's boot-install recorded
// that kind's CLI as failed AND the CLI is not present now. Everything else proceeds: a
// kind with no boot-installed CLI, "ok" / "skipped", an unknown or absent status, a failure
// recorded for another tool, and a stale failure whose CLI has since appeared.
func cliInstallRefusal(kind string) *sessionx.SpawnRefusal {
	for _, spec := range toolSpecs {
		if spec.Name != kind {
			continue
		}
		bi, ok := readBootInstallStatus()[kind]
		if !ok || bi.Status != "failed" || cliPresent(kind, spec.Cmd) {
			return nil
		}
		reason := bi.Reason
		if _, known := bootInstallRefusalMsg[reason]; !known {
			reason = "other"
		}
		return &sessionx.SpawnRefusal{
			Status: http.StatusConflict,
			Code:   "cli_install_" + reason,
			Message: fmt.Sprintf("%s could not be installed when this workspace started because %s. %s",
				kind, bootInstallRefusalMsg[reason], bootInstallRefusalHint[reason]),
		}
	}
	return nil
}

func init() { sessionx.KindStartRefusal = cliInstallRefusal }
