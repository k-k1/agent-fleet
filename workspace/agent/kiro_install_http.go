package main

// kiro_install_http.go — the HTTP face of the on-demand Kiro installer (docs/43
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

import (
	"net/http"
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
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"state": st, "error": e})
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
