package main

// engines.go — the Workspace side of the fleet's own inference engines (ADR 0071 P0).
//
// Three small jobs, all of them talking to the Control Plane over the same public hairpin
// the memo, schedule and MCP bridges use (AF_CP_BASE_URL), authenticated by the issuing
// token the CP injects at container start (AF_ENGINE_ISSUE_TOKEN):
//
//  1. at boot, ask which engines exist and write them into opencode's config as a provider,
//     so `llamacpp/<model>` is in the launch menu — while every engine is still asleep;
//  2. at each opencode launch, buy a token scoped to THAT session and hand it to the pane
//     through tmux's environment;
//  3. accept the usage rows the CP posts back after an engine answers, and append them to
//     this workspace's ledger, which is where every other feature's consumption lives.
//
// A deployment with no engines gets 404 on the catalog and everything here is a no-op. That
// is the normal case: a GPU box is $1.26/hour and nobody deploys one by accident.

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

var engineHTTP = &http.Client{Timeout: 20 * time.Second}

// The opencode launcher calls this for every session it starts. Wired here rather than in
// the opencode package because minting the token is a call to the Control Plane, and an
// internal CLI package has no business knowing the CP exists (the same seam UsagePref uses).
func init() { opencode.EngineEnv = engineSessionEnv }

// engineCPCall is the shared shape of the two calls out: base URL, issuing token, JSON in
// and out. Returns ok=false with no error logged for "this deployment has no engines" (404)
// and for "there is no CP to ask" (a dev agent with no AF_CP_BASE_URL).
func engineCPCall(ctx context.Context, method, path string, in, out any) bool {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("AF_CP_BASE_URL")), "/")
	token := strings.TrimSpace(os.Getenv("AF_ENGINE_ISSUE_TOKEN"))
	if base == "" || token == "" {
		return false
	}
	var body *bytes.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return false
		}
		body = bytes.NewReader(b)
	} else {
		body = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, body)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := engineHTTP.Do(req)
	if err != nil {
		log.Printf("engines: %s %s failed: %v", method, path, err)
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return false // no engines in this deployment: not a problem, and not worth a line
	}
	if resp.StatusCode >= 300 {
		log.Printf("engines: %s %s answered %s", method, path, resp.Status)
		return false
	}
	if out == nil {
		return true
	}
	return json.NewDecoder(resp.Body).Decode(out) == nil
}

// syncEngineProviders asks the CP for the engine catalogue and writes it into opencode's
// config. Called once at boot: the catalogue only changes when the 60-engines stack does,
// and that replaces the CP task, whose next workspace start runs this again.
func syncEngineProviders() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var cat struct {
		Engines []struct {
			Key      string   `json:"key"`
			Provider string   `json:"provider"`
			BaseURL  string   `json:"base_url"`
			Models   []string `json:"models"`
		} `json:"engines"`
	}
	if !engineCPCall(ctx, http.MethodGet, "/internal/engine/catalog", nil, &cat) {
		return
	}
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("AF_CP_BASE_URL")), "/")
	providers := make([]opencode.EngineProvider, 0, len(cat.Engines))
	for _, e := range cat.Engines {
		providers = append(providers, opencode.EngineProvider{
			Key: e.Key, Provider: e.Provider, BaseURL: base + e.BaseURL, Models: e.Models,
		})
	}
	changed, err := opencode.WriteEngineProviders(providers)
	if err != nil {
		log.Printf("engines: writing the opencode provider failed: %v", err)
		return
	}
	if changed {
		names := make([]string, 0, len(providers))
		for _, p := range providers {
			names = append(names, p.Provider+" ("+strings.Join(p.Models, ",")+")")
		}
		log.Printf("engines: opencode provider written: %s", strings.Join(names, "; "))
	}
}

// --- the per-session token ------------------------------------------------------

// engineTokenCache holds one token per scope — a session name, or "" for the workspace-wide
// one the managed route's shared daemon uses. A launch is not the only thing that asks
// (`opencode models` asks on every launch-modal open), and buying a new token each time would
// leave a trail of live credentials behind one session.
var engineTokenCache sync.Map // scope -> engineCachedToken

type engineCachedToken struct {
	value string
	// renewAt is deliberately well before the token's own expiry: a session relaunched at
	// the last minute must not be handed a credential that dies inside the first answer.
	renewAt time.Time
	// failedAt marks a negative entry. This is called from `opencode models`, which is on the
	// launch modal's path, so a Control Plane that cannot be reached must cost one timeout and
	// not one per modal open.
	failedAt time.Time
}

// engineNegativeCache is how long a failed mint is remembered. Short enough that a CP which
// has just come back is picked up on the next launch, long enough that a modal opened
// repeatedly does not stall repeatedly.
const engineNegativeCache = time.Minute

// engineSessionEnv mints (or reuses) an engine token for `name`, as KEY=VALUE entries.
//
// An EMPTY name asks for the workspace-scoped token, which is what the managed route needs:
// its `opencode serve` daemon is shared by every session in the workspace, so there is no
// session to scope it to. A named session gets a session-scoped one.
//
// Nil when the deployment runs no engines, which is what makes it safe to call
// unconditionally from the launcher and from env().
func engineSessionEnv(name string) []string {
	if v, ok := engineTokenCache.Load(name); ok {
		c := v.(engineCachedToken)
		if !c.failedAt.IsZero() && time.Since(c.failedAt) < engineNegativeCache {
			return nil
		}
		if c.value != "" && time.Now().Before(c.renewAt) {
			return []string{opencode.EngineProviderKeyEnv + "=" + c.value}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var out struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
	}
	req := map[string]string{"session": name, "key": "llm"}
	if !engineCPCall(ctx, http.MethodPost, "/internal/engine/token", req, &out) || out.Token == "" {
		engineTokenCache.Store(name, engineCachedToken{failedAt: time.Now()})
		return nil
	}
	renew := time.Now().Add(time.Hour)
	if exp, err := time.Parse(time.RFC3339, out.ExpiresAt); err == nil {
		if half := time.Until(exp) / 2; half > 0 {
			renew = time.Now().Add(half)
		}
	}
	engineTokenCache.Store(name, engineCachedToken{value: out.Token, renewAt: renew})
	return []string{opencode.EngineProviderKeyEnv + "=" + out.Token}
}

// --- the usage the CP posts back -------------------------------------------------

// engineUsageReq is what control-plane/engine_usage.go sends. The CP is the only party that
// sees an engine's response, so it is the only party that can count the tokens; the ledger
// they belong in is here, next to every other feature's rows (ADR 0029).
type engineUsageReq struct {
	Feature  string `json:"feature"`  // "engine.llm"
	Provider string `json:"provider"` // "llamacpp"
	Session  string `json:"session"`
	Model    string `json:"model"`
	In       int    `json:"in"`
	Out      int    `json:"out"`
	MS       int    `json:"ms"`
	OK       bool   `json:"ok"`
	Measured string `json:"measured"`
}

// handleEngineUsage appends one engine call to the ledger (POST /engine/usage). Called by
// the CP itself, like /work-items/fetch and /notifications — never by the Console, so it
// needs no entry in the CP's agent-proxy allowlist.
func handleEngineUsage(w http.ResponseWriter, r *http.Request) {
	var req engineUsageReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	feature := strings.TrimSpace(req.Feature)
	// Only the engine features, and only ones this Agent recognises. The route is
	// authenticated, but "whatever the caller called it" would let a mislabelled row into a
	// graph whose categories are a frozen enumeration (ADR 0029 §2).
	if !strings.HasPrefix(feature, "engine.") {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_feature", "feature must be engine.<key>")
		return
	}
	measured := usagex.MeasuredExact
	if req.Measured != usagex.MeasuredExact || (req.In == 0 && req.Out == 0) {
		// An engine that reported no usage and an engine that reported zero spent are
		// different facts, and merging them puts free calls into the graph.
		measured = usagex.MeasuredNone
	}
	row := usagex.Record{
		TS:      time.Now().UTC().Format(time.RFC3339),
		Call:    chatx.RandUUID(),
		Feature: feature,
		Trigger: usagex.TriggerUser,
		// Kind is the agent kind that ran, i.e. what was driving the session — not the
		// engine. The engine is the model's provenance and rides on Model/ModelSrc.
		Kind:     engineSessionKind(req.Session),
		Model:    strings.TrimSpace(req.Model),
		ModelRaw: strings.TrimSpace(req.Model),
		ModelSrc: usagex.ModelReported,
		Ref:      strings.TrimSpace(req.Session),
		In:       req.In,
		Out:      req.Out,
		Spend:    req.In + req.Out,
		MS:       req.MS,
		OK:       req.OK,
		Measured: measured,
	}
	if err := usagex.AppendRows([]usagex.Record{row}); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "ledger_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"recorded": true})
}

// engineSessionKind resolves which CLI the session runs, so the row lands in the same
// dimension as that session's other consumption. An unknown session is recorded as opencode
// rather than dropped: opencode is the only kind P0 wires an engine to, and losing the row
// over a name lookup is worse than a coarse one.
func engineSessionKind(name string) string {
	if m, ok := session.ReadMeta(strings.TrimSpace(name)); ok && m.Kind != "" {
		return m.Kind
	}
	return session.KindOpencode
}
