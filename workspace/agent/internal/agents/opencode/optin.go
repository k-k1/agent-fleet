package opencode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// The opt-in that runs opencode 2 beside a pinned 1.x in one workspace. Nothing here
// is reachable until a member both installs the v2 package (`workspace-agent install-opencode2`)
// and switches to it (`workspace-agent opencode-major 2`): UseV2 is false otherwise, and then
// every function in this file answers what the code answered before it existed.
//
// It only has a job while the opencode on PATH is 1.x (an image re-pinned to 1.x for a rollback).
// Once the pin is 2.x the PATH binary is opencode 2 and owns ~/.local/share/opencode/opencode.db,
// which it migrated in place; a second v2 with a private database beside it would split the
// member's sessions in two, so PinnedV2 makes the whole opt-in inert (docs/log/119).

// MajorEnv selects the major for this Agent process: "2" opts in, "1" opts out whatever the
// switch file says. Unset, the switch file decides.
const MajorEnv = "AF_OPENCODE_MAJOR"

// V2FallbackVersion is what V2Version answers when the image recorded no usable opencode 2
// version (an image built before versions.json carried "opencode_v2"). It is a second literal
// that nothing bumps: a test only checks that it is a 2.x release, so it may trail the image pin.
const V2FallbackVersion = "2.0.29"

// V2Version is the opencode 2 release the opt-in installs unless told otherwise: the
// Dockerfile's OPENCODE_V2_VERSION as recorded in versions.json, so a rolled-back workspace tries
// the build the contract tests last ran on (docs/log/119). It is never derived from the
// "opencode" pin: the opt-in only has a job while that pin is 1.x, and handing it a 1.x version
// would install the wrong package. A recorded value that is not 2.x is ignored.
func V2Version() string {
	if b, err := os.ReadFile(buildPinsPath); err == nil {
		var pins struct {
			V2 string `json:"opencode_v2"`
		}
		if json.Unmarshal(b, &pins) == nil && majorOf(pins.V2) >= 2 {
			return pins.V2
		}
	}
	return V2FallbackVersion
}

// V2Prefix is the npm prefix `@opencode/cli` is installed into. It is a private tree, so the
// package's `opencode` bin never lands on PATH next to the pinned 1.x one; the database is kept
// outside it so that reinstalling the package never touches sessions.
func V2Prefix() string {
	return filepath.Join(paths.HomeDir(), ".local", "share", "agent-fleet", "opencode2")
}

// V2Binary is the installed opencode 2 executable, "" when it is not installed.
func V2Binary() string {
	p := filepath.Join(V2Prefix(), "node_modules", "@opencode", "cli", "bin", "opencode.exe")
	if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
		return p
	}
	return ""
}

// V2DBPath is the database every opencode 2 process af starts uses. opencode 2 migrates the
// database it opens in place and never writes the 1.x tables again, so sharing
// ~/.local/share/opencode/opencode.db would strand the member's 1.x sessions the moment a
// 2.x process touched it (measured on 2.0.28: the file, its WAL and its SHM all change after
// `opencode models`). OPENCODE_DB is the one data override the binary has; its log, locks and
// model cache stay in the shared directories and are format-compatible (measured).
func V2DBPath() string {
	return filepath.Join(paths.HomeDir(), ".local", "share", "agent-fleet", "opencode2-data", "opencode.db")
}

// majorSwitchFile holds "2" while the member has switched this workspace to opencode 2.
func majorSwitchFile() string { return filepath.Join(paths.AgentStateDir(), "opencode-major") }

// WantsV2 reports the member's choice, whether or not the binary is there.
func WantsV2() bool {
	switch strings.TrimSpace(os.Getenv(MajorEnv)) {
	case "2":
		return true
	case "1":
		return false
	}
	b, err := os.ReadFile(majorSwitchFile())
	return err == nil && strings.TrimSpace(string(b)) == "2"
}

// SetWantsV2 writes the switch file. The environment variable still wins where it is set.
func SetWantsV2(on bool) error {
	f := majorSwitchFile()
	if !on {
		if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		return err
	}
	return writeAtomic(f, []byte("2\n"), 0o644)
}

// PinnedV2 is whether the opencode on PATH (or, failing that, the image pin) is already
// opencode 2, so the opt-in has nothing to add. It does not look at the switch.
func PinnedV2() bool { return pathMajor() >= 2 }

// UseV2 is whether af launches the private opt-in opencode 2 right now: chosen AND installed AND
// the PATH opencode is still 1.x. A choice without a binary falls back to the pinned 1.x, with its
// own data, instead of failing every launch; a choice made before the pin moved to 2.x is ignored
// and the pinned opencode 2 is used with the main database. The private install and its database
// are left on disk untouched (they are not merged into the main one).
func UseV2() bool { return WantsV2() && V2Binary() != "" && !PinnedV2() }

// LaunchSpec is one reading of the switch: which binary, which database and which key a start
// uses. A start takes it once and builds the command and the environment from it, so a switch
// that flips in between cannot give the v2 binary the 1.x database (or the reverse).
type LaunchSpec struct {
	V2  bool
	Maj int    // the major this reading launches
	Bin string // "opencode" (resolved through PATH) or the opt-in binary's path
	Key string // launchKey as of the reading
}

// CurrentLaunch reads the switch once.
func CurrentLaunch() LaunchSpec {
	// The switch and the binary are read exactly once, here. Everything else in the spec follows
	// from them: the opt-in is major 2 by construction, and the off branch resolves its major from
	// the PATH binary and the image pin only (pathMajor does not look at the switch), so no
	// combination of {off, major 2} or {on, major 1} can be built.
	bin := V2Binary()
	wants := bin != "" && WantsV2() // before pathMajor: the switch is read once, ahead of the resolver
	if pm := pathMajor(); !wants || pm >= 2 {
		return LaunchSpec{Maj: pm, Bin: "opencode"}
	}
	return LaunchSpec{V2: true, Maj: 2, Bin: bin, Key: "v2:" + bin}
}

// Env is the provider keys plus, for the opt-in, its own database: the whole environment an
// opencode process af starts gets from af, as of this reading.
func (l LaunchSpec) Env() []string { return append(keyEnv(), l.processEnv()...) }

func (l LaunchSpec) processEnv() []string {
	if !l.V2 {
		return nil
	}
	// opencode 2 does not create the directory of OPENCODE_DB (measured: ENOENT on start).
	_ = os.MkdirAll(filepath.Dir(V2DBPath()), 0o700)
	return []string{"OPENCODE_DB=" + V2DBPath()}
}

// Major is the opencode major this reading launches: 2 for the opt-in, else what the PATH one is.
func (l LaunchSpec) Major() int { return l.Maj }

// version is the full version of the binary this reading launches.
func (l LaunchSpec) version() string {
	if l.V2 {
		return readPackageVersion(filepath.Join(filepath.Dir(l.Bin), "..", "package.json"))
	}
	return pathVersion()
}

func (l LaunchSpec) shellBin() string {
	if l.Bin == "opencode" {
		return l.Bin
	}
	return session.ShellQuote(l.Bin)
}

// Binary is what af executes for opencode as of one reading; callers that also need the
// environment must use CurrentLaunch instead.
func Binary() string { return CurrentLaunch().Bin }

// v2ProcessEnv is the opt-in's own variables as of one reading; nil unless active.
func v2ProcessEnv() []string { return CurrentLaunch().processEnv() }

// ProcessEnv is the exported form, for the callers outside this package.
func ProcessEnv() []string { return v2ProcessEnv() }

// launchKey identifies which opencode a daemon was started as. A running daemon whose key is not
// the current one was started under the other setting.
func launchKey() string { return CurrentLaunch().Key }

// shellBinary is Binary for a command line that a shell will parse.
func shellBinary() string { return CurrentLaunch().shellBin() }
