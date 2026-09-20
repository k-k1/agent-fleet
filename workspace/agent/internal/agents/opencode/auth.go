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
// On the two routes that declare opencode.ai is not being used — UsageFree (the zero-auth
// route) and UsageOwn (direct providers only) — OPENCODE_API_KEY is dropped, so that such a
// workspace cannot end up on a billed route merely because a key is still stored. Other
// providers' keys (ANTHROPIC_API_KEY and the like) are the user's own
// billing and are left alone. UsageOff drops every key — defense in depth: Connected()
// should already have stopped the caller, but if env() is reached on its own it must
// leave no billing or outbound path behind.
func env() []string {
	if UsagePref() == UsageOff {
		return nil
	}
	var out []string
	// ⚠️ NOT an early return when there are no stored keys. There used to be one, and it hid
	// the engine token below from every workspace that has none — which is the free tier and
	// the Console-OAuth login, i.e. the common case. The stored keys and the fleet's own
	// engine are independent: a workspace can have no provider key at all and still be
	// entitled to the engine. Same for an unreadable store: that is a reason to lose the keys,
	// not a reason to lose the engine.
	if s, err := secrets.Load(); err == nil {
		noOpencodeAI := UsagePref() == UsageFree || UsagePref() == UsageOwn
		names := make([]string, 0, len(s.Opencode))
		for k := range s.Opencode {
			if noOpencodeAI && k == opencodeKeyEnv {
				continue
			}
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			out = append(out, k+"="+s.Opencode[k])
		}
	}
	// The fleet's own engines (ADR 0071). It has to be HERE and not only on LaunchPlan.Env,
	// because opencode's managed route — the default one — runs every session through a single
	// shared `opencode serve` daemon whose environment comes from exactly this function. A
	// token placed only on the launch plan reaches the tmux route and nothing else, and the
	// symptom is `401 invalid engine session token` on the first message of a managed session
	// (measured on the live deployment, which is how this was found).
	//
	// Workspace-scoped for the same reason: one daemon serves every session in the workspace,
	// so there is no session to scope it to at this point. BuildLaunch overrides it with a
	// session-scoped one where the route allows that.
	return append(out, EngineEnv("")...)
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
	switch UsagePref() {
	case UsageOff:
		return false
	case UsageOwn:
		// opencode.ai is not used on this route, so neither an account login nor a stored
		// OPENCODE_API_KEY makes opencode usable here — what does is a provider the user
		// connected directly, or one of the fleet's own engines (ADR 0071). Being strict is
		// the safe direction: reporting connected with nothing behind it would let a launch
		// fall through to opencode's own default model, which is a zero-auth opencode.ai one
		// — exactly the unasked-for outside call this setting exists to prevent.
		return hasDirectProviderKey(s) || HasEngineProviders()
	}
	return UsagePref() == UsageFree || len(s.Opencode) > 0 || oa.connected
}

// hasDirectProviderKey reports whether any stored key belongs to a provider other than
// opencode.ai itself (ANTHROPIC_API_KEY and the like — the user's own bill).
func hasDirectProviderKey(s *secrets.Data) bool {
	for k := range s.Opencode {
		if k != opencodeKeyEnv {
			return true
		}
	}
	return false
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
	// The daemon's recent lifetime (serve.go's ledger). It is here because this is the one
	// opencode-shaped surface a person inside the workspace can already read: a daemon that
	// keeps being replaced kills a turn every time it lands on one, and until now the only
	// record of that was the container's stdout.
	if ev := Lifecycle(); len(ev) > 0 {
		m["lifecycle"] = ev
	}
	// Settings changed since the running daemon started. The Console turns this into the
	// notice and the restart button; absent means everything stored is what serve is using.
	if p, ok := PendingRestartInfo(); ok {
		m["restart_required"] = p
	}
	return m
}

// HandleServeRestart applies the pending settings by replacing the serve daemon
// (POST /connections/opencode/serve/restart). Sessions mid-turn are drained first, and one
// still running when the drain times out is cut short, so this is only ever taken on a
// person's own initiative.
func HandleServeRestart(w http.ResponseWriter, r *http.Request) {
	if !Serve().Restart("opencode restart requested from the Console") {
		// An adopted daemon cannot be signalled, so the old environment is still in force.
		// Reporting success here would tell the user their key change had landed when it
		// had not.
		httpx.WriteErr(w, http.StatusConflict, "serve_not_owned",
			"この serve は別プロセスが起動したもので、Agent からは入れ替えられません（ワークスペースの再起動が要ります）")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"restarted": true})
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
// Keys are injected as env at launch (docs/log/27 §7), so storing one has no effect on a
// serve daemon that is already running. Measured: deleting the key in the Console leaves
// the daemon holding it in its own environment, still reporting the env connection in
// connections[] and still listing the models that key can be billed for. Only a new process
// applies it, i.e. Supervisor.Restart.
//
// That restart is NOT taken here. It drains, and a turn still running when the drain times
// out is killed — a price the person who just changed a setting is the one able to judge.
// So the change is recorded and the Console offers the button (Settings → Agents).
func applyKeyChange(reason string) {
	InvalidateModels()
	noteRestart("opencode " + reason)
}

// noteRestart is the seam tests replace.
var noteRestart = NotePendingRestart

// ApplyUsageChange is applyKeyChange for a billing-route switch: entering or leaving the
// free tier changes whether OPENCODE_API_KEY is injected, so it needs the same
// propagation as a key change.
func ApplyUsageChange(reason string) { applyKeyChange("usage changed: " + reason) }

// ApplyEngineChange is applyKeyChange for the fleet's own engines (ADR 0071): the serve
// daemon reads the provider block and resolves `{env:AF_ENGINE_TOKEN}` ONCE, at start, so a
// daemon that came up before the engine catalogue was written knows nothing about it and
// every managed turn on that model fails. Restart is a no-op when nothing is running, which
// is the ordinary case at boot — this only pays for itself when the catalogue changes under a
// live daemon.
func ApplyEngineChange(reason string) { applyKeyChange("engines changed: " + reason) }

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
