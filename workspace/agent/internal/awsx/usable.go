package awsx

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// A profile that asks for confirmation before use is not written to ~/.aws/config, so a plain
// `aws --profile` cannot run it unnoticed, yet it stays usable through af-aws-exec (ADR 0126
// decision 6). "Usable" is therefore no longer "in the managed block": the sync records the
// usable names here, under the same lock as the block, and writes each withheld profile's
// config to a file of the Agent's own that af-aws-exec points the CLI at.

// usablePath is the last sync's usable set.
func usablePath() string { return filepath.Join(paths.AgentStateDir(), "aws-usable.json") }

// gatedDir holds the withheld profiles' configs, one file per profile name.
func gatedDir() string { return filepath.Join(paths.AgentStateDir(), "aws-gated") }

// GatedConfigPath is the config af-aws-exec runs the withheld profile name from. It holds the
// same sections the managed block would (no secret); a chain's also holds its source's.
func GatedConfigPath(name string) string { return filepath.Join(gatedDir(), name+".config") }

type usableFile struct {
	Exported []string `json:"exported"`
	Withheld []string `json:"withheld,omitempty"`
}

// writeUsable records res's usable set and rewrites the withheld profiles' configs, removing
// those of names no longer withheld. A failure stops the sync before the block is written.
func writeUsable(res SyncResult) error {
	if len(res.Withheld) > 0 {
		if err := os.MkdirAll(gatedDir(), 0o700); err != nil {
			return err
		}
	}
	for _, n := range res.Withheld {
		if !profileFileName(n) {
			continue
		}
		if err := writeIfChanged(GatedConfigPath(n), []byte(res.withheldINI[n])); err != nil {
			return err
		}
	}
	if ents, err := os.ReadDir(gatedDir()); err == nil {
		for _, e := range ents {
			name, ok := cutConfigSuffix(e.Name())
			if ok && !slices.Contains(res.Withheld, name) {
				os.Remove(filepath.Join(gatedDir(), e.Name()))
			}
		}
	}
	b, err := json.Marshal(usableFile{Exported: res.Exported, Withheld: res.Withheld})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(usablePath()), 0o700); err != nil {
		return err
	}
	return writeIfChanged(usablePath(), b)
}

// profileFileName reports whether a profile name is safe as a file name: one path segment of
// the characters a Settings profile name can hold (the renderer already refused the rest).
func profileFileName(n string) bool {
	return n != "." && n != ".." && gatedNameRe.MatchString(n)
}

// gatedNameRe is the allowlist the renderer holds a profile name to (ssm.RenderSSMConfig).
var gatedNameRe = regexp.MustCompile(`^[A-Za-z0-9._@-]{1,64}$`)

func cutConfigSuffix(f string) (string, bool) {
	const suffix = ".config"
	if len(f) <= len(suffix) || f[len(f)-len(suffix):] != suffix {
		return "", false
	}
	return f[:len(f)-len(suffix)], true
}

func writeIfChanged(path string, data []byte) error {
	if old, err := os.ReadFile(path); err == nil && string(old) == string(data) {
		return nil
	}
	return writeAtomic(path, data, 0o600)
}

// UsableNow lists the profiles af-aws-exec may run now, for the Agent's routes (login, logout,
// state, the expiry warning) and for af-aws-exec right before it fetches credentials. It is
// worked out from the files as they are now, not from the last sync: the last Settings list is
// rendered again against the member's current ~/.aws/config and credentials (render writes
// nothing), so a profile, sso-session or [DEFAULT] line they added since takes a profile away,
// a chain's source included. An exported profile must also still be in the block on disk, and a
// withheld one must have its config. A file that exists but cannot be read makes nothing usable:
// it may define any of these names, and the af-<name> token would then be theirs.
func UsableNow() []string {
	b, err := os.ReadFile(ConfigPath())
	if err != nil && !os.IsNotExist(err) {
		return nil
	}
	creds, cerr := os.ReadFile(filepath.Join(filepath.Dir(ConfigPath()), "credentials"))
	if cerr != nil && !os.IsNotExist(cerr) {
		return nil
	}
	inBlock := exportedInText(string(b))
	ps, _, ok := cachedList()
	if !ok {
		// No Settings list to check against (before the first sync of this membership): the
		// block is what was usable, as before this change.
		return inBlock
	}
	_, res, err := render(string(b), string(creds), ps)
	if err != nil {
		return nil
	}
	var out []string
	for _, n := range res.Exported {
		if slices.Contains(inBlock, n) {
			out = append(out, n)
		}
	}
	for _, n := range res.Withheld {
		if !profileFileName(n) {
			continue
		}
		if _, err := os.Stat(GatedConfigPath(n)); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// WithheldNow lists the profiles the last sync withheld from the managed block.
func WithheldNow() []string {
	b, err := os.ReadFile(usablePath())
	if err != nil {
		return nil
	}
	var u usableFile
	if json.Unmarshal(b, &u) != nil {
		return nil
	}
	return u.Withheld
}
