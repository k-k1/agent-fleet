package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

// Per-user UI preferences (theme / icon set / fonts / viewer options). The Console
// stores its display settings here so they follow the user across browsers/devices,
// instead of living only in each browser's localStorage. The payload is an opaque
// JSON object owned by the Console; the Agent just persists it verbatim. Stored under
// the denylisted .config/agent-fleet (hidden from the file browser) in the home
// volume, so it survives Stop→Start.

const maxUIPrefsBytes = 64 << 10 // 64 KiB cap on the prefs blob

func uiPrefsPath() string {
	return filepath.Join(homeDir(), ".config", "agent-fleet", "ui-prefs.json")
}

func handleGetUIPrefs(w http.ResponseWriter, r *http.Request) {
	b, err := os.ReadFile(uiPrefsPath())
	if err != nil || len(b) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{}) // none yet → empty object
		return
	}
	var obj map[string]any
	if json.Unmarshal(b, &obj) != nil {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}
	writeJSON(w, http.StatusOK, obj)
}

func handlePutUIPrefs(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxUIPrefsBytes+1))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "read failed")
		return
	}
	if len(body) > maxUIPrefsBytes {
		writeErr(w, http.StatusRequestEntityTooLarge, "too_large", "prefs too large")
		return
	}
	// Validate it's a JSON object before persisting (the Console owns the shape).
	var obj map[string]any
	if json.Unmarshal(body, &obj) != nil {
		writeErr(w, http.StatusBadRequest, "bad_json", "body must be a JSON object")
		return
	}
	if err := os.MkdirAll(filepath.Dir(uiPrefsPath()), 0o700); err != nil {
		writeErr(w, http.StatusInternalServerError, "mkdir_failed", err.Error())
		return
	}
	if err := os.WriteFile(uiPrefsPath(), body, 0o600); err != nil {
		writeErr(w, http.StatusInternalServerError, "write_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, obj)
}
