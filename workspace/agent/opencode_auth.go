package main

import (
	"net/http"
	"regexp"
	"sort"
	"strings"
)

// opencode provider auth: mirrors the claude "settings-driven" model — the user
// pastes a provider API key in the Console, it's kept in the encrypted store
// (secrets.go, at-rest sealed), and the Agent injects it as the provider's env var
// when it launches an opencode session. opencode natively reads provider keys from
// the environment (ANTHROPIC_API_KEY, OPENAI_API_KEY, …), so no auth.json is written
// and the key never lands in a plaintext file on the bind-mounted disk.

// envNameRe constrains the env var name to the conventional ALL_CAPS form so an
// arbitrary value can't be smuggled into the container environment.
var envNameRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,63}$`)

// opencodeEnv loads the stored provider keys as "NAME=value" entries for the
// session launcher to pass via `docker`/tmux `-e`. Order is stable (sorted).
func opencodeEnv() []string {
	s, err := loadSecrets()
	if err != nil || len(s.Opencode) == 0 {
		return nil
	}
	names := make([]string, 0, len(s.Opencode))
	for k := range s.Opencode {
		names = append(names, k)
	}
	sort.Strings(names)
	out := make([]string, 0, len(names))
	for _, k := range names {
		out = append(out, k+"="+s.Opencode[k])
	}
	return out
}

// opencodeStatus reports which provider env vars are configured (names only,
// never the keys) for the Console Connections panel.
func opencodeStatus(s *secretsData) map[string]any {
	names := []string{}
	for k := range s.Opencode {
		names = append(names, k)
	}
	sort.Strings(names)
	return map[string]any{"connected": len(names) > 0, "envs": names}
}

type opencodeConnReq struct {
	Env string `json:"env"` // provider env var name, e.g. ANTHROPIC_API_KEY
	Key string `json:"key"` // the API key
}

// handlePutOpencodeConn stores a provider API key under its env var name.
func handlePutOpencodeConn(w http.ResponseWriter, r *http.Request) {
	var req opencodeConnReq
	if !decodeJSON(w, r, &req) {
		return
	}
	env := strings.TrimSpace(req.Env)
	if !envNameRe.MatchString(env) {
		writeErr(w, http.StatusBadRequest, "bad_env", "env must be ALL_CAPS like ANTHROPIC_API_KEY")
		return
	}
	key := strings.TrimSpace(req.Key)
	if key == "" {
		writeErr(w, http.StatusBadRequest, "bad_key", "key is required")
		return
	}
	s, err := loadSecrets()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	if s.Opencode == nil {
		s.Opencode = map[string]string{}
	}
	s.Opencode[env] = key
	if err := s.save(); err != nil {
		writeErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"connected": true, "env": env})
}

// handleDeleteOpencodeConn removes a stored provider key.
func handleDeleteOpencodeConn(w http.ResponseWriter, r *http.Request) {
	env := r.PathValue("env")
	if !envNameRe.MatchString(env) {
		writeErr(w, http.StatusBadRequest, "bad_env", "invalid env name")
		return
	}
	s, err := loadSecrets()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	delete(s.Opencode, env)
	if err := s.save(); err != nil {
		writeErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"disconnected": env})
}
