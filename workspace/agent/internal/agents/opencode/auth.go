package opencode

import (
	"net/http"
	"os/exec"
	"regexp"
	"sort"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// opencode provider auth: mirrors the claude "settings-driven" model — the user
// pastes a provider API key in the Console, it's kept in the encrypted store
// (internal/secrets, at-rest sealed), and the Agent injects it as the provider's env var
// when it launches an opencode session. opencode natively reads provider keys from
// the environment (ANTHROPIC_API_KEY, OPENAI_API_KEY, …), so no auth.json is written
// and the key never lands in a plaintext file on the bind-mounted disk.

// envNameRe constrains the env var name to the conventional ALL_CAPS form so an
// arbitrary value can't be smuggled into the container environment.
var envNameRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,63}$`)

// opencodeKeyEnv is the one env var that pays opencode.ai — this single one for both Zen
// and Go.
const opencodeKeyEnv = "OPENCODE_API_KEY"

// UsagePref reports the selected billing route (ui-prefs opencodeCatalog). The Agent
// proper is what reads ui-prefs, so the reader is injected rather than having an internal
// package touch main's settings file. Unset means UsageOff: disabled until explicitly
// chosen.
var UsagePref = func() string { return UsageOff }

// env loads the stored provider keys as "NAME=value" entries for the
// session launcher to pass via `docker`/tmux `-e`. Order is stable (sorted).
//
// On the free tier (UsageFree) OPENCODE_API_KEY is dropped, so that a workspace which
// chose "use the free tier" cannot end up on a billed route merely because a key is still
// stored. Other providers' keys (ANTHROPIC_API_KEY and the like) are the user's own
// billing and are left alone. UsageOff drops every key — defense in depth: Connected()
// should already have stopped the caller, but if env() is reached on its own it must
// leave no billing or outbound path behind.
func env() []string {
	if UsagePref() == UsageOff {
		return nil
	}
	s, err := secrets.Load()
	if err != nil || len(s.Opencode) == 0 {
		return nil
	}
	free := UsagePref() == UsageFree
	names := make([]string, 0, len(s.Opencode))
	for k := range s.Opencode {
		if free && k == opencodeKeyEnv {
			continue
		}
		names = append(names, k)
	}
	sort.Strings(names)
	out := make([]string, 0, len(names))
	for _, k := range names {
		out = append(out, k+"="+s.Opencode[k])
	}
	return out
}

// Env is the exported form of env for the assistant chat's headless `opencode run`,
// which needs the same provider keys the interactive launcher injects.
func Env() []string { return env() }

// Available reports whether the opencode binary is on PATH at all — a much weaker
// signal than Connected (below). Used only for the Console Connections panel's
// "supported" field, which distinguishes "old image, binary missing" from "binary
// present but not connected".
func Available() bool {
	_, err := exec.LookPath("opencode")
	return err == nil
}

// connected is the shared "is opencode actually usable" formula: stored provider
// key(s), a completed account OAuth login, or the user's explicit opt-in to the
// zero-auth free tier (UsageFree, set under Settings > Agents > opencode "Route" —
// default is UsageOff, so a fresh workspace is NOT connected until the user
// configures something). Takes the already-loaded secrets/oauth state so callers
// that already have them (Status) don't reload.
//
// UsageOff overrides everything else to false, even a stored key or a live OAuth
// login — the point of an explicit "off" (as opposed to just never touching this
// setting) is a hard lock a security policy can rely on even if a key gets pasted in
// later without anyone flipping the route back.
func connected(s *secrets.Data, oa oauthState) bool {
	if UsagePref() == UsageOff {
		return false
	}
	return UsagePref() == UsageFree || len(s.Opencode) > 0 || oa.connected
}

// Connected reports whether opencode is actually usable — see connected() above. This
// is the single gate every entry point into opencode must honor before it runs a
// turn: registry.ts's kind availability (via Status below) AND headlessAgentAvailable
// (chat_providers.go). Unlike claude/codex, opencode's CLI does not hard-fail without
// credentials — it silently falls back to its own zero-auth free models (verified
// live: a fresh data dir answers via the free model) — so skipping this check is how
// assistant chat used to reach a third-party inference service the user never
// configured, which some tenants' security policy forbids. Default OFF, opt-in only.
func Connected() bool {
	s, err := secrets.Load()
	if err != nil {
		s = &secrets.Data{}
	}
	return connected(s, oauthStatus())
}

// Status reports which provider env vars are configured (names only,
// never the keys) for the Console Connections panel (GET /connections), plus the
// state of the second, independent path: the opencode Console account (OAuth device
// flow — oauth.go). connected answers "is opencode authenticated and usable", so either
// route makes it true; registry.ts's kind gate reads it.
func Status(s *secrets.Data) map[string]any {
	names := []string{}
	for k := range s.Opencode {
		names = append(names, k)
	}
	sort.Strings(names)
	oa := oauthStatus()
	usage := UsagePref()
	m := map[string]any{
		// connected is what decides whether this kind can be launched, for both
		// registry.ts and headlessAgentAvailable — the same formula Connected uses.
		"connected":      connected(s, oa),
		"envs":           names,
		"usage":          usage,
		"supported":      Available(), // no binary (old image) means not even the free tier can launch
		"oauth":          oa.connected,
		"oauth_known":    oa.known, // false = daemon not started, so unverified (not necessarily disconnected)
		"oauth_disabled": Serve().Disabled(),
	}
	if oa.label != "" {
		m["oauth_label"] = oa.label // the Console org name (label resolution, measured)
	}
	// The route to the usage page (docs/log/54 §54.7): the numbers cannot be fetched, so
	// this returns only the ID, the page URL, and whatever quota information was
	// observable when a limit was hit.
	if id, src := WorkspaceID(); id != "" {
		m["workspace_id"] = id
		m["workspace_id_source"] = src
		m["workspace_url"] = WorkspaceURL(id, "go")
	}
	if l := LastLimit(); l.Name != "" || l.ResetAt != "" {
		m["last_limit"] = l
	}
	return m
}

type connReq struct {
	Env string `json:"env"` // provider env var name, e.g. ANTHROPIC_API_KEY
	Key string `json:"key"` // the API key
}

// HandlePutConn stores a provider API key under its env var name
// (PUT /connections/opencode).
func HandlePutConn(w http.ResponseWriter, r *http.Request) {
	var req connReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	env := strings.TrimSpace(req.Env)
	if !envNameRe.MatchString(env) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_env", "env must be ALL_CAPS like ANTHROPIC_API_KEY")
		return
	}
	key := strings.TrimSpace(req.Key)
	if key == "" {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_key", "key is required")
		return
	}
	s, err := secrets.Load()
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	if s.Opencode == nil {
		s.Opencode = map[string]string{}
	}
	s.Opencode[env] = key
	if err := s.Save(); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	applyKeyChange("provider key stored: " + env)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"connected": true, "env": env})
}

// applyKeyChange propagates a stored-key change to the places that cached it.
//
// Keys are injected as env at launch (docs/log/27 §7), so storing one has NO effect on a
// serve daemon that is already running. Measured: deleting the key in the Console leaves
// the daemon holding it in its own environment, still reporting the env connection in
// connections[] and still listing the models that key can be billed for (restarting the
// Agent does not help either, since Ensure adopts the live daemon). The path that does
// apply it is generation++ plus a drain, i.e. Supervisor.Restart. The drain can take up
// to 60 seconds, so the handler does not wait and hands it to a separate goroutine.
func applyKeyChange(reason string) {
	InvalidateModels()
	go restartServe("opencode " + reason)
}

// restartServe is the seam tests replace (a real Restart drains live turns).
var restartServe = func(reason string) { Serve().Restart(reason) }

// ApplyUsageChange is applyKeyChange for a billing-route switch: entering or leaving the
// free tier changes whether OPENCODE_API_KEY is injected, so it needs the same
// propagation as a key change.
func ApplyUsageChange(reason string) { applyKeyChange("usage changed: " + reason) }

// HandleDeleteConn removes a stored provider key
// (DELETE /connections/opencode/{env}).
func HandleDeleteConn(w http.ResponseWriter, r *http.Request) {
	env := r.PathValue("env")
	if !envNameRe.MatchString(env) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_env", "invalid env name")
		return
	}
	s, err := secrets.Load()
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	delete(s.Opencode, env)
	if err := s.Save(); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	applyKeyChange("provider key removed: " + env)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"disconnected": env})
}
