// update.go — host self-update surface for the native runtime (docs/log/42 + ADR).
//
// The `af` launcher stages a new release on disk (`af update`) and re-points the
// ~/.local/bin/af symlink, but the RUNNING af-cp keeps serving the version it was
// started from until the service restarts. These endpoints let the Console see
// that a newer version is staged (current vs the VERSION file behind the symlink)
// and trigger the restart that applies it — decoupled under systemd so our own
// SIGTERM does not abort the restarter.
//
// Native-only: the launcher passes AF_SELF_PKG / AF_SELF_LINK; without them (the
// Docker/ECS deployments, where the CP is updated by rebuilding the image, and
// dev builds) the routes are not registered and the Console hides the surface.
package main

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// updateEnabled reports whether host self-update applies (native launcher present).
func updateEnabled() bool { return os.Getenv("AF_SELF_LINK") != "" }

// stagedVersion reads the VERSION file of the package the ~/.local/bin/af symlink
// currently points at — i.e. what a restart would boot into. Empty if it cannot
// be resolved (hand-extracted copy, missing file).
func stagedVersion() string {
	link := os.Getenv("AF_SELF_LINK")
	if link == "" {
		return ""
	}
	real, err := filepath.EvalSymlinks(link)
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(filepath.Dir(real), "VERSION"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func registerUpdateRoutes(mux *http.ServeMux, _ config) {
	if !updateEnabled() {
		return
	}
	mux.HandleFunc("GET /api/update/status", updateStatus)
	mux.HandleFunc("POST /api/update/apply", updateApply)
}

// updateStatus: current (running, baked) vs installed (staged on disk). Not
// auth-exempt (same stance as /api/version).
func updateStatus(w http.ResponseWriter, _ *http.Request) {
	installed := stagedVersion()
	writeJSON(w, http.StatusOK, map[string]any{
		"current":         buildVersion,
		"installed":       installed,
		"restartRequired": installed != "" && installed != buildVersion,
		"systemd":         os.Getenv("AF_SYSTEMD_UNIT") != "",
	})
}

// updateApply applies a staged update by restarting so the new symlink target is
// booted. Under systemd the restart is launched as a transient unit via
// systemd-run so it survives the SIGTERM systemd sends us; in the foreground we
// re-exec the launcher (the symlink already points at the staged version), which
// keeps the workspace child processes alive across the CP swap.
func updateApply(w http.ResponseWriter, _ *http.Request) {
	installed := stagedVersion()
	if installed == "" || installed == buildVersion {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": map[string]string{"code": "no_staged_update", "message": "no newer version is staged"},
		})
		return
	}

	if unit := os.Getenv("AF_SYSTEMD_UNIT"); unit != "" {
		// --collect reaps the transient unit when done; a fixed --unit name makes a
		// concurrent double-click a harmless "unit already exists" no-op.
		cmd := exec.Command("systemd-run", "--user", "--collect",
			"--unit=agent-fleet-apply", "systemctl", "--user", "restart", unit)
		cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
		if err := cmd.Run(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": map[string]string{"code": "restart_failed", "message": err.Error()},
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "mode": "systemd", "to": installed})
		return
	}

	// Foreground: re-exec the launcher after the response flushes.
	link := os.Getenv("AF_SELF_LINK")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "mode": "foreground", "to": installed})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	go func() {
		time.Sleep(400 * time.Millisecond)
		// Replaces this process image with `af start`; on failure the service just
		// keeps running the old version (nothing is lost).
		_ = syscall.Exec(link, []string{link, "start"}, os.Environ())
	}()
}
