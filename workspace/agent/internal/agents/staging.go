package agents

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// Staged sign-in: a login CLI runs under a home of its own and only the credential it wrote is
// installed into the live home once the sign-in has completed. Re-authentication then keeps the
// current login in use for the whole flow and after an abandoned one. It is not optional for
// codex (`codex login --device-auth` deletes the live auth.json the moment it starts) and is
// what lets the other kinds sign in again at all: signed in, cursor, kiro, muse and agy print no
// login URL. Measured per kind in #4231: each honours HOME, and each one's login is fully carried
// by the files (or kiro's auth_kv rows) its Install copies.

// stagingTTL bounds a staging home's life: longer than any flow's TTL, so the sweep never takes
// one in use.
const stagingTTL = time.Hour

// stagingRoot holds the staging homes. Under the Agent's state directory: local disk, outside the
// file browser, and not on the shared EFS mount the config directory can be.
func stagingRoot() string { return filepath.Join(paths.AgentStateDir(), "login-staging") }

// NewStagingHome makes an empty, private home for one sign-in of kind. Homes left behind by an
// Agent that died mid-flow are swept here.
func NewStagingHome(kind string) (string, error) {
	root := stagingRoot()
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	if ents, err := os.ReadDir(root); err == nil {
		for _, e := range ents {
			if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > stagingTTL {
				_ = os.RemoveAll(filepath.Join(root, e.Name()))
			}
		}
	}
	return os.MkdirTemp(root, kind+"-")
}

// Stage ties dir to f: f.Dir names it and closing the flow, on any path, removes it.
func Stage(f *Flow, dir string) {
	f.Dir = dir
	f.OnClose = func() { _ = os.RemoveAll(dir) }
}

// WithHome returns env with HOME set to home and every XDG base directory removed, so a CLI
// derives all of its paths from the staging home rather than writing into the live one.
func WithHome(env []string, home string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if strings.HasPrefix(kv, "HOME=") || strings.HasPrefix(kv, "XDG_") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "HOME="+home)
}

// InstallFile copies src to dst with perm, replacing dst in one rename: a reader sees the old
// credential or the new one, never a partial file.
func InstallFile(src, dst string, perm os.FileMode) error {
	b, err := os.ReadFile(src)
	if errors.Is(err, fs.ErrNotExist) {
		return errors.New("the sign-in completed but wrote no " + filepath.Base(src))
	}
	if err != nil {
		return err
	}
	return WriteFileAtomic(dst, b, perm)
}

// WriteFileAtomic writes b to dst through a synced temporary file in the same directory and a
// rename, creating the directory (0700) when it is missing.
func WriteFileAtomic(dst string, b []byte, perm os.FileMode) error {
	return WriteFileAtomicChecked(dst, b, perm, nil)
}

// WriteFileAtomicChecked runs check after staging the replacement and, if the
// check succeeds, renames it immediately. It does not lock external writers.
func WriteFileAtomicChecked(dst string, b []byte, perm os.FileMode, check func() error) error {
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(dst)+".af-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op once renamed
	if err := tmp.Chmod(perm); err != nil {
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
	if check != nil {
		if err := check(); err != nil {
			return err
		}
	}
	return os.Rename(tmp.Name(), dst)
}

// StagedPoll is the poll of a device-style sign-in (start → poll) that runs under a staging
// home. Each kind fills in how to recognise a finished sign-in there and what to install.
type StagedPoll struct {
	Flows *FlowStore
	// Logins are the routes whose login the install replaces (BeginLoginChange).
	Logins []string
	// SignedInAt reports a finished sign-in under the staging home.
	SignedInAt func(home string) bool
	// Install copies the staged credential into the live home.
	Install func(home string) error
	// LiveSignedIn answers a repeat poll of a flow this process already installed.
	LiveSignedIn func() bool
	// Detail words a flow that ended without signing in; nil = its whole output, trimmed.
	Detail func(f *Flow) string
	// Installed runs after a successful install (a status cache to drop).
	Installed func()

	mu   sync.Mutex
	done map[string]time.Time
}

// Serve answers one poll of flowID.
//
// Only the staging home is consulted while the flow is live: the live login is the one being
// replaced, and reading it would report a re-authentication done before the user approved
// anything. A flow whose CLI exited without signing in (refused, expired, broken) is reported
// as failed so the client stops instead of polling out its deadline.
func (p *StagedPoll) Serve(w http.ResponseWriter, flowID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	f := p.Flows.Get(flowID)
	if f == nil {
		// A repeat poll of a flow this process installed says connected without "stored"; the
		// Control Plane audits only the call that saw the sign-in finish.
		if p.wasInstalled(flowID) && p.LiveSignedIn() {
			httpx.WriteJSON(w, http.StatusOK, map[string]any{"connected": true, "stored": false})
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"connected": false})
		return
	}
	ended := f.Ended() // before the probe: a CLI that exited and wrote nothing has failed
	if !p.SignedInAt(f.Dir) {
		if ended {
			detail := strings.TrimSpace(f.Clean())
			if p.Detail != nil {
				detail = p.Detail(f)
			}
			if t := p.Flows.Take(flowID); t != nil {
				t.Close()
			}
			httpx.WriteJSON(w, http.StatusOK, map[string]any{"connected": false, "failed": true, "detail": detail})
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"connected": false})
		return
	}
	// The flow can have been reaped (and its staging home removed) while the probe ran: Reap
	// does not take p.mu.
	if f = p.Flows.Take(flowID); f == nil {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"connected": false})
		return
	}
	defer f.Close() // removes the staging home, after the install below
	loginOp, began := BeginLoginChange(w, p.Logins...)
	if !began {
		return
	}
	defer loginOp.Finish()
	if err := p.Install(f.Dir); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "install_failed", "the sign-in completed, but its login could not be installed: "+err.Error())
		return
	}
	p.markInstalled(flowID)
	if p.Installed != nil {
		p.Installed()
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"connected": true, "stored": true})
}

// markInstalled remembers flowID for stagingTTL, so a repeat poll still answers connected
// after the flow itself is gone. Called under p.mu.
func (p *StagedPoll) markInstalled(flowID string) {
	if p.done == nil {
		p.done = map[string]time.Time{}
	}
	for k, t := range p.done {
		if time.Since(t) > stagingTTL {
			delete(p.done, k)
		}
	}
	p.done[flowID] = time.Now()
}

func (p *StagedPoll) wasInstalled(flowID string) bool {
	t, ok := p.done[flowID]
	return ok && time.Since(t) <= stagingTTL
}
