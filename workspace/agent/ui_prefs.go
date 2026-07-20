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

// assistantAgentOrderPref returns the user's assistant-chat backend priority (the
// AgentsTab 並べ替え UI, ui-prefs assistantAgentOrder), normalized into a TOTAL
// order: unknown kinds and dupes are dropped, and kinds missing from the stored
// list are appended in the built-in default order — so a partial or stale list
// (e.g. written before a new backend existed) still ranks every backend. When the
// key is absent, the legacy single-pin key (assistantAgent) degrades gracefully:
// a pin is simply "that backend first". Read live per call (like
// chatOutputLanguage) so a change applies from the next builtin-assistant
// conversation / one-shot call without a restart.
func assistantAgentOrderPref() []string {
	prefs := readUIPrefs()
	out := make([]string, 0, len(defaultHeadlessOrder))
	seen := map[string]bool{}
	add := func(k string) {
		if _, ok := chatProviders[k]; ok && !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	if raw, ok := prefs["assistantAgentOrder"].([]any); ok {
		for _, v := range raw {
			if s, ok := v.(string); ok {
				add(s)
			}
		}
	} else if pin, _ := prefs["assistantAgent"].(string); pin != "" {
		add(pin) // legacy pin ("auto" is not a kind, so it falls through to the default)
	}
	for _, k := range defaultHeadlessOrder {
		add(k)
	}
	return out
}

// chatAutoTurnEnabled is the global ON/OFF for the operator's automatic turn on a
// session report (docs/30, 設定 > エージェント「報告への自動応答」). Missing/invalid
// key ⇒ true, matching the frontend default — the feature ships ON, with the
// per-conversation maxAutoTurns cap as the safety stop.
func chatAutoTurnEnabled() bool {
	v, ok := readUIPrefs()["assistantAutoTurn"].(bool)
	return !ok || v
}

// chatAutoCompactEnabled is the global ON/OFF for the assistant chat's preventive
// auto-compaction at the context threshold (docs/33 第4段, 設定 > エージェント
// 「コンテキストの自動圧縮」). Missing/invalid key ⇒ true, matching the frontend
// default — the 80% notice gives the user a manual window first, and the summary
// handoff keeps the stored thread intact, so ON is the safe default.
func chatAutoCompactEnabled() bool {
	v, ok := readUIPrefs()["assistantAutoCompact"].(bool)
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
