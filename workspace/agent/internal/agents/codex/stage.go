package codex

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// Staged sign-in: the device flow runs codex under a CODEX_HOME of its own and its
// auth.json is copied into the live home only once the sign-in has completed, so the current
// login keeps working for the whole flow and survives an abandoned one.

// codexHome is the CODEX_HOME every session reads; the Agent never sets the variable.
func codexHome() string { return filepath.Join(paths.HomeDir(), ".codex") }

// stagingRoot holds the staging homes. Under the Agent's state directory: local disk, outside
// the file browser, and not on the shared EFS mount the config directory can be.
func stagingRoot() string { return filepath.Join(paths.AgentStateDir(), "codex-login") }

// newStagingHome makes an empty, private CODEX_HOME for one sign-in. The live config.toml is
// copied in so account-level settings (a forced login method or workspace) apply to the login
// as they would in place. Homes left by an Agent that died mid-flow are swept here: nothing
// outlives the flow TTL legitimately.
func newStagingHome() (string, error) {
	root := stagingRoot()
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	if ents, err := os.ReadDir(root); err == nil {
		for _, e := range ents {
			if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > 2*flowTTL {
				_ = os.RemoveAll(filepath.Join(root, e.Name()))
			}
		}
	}
	dir, err := os.MkdirTemp(root, "flow-")
	if err != nil {
		return "", err
	}
	if b, err := os.ReadFile(filepath.Join(codexHome(), "config.toml")); err == nil {
		_ = os.WriteFile(filepath.Join(dir, "config.toml"), b, 0o600)
	}
	return dir, nil
}

// loggedInAt is loggedIn for the CODEX_HOME at home. A var for tests.
var loggedInAt = func(home string) bool {
	cmd := exec.Command("codex", "login", "status")
	cmd.Env = append(os.Environ(), "CODEX_HOME="+home)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false
	}
	return !strings.Contains(strings.ToLower(string(out)), "not logged in")
}

// installAuth copies the staged auth.json into home, replacing the live one in a single
// rename: a session reading auth.json sees the old login or the new one, never a partial file.
func installAuth(stage, home string) error {
	b, err := os.ReadFile(filepath.Join(stage, "auth.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return errors.New("codex reported a sign-in but wrote no auth.json")
	}
	if err != nil {
		return err
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(home, ".auth.json.af-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op once renamed
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(home, "auth.json"))
}

// pollMu serialises device polls, so two polls of one flow cannot both install it.
var pollMu sync.Mutex

// installed remembers the flows this process has installed, for flowTTL, so a repeat poll of
// one still answers connected after the flow itself is gone.
var installed = &doneSet{m: map[string]time.Time{}}

type doneSet struct {
	mu sync.Mutex
	m  map[string]time.Time
}

func (d *doneSet) add(id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for k, t := range d.m {
		if time.Since(t) > flowTTL {
			delete(d.m, k)
		}
	}
	d.m[id] = time.Now()
}

func (d *doneSet) has(id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	t, ok := d.m[id]
	return ok && time.Since(t) <= flowTTL
}
