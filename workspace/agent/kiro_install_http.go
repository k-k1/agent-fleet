package main

// kiro_install_http.go — the HTTP face of the on-demand Kiro installer (docs/log/43
// Track C). Kiro's ~855MB bundle is NOT baked on the lean image (decision §4-2), so
// a lean workspace starts with kiro-cli absent and Status() reports
// supported=false. The connection card can't reach kiro's login flow (it needs the
// binary) and the kind can't be launched (available gates on connected), so the card
// offers an explicit "install" button that lands the CLI in the user's ~/.local via
// the same installKiro() the first-use launch guard runs (Track B).
//
// The download is minutes long, so this runs the install in the background and
// exposes a tiny state machine the card polls: POST starts it (idempotent while
// running), GET reports {state, error}. Once state=done, the next /connections poll
// sees supported=true and the card switches to the login flow.
//
// The SAME route drives updates. kiro's home copy survives image rebuilds and has no
// self-updater (we pin app.disableAutoupdates off), so after a versions.json pin bump
// the user sits on an old version until something re-installs. The launch guard does it
// automatically at the next kiro launch, but that is an implicit multi-minute stall the
// user didn't ask for — so GET also reports {installed, version, pin, updateAvailable}
// and the card turns that into a visible "update available → press when you want to"
// affordance. POST then performs the upgrade exactly like a first install.

import (
	"net/http"
	"path/filepath"
	"sync"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

type kiroInstall struct {
	mu    sync.Mutex
	state string // "" (idle) | "installing" | "done" | "error"
	err   string
}

var kiroInstaller kiroInstall

// snapshot returns the current state, normalizing idle to "idle" for the client.
func (k *kiroInstall) snapshot() (string, string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	st := k.state
	if st == "" {
		st = "idle"
	}
	return st, k.err
}

// handleKiroInstall drives the on-demand install. POST /connections/kiro/install
// starts it (or reports the in-flight/finished state); GET reports the state.
func handleKiroInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		st, e := kiroInstaller.snapshot()
		body := map[string]any{"state": st, "error": e}
		// Version facts for the card. Skipped while an install is running: the tree is
		// mid-swap, so any version we read is meaningless (and the poll is 4s-tight).
		if st != "installing" {
			pin := readBuildPins()["kiro"]
			_, cur, vst := kiroCheck(filepath.Join(homeDir(), ".local", "bin"), pin)
			body["installed"] = vst != kiroMissing
			body["version"] = cur // "" when the binary can't report one
			body["pin"] = pin
			// Only claim an update when we KNOW the versions differ: an unreadable
			// version or a missing pin must not nag the user with a 554MB download.
			body["updateAvailable"] = vst == kiroStale
		}
		httpx.WriteJSON(w, http.StatusOK, body)
		return
	}
	// POST. Nothing to do when the PINNED version is already present (baked or a prior
	// install). A present-but-stale kiro (the home copy predates a versions.json pin
	// bump) is NOT "done" — it falls through so this route can bring it to the pin,
	// the same upgrade the launch guard performs.
	if kiroInstallCurrent() {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"state": "done"})
		return
	}
	kiroInstaller.mu.Lock()
	if kiroInstaller.state == "installing" {
		kiroInstaller.mu.Unlock()
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"state": "installing"})
		return
	}
	kiroInstaller.state = "installing"
	kiroInstaller.err = ""
	kiroInstaller.mu.Unlock()
	go func() {
		err := installKiro(false)
		kiroInstaller.mu.Lock()
		if err != nil {
			kiroInstaller.state = "error"
			kiroInstaller.err = err.Error()
		} else {
			kiroInstaller.state = "done"
		}
		kiroInstaller.mu.Unlock()
	}()
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"state": "installing"})
}
