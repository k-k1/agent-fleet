package main

// muse_install_http.go — the HTTP face of the on-demand Muse Code installer (ADR 0095
// decision 8). Muse Code is proprietary and not baked, so a fresh workspace has no `muse` and
// muse.Status() reports supported=false: the connection card cannot offer the sign-in (that
// needs the binary) and the kind cannot be launched, so the card offers an install button that
// lands the binary in the member's ~/.local via the same installMuse() the CLI subcommand runs.
//
// The download is minutes long, so this runs in the background behind a small state machine the
// card polls: POST starts it (idempotent while running), GET reports {state, error}. Once
// state=done the next /connections poll sees supported=true and the card switches to the
// sign-in.
//
// The SAME route drives updates and the shadow repair. The home copy survives image rebuilds
// and AF's copy has no self-updater, so after a pin bump the member sits on the old version
// until something re-installs; and a member who ran the vendor's own installer has an
// unmanaged, self-updating build at the same path. Both show up as a version that differs from
// the pin, so GET reports {installed, version, pin, updateAvailable} and POST performs the
// re-install. Unlike kiro there is no launch guard to do it implicitly — muse is managed-only,
// so there is no pane program to hang one on — which makes this route the only place it happens.

import (
	"net/http"
	"path/filepath"
	"sync"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

type museInstall struct {
	mu    sync.Mutex
	state string // "" (idle) | "installing" | "done" | "error"
	err   string
}

var museInstaller museInstall

// snapshot returns the current state, normalizing idle to "idle" for the client.
func (m *museInstall) snapshot() (string, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.state
	if st == "" {
		st = "idle"
	}
	return st, m.err
}

// handleMuseInstall drives the on-demand install. POST /connections/muse/install starts it (or
// reports the in-flight/finished state); GET reports the state.
func handleMuseInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		st, e := museInstaller.snapshot()
		body := map[string]any{"state": st, "error": e}
		// Version facts for the card. Skipped while an install runs: the tree is mid-swap, so
		// any version read there is meaningless (and the card's poll is seconds-tight).
		if st != "installing" {
			pin := readBuildPins()["muse"]
			_, cur, vst := museCheck(filepath.Join(homeDir(), ".local", "bin"), pin)
			body["installed"] = vst != museMissing
			body["version"] = cur // "" when the binary cannot report one
			body["pin"] = pin
			// Only claim an update when the versions are KNOWN to differ: an unreadable
			// version or a missing pin must not nag the member into a 299 MiB download.
			body["updateAvailable"] = vst == museStale
		}
		httpx.WriteJSON(w, http.StatusOK, body)
		return
	}
	// POST. Nothing to do when the pinned version is already present. Present-but-stale is
	// NOT "done": it falls through so this route brings it to the pin.
	if museInstallCurrent() {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"state": "done"})
		return
	}
	museInstaller.mu.Lock()
	if museInstaller.state == "installing" {
		museInstaller.mu.Unlock()
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"state": "installing"})
		return
	}
	museInstaller.state = "installing"
	museInstaller.err = ""
	museInstaller.mu.Unlock()
	go func() {
		err := installMuse(false)
		museInstaller.mu.Lock()
		if err != nil {
			museInstaller.state = "error"
			museInstaller.err = err.Error()
		} else {
			museInstaller.state = "done"
		}
		museInstaller.mu.Unlock()
	}()
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"state": "installing"})
}
