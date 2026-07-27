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

var (
	tomlSectionRE = regexp.MustCompile(`^\s*\[([^]]+)]\s*(?:#.*)?$`)
	nudgeKeyRE    = regexp.MustCompile(`^\s*hide_rate_limit_model_nudge\s*=\s*(true|false)\s*(?:#.*)?$`)
)

// codexConfigPath is codex's own config file. It follows $CODEX_HOME like the CLI
// does (paths.CodexHome) — unset in the workspace, so this is ~/.codex/config.toml
// as before, and the same file the MCP registry materializes into (docs/48 §8).
func codexConfigPath() string {
	return filepath.Join(paths.CodexHome(), "config.toml")
}

// rateLimitModelNudgeEnabled follows Codex's default: an absent notice key means
// the model-switch reminder is shown.
func rateLimitModelNudgeEnabled(b []byte) bool {
	inNotice := false
	for _, line := range strings.Split(string(b), "\n") {
		if m := tomlSectionRE.FindStringSubmatch(line); m != nil {
			inNotice = m[1] == "notice"
			continue
		}
		if inNotice {
			if m := nudgeKeyRE.FindStringSubmatch(line); m != nil {
				return m[1] != "true"
			}
		}
	}
	return true
}

// setRateLimitModelNudge edits only Codex's notice key, preserving the rest of
// config.toml byte-for-byte (including comments and project trust sections).
func setRateLimitModelNudge(b []byte, enabled bool) []byte {
	lines := strings.Split(string(b), "\n")
	inNotice, noticeFound := false, false
	value := "false"
	if !enabled {
		value = "true"
	}
	entry := "hide_rate_limit_model_nudge = " + value
	for i, line := range lines {
		if m := tomlSectionRE.FindStringSubmatch(line); m != nil {
			if inNotice {
				lines = append(lines[:i], append([]string{entry}, lines[i:]...)...)
				return []byte(strings.Join(lines, "\n"))
			}
			inNotice = m[1] == "notice"
			if inNotice {
				noticeFound = true
			}
			continue
		}
		if inNotice && nudgeKeyRE.MatchString(line) {
			lines[i] = entry
			return []byte(strings.Join(lines, "\n"))
		}
	}
	if noticeFound {
		// Split preserves the trailing empty line, so insert before it.
		at := len(lines)
		if at > 0 && lines[at-1] == "" {
			at--
		}
		lines = append(lines[:at], append([]string{entry}, lines[at:]...)...)
		return []byte(strings.Join(lines, "\n"))
	}
	prefix := ""
	if len(b) > 0 {
		prefix = "\n"
		if !strings.HasSuffix(string(b), "\n") {
			prefix = "\n\n"
		}
	}
	return append(b, []byte(prefix+"[notice]\n"+entry+"\n")...)
}

func settingsBody() map[string]any {
	b, _ := os.ReadFile(codexConfigPath())
	return map[string]any{"rate_limit_model_nudge": rateLimitModelNudgeEnabled(b)}
}

// HandleSettingsGet reports user-editable Codex behavior settings.
func HandleSettingsGet(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, settingsBody())
}

type settingsReq struct {
	RateLimitModelNudge *bool `json:"rate_limit_model_nudge"`
}

// HandleSettingsPut updates Codex behavior settings without rewriting unrelated
// user configuration.
func HandleSettingsPut(w http.ResponseWriter, r *http.Request) {
	var req settingsReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if req.RateLimitModelNudge != nil {
		path := codexConfigPath()
		b, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			httpx.WriteErr(w, http.StatusInternalServerError, "read_failed", err.Error())
			return
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			httpx.WriteErr(w, http.StatusInternalServerError, "write_failed", err.Error())
			return
		}
		tmp := path + ".af-tmp"
		if err := os.WriteFile(tmp, setRateLimitModelNudge(b, *req.RateLimitModelNudge), 0o600); err != nil {
			httpx.WriteErr(w, http.StatusInternalServerError, "write_failed", err.Error())
			return
		}
		if err := os.Rename(tmp, path); err != nil {
			_ = os.Remove(tmp)
			httpx.WriteErr(w, http.StatusInternalServerError, "write_failed", err.Error())
			return
		}
	}
	httpx.WriteJSON(w, http.StatusOK, settingsBody())
}
