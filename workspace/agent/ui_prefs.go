package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
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

// readUIPrefs loads the raw prefs blob from disk — the same file handleGetUIPrefs
// serves over HTTP — for same-process feature gates that don't need (and shouldn't
// pay for) an HTTP round trip. Any read/parse failure returns an empty map, same
// fallback as the handler, so a corrupt/missing file just means "nothing set".
func readUIPrefs() map[string]any {
	b, err := os.ReadFile(uiPrefsPath())
	if err != nil || len(b) == 0 {
		return map[string]any{}
	}
	var obj map[string]any
	if json.Unmarshal(b, &obj) != nil {
		return map[string]any{}
	}
	return obj
}

// autoTitleSuggestEnabled is the global ON/OFF for auto session-title suggestion
// (Console DisplayTab セッション). Missing/invalid key ⇒ true, matching the frontend's
// DEFAULTS.autoTitleSuggest (settings.ts) so pre-feature clients get it without an
// explicit opt-in.
func autoTitleSuggestEnabled() bool {
	v, ok := readUIPrefs()["autoTitleSuggest"].(bool)
	return !ok || v
}

// chatOutputLanguage returns the user's forced chat output language ("ja" | "en"),
// or "" when unset/"auto"/invalid — meaning "follow the input" (no language rule is
// injected, preserving the persona-driven default). Read live per turn from ui-prefs
// so a change takes effect on the next message of every conversation (not snapshotted).
func chatOutputLanguage() string {
	switch v, _ := readUIPrefs()["outputLanguage"].(string); v {
	case "ja", "en":
		return v
	default:
		return ""
	}
}

func handleGetUIPrefs(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, readUIPrefs())
}

func handlePutUIPrefs(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxUIPrefsBytes+1))
	if err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_request", "read failed")
		return
	}
	if len(body) > maxUIPrefsBytes {
		httpx.WriteErr(w, http.StatusRequestEntityTooLarge, "too_large", "prefs too large")
		return
	}
	// Validate it's a JSON object before persisting (the Console owns the shape).
	var obj map[string]any
	if json.Unmarshal(body, &obj) != nil {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_json", "body must be a JSON object")
		return
	}
	if err := os.MkdirAll(filepath.Dir(uiPrefsPath()), 0o700); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "mkdir_failed", err.Error())
		return
	}
	if err := os.WriteFile(uiPrefsPath(), body, 0o600); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "write_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, obj)
}
