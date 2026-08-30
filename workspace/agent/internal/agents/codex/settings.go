package codex

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

var tomlSectionRE = regexp.MustCompile(`^\s*\[([^]]+)]\s*(?:#.*)?$`)

// codexConfigPath is codex's own config file. It follows $CODEX_HOME like the CLI
// does (paths.CodexHome) — unset in the workspace, so this is ~/.codex/config.toml
// as before, and the same file the MCP registry materializes into (docs/log/48 §8).
func codexConfigPath() string {
	return filepath.Join(paths.CodexHome(), "config.toml")
}

// tomlBoolKeyRE matches `key = true|false` (with optional trailing comment) for a
// single key. Built per call — these files are a few KB and the edits are rare.
func tomlBoolKeyRE(key string) *regexp.Regexp {
	return regexp.MustCompile(`^\s*` + regexp.QuoteMeta(key) + `\s*=\s*(true|false)\s*(?:#.*)?$`)
}

// tomlBool reads section.key as a boolean. found=false when the section or the key
// is absent, so callers apply Codex's own default rather than guessing.
func tomlBool(b []byte, section, key string) (val, found bool) {
	keyRE := tomlBoolKeyRE(key)
	inSection := false
	for _, line := range strings.Split(string(b), "\n") {
		if m := tomlSectionRE.FindStringSubmatch(line); m != nil {
			inSection = m[1] == section
			continue
		}
		if inSection {
			if m := keyRE.FindStringSubmatch(line); m != nil {
				return m[1] == "true", true
			}
		}
	}
	return false, false
}

// tomlHasSection reports whether the file already declares [section]. Used to
// leave user-tuned tables strictly alone.
func tomlHasSection(b []byte, section string) bool {
	for _, line := range strings.Split(string(b), "\n") {
		if m := tomlSectionRE.FindStringSubmatch(line); m != nil && m[1] == section {
			return true
		}
	}
	return false
}

// tomlSetBool writes section.key = v, preserving the rest of config.toml
// byte-for-byte (including comments and [projects.*] trust sections). Codex owns
// this file; we only ever touch the one key the user toggled.
func tomlSetBool(b []byte, section, key string, v bool) []byte {
	keyRE := tomlBoolKeyRE(key)
	lines := strings.Split(string(b), "\n")
	inSection, sectionFound := false, false
	value := "false"
	if v {
		value = "true"
	}
	entry := key + " = " + value
	for i, line := range lines {
		if m := tomlSectionRE.FindStringSubmatch(line); m != nil {
			if inSection {
				lines = append(lines[:i], append([]string{entry}, lines[i:]...)...)
				return []byte(strings.Join(lines, "\n"))
			}
			inSection = m[1] == section
			if inSection {
				sectionFound = true
			}
			continue
		}
		if inSection && keyRE.MatchString(line) {
			lines[i] = entry
			return []byte(strings.Join(lines, "\n"))
		}
	}
	if sectionFound {
		// Split preserves the trailing empty line, so insert before it.
		at := len(lines)
		if at > 0 && lines[at-1] == "" {
			at--
		}
		lines = append(lines[:at], append([]string{entry}, lines[at:]...)...)
		return []byte(strings.Join(lines, "\n"))
	}
	return append(b, []byte(tomlAppendPrefix(b)+"["+section+"]\n"+entry+"\n")...)
}

// tomlAppendPrefix is the blank-line padding needed before appending a new table.
func tomlAppendPrefix(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	if strings.HasSuffix(string(b), "\n") {
		return "\n"
	}
	return "\n\n"
}

// rateLimitModelNudgeEnabled follows Codex's default: an absent notice key means
// the model-switch reminder is shown.
func rateLimitModelNudgeEnabled(b []byte) bool {
	hidden, found := tomlBool(b, "notice", "hide_rate_limit_model_nudge")
	return !found || !hidden
}

// setRateLimitModelNudge edits only Codex's notice key.
func setRateLimitModelNudge(b []byte, enabled bool) []byte {
	return tomlSetBool(b, "notice", "hide_rate_limit_model_nudge", !enabled)
}

func settingsBody() map[string]any {
	b, _ := os.ReadFile(codexConfigPath())
	return map[string]any{
		"rate_limit_model_nudge": rateLimitModelNudgeEnabled(b),
		"memories":               memoriesEnabled(b),
		// Whether Codex has actually materialized ~/.codex/memories yet — enabling
		// the flag only takes effect the next time a Codex session runs.
		"memories_ready": MemoriesMaterialized(),
	}
}

// HandleSettingsGet reports user-editable Codex behavior settings.
func HandleSettingsGet(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, settingsBody())
}

type settingsReq struct {
	RateLimitModelNudge *bool `json:"rate_limit_model_nudge"`
	Memories            *bool `json:"memories"`
}

// HandleSettingsPut updates Codex behavior settings without rewriting unrelated
// user configuration. Every requested key is folded into one read-modify-write so
// a multi-key request cannot leave the file half-updated.
func HandleSettingsPut(w http.ResponseWriter, r *http.Request) {
	var req settingsReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if req.RateLimitModelNudge == nil && req.Memories == nil {
		httpx.WriteJSON(w, http.StatusOK, settingsBody())
		return
	}
	path := codexConfigPath()
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		httpx.WriteErr(w, http.StatusInternalServerError, "read_failed", err.Error())
		return
	}
	if req.RateLimitModelNudge != nil {
		b = setRateLimitModelNudge(b, *req.RateLimitModelNudge)
	}
	if req.Memories != nil {
		b = setMemories(b, *req.Memories)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "write_failed", err.Error())
		return
	}
	tmp := path + ".af-tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "write_failed", err.Error())
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		httpx.WriteErr(w, http.StatusInternalServerError, "write_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, settingsBody())
}
