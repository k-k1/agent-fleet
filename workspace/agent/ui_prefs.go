package main

// Only the ui-prefs accessors that depend on main's own feature code (chatProviders /
// defaultAutoTurns / modelHidden / materializeMCPAll / agentOf), plus the HTTP handlers, live
// here. Reading and writing the prefs themselves, and every accessor that depends on nothing,
// call internal/uiprefs directly.

import (
	"encoding/json"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
)

// skipPermissionsPref is the per-kind default for "skip the permission prompt" (Settings >
// Agents > each card, ui-prefs agentLaunchDefaults[<kind>].skipPermissions — docs/log/76).
// ok=false means "no setting for that kind"; the default value itself belongs to
// agents.SkipPermissions (true, as before).
//
// Read in-process rather than over HTTP so the same default applies to the launch paths that
// are not the Console — MCP's create_session, scheduled execution, restarting a stopped
// session, fork/recreate. Resolving it on the Console side alone produces "I turned it off in
// the settings, yet the session the schedule started runs with bypass".
func skipPermissionsPref(kind string) (bool, bool) {
	k := sessionx.NormalizeKind(kind)
	// Kinds whose pending approvals cannot be answered from the Console (codex / opencode) are
	// not eligible for the choice. Even if an old or corrupt prefs file wrote false for such a
	// kind, drop it here and launch with bypass as before — reliably better than wedging on an
	// approval dialog nobody can answer.
	if !sessionx.AgentOf(k).Caps().PermissionChoice {
		return false, false
	}
	defs, ok := uiprefs.Read()["agentLaunchDefaults"].(map[string]any)
	if !ok {
		return false, false
	}
	row, ok := defs[k].(map[string]any)
	if !ok {
		return false, false
	}
	v, ok := row["skipPermissions"].(bool)
	return v, ok
}

// internal/agents does not read main's settings files itself, so this is handed over as a hook,
// the same way opencode.UsagePref and mcpreg.PeerMessagingEnabled are.
func init() { agents.SkipPermissionsPref = skipPermissionsPref }

// agentOrderPref normalizes a stored priority list into a TOTAL order: unknown kinds
// and dupes are dropped, and kinds missing from the stored list are appended in the
// built-in default order — so a partial or stale list (e.g. written before a new
// backend existed) still ranks every backend. Read live per call (like
// chatOutputLanguage) so a change applies to the next conversation / one-shot without
// a restart.
//
// keys is a first-wins fallback chain (new key → old key). It is a chain rather than a single
// list so that, once chat and AI-assist generation got separate priorities (docs/log/84), the
// assist side still inherits from a prefs file that only has the old single list.
func agentOrderPref(keys ...string) []string {
	prefs := uiprefs.Read()
	out := make([]string, 0, len(chatx.DefaultHeadlessOrder))
	seen := map[string]bool{}
	add := func(k string) {
		if _, ok := chatx.ChatProviders[k]; ok && !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	stored := false
	for _, key := range keys {
		raw, ok := prefs[key].([]any)
		if !ok {
			continue
		}
		for _, v := range raw {
			if s, ok := v.(string); ok {
				add(s)
			}
		}
		stored = true
		break
	}
	if !stored {
		// legacy pin ("auto" is not a kind, so it falls through to the default)
		if pin, _ := prefs["assistantAgent"].(string); pin != "" {
			add(pin)
		}
	}
	for _, k := range chatx.DefaultHeadlessOrder {
		add(k)
	}
	return out
}

// assistantAgentOrderPref is the priority for assistant-CHAT conversations
// (the AssistantTab reordering UI, ui-prefs assistantAgentOrder).
func assistantAgentOrderPref() []string { return agentOrderPref("assistantAgentOrder") }

// aiAssistOrderPref is the priority for the AI-assist one-shots — titles, branch
// names, reply chips, edit suggestions, plan updates (Settings > AI assist, ui-prefs
// aiAssistOrder). Separate from the chat because the two want opposite things: the
// chat wants the strongest CLI, these run constantly and want the cheapest that
// works. Falls back to assistantAgentOrder, which is where both used to live.
func aiAssistOrderPref() []string { return agentOrderPref("aiAssistOrder", "assistantAgentOrder") }

// assistantModelPref returns a per-backend model selected in AssistantTab. The
// boolean distinguishes a missing (pre-feature) map from an explicit empty value:
// empty means "let this CLI choose its default", while missing keeps the historical
// backend-specific defaults.
func assistantModelPref(key, kind string) (string, bool) {
	raw, ok := uiprefs.Read()[key].(map[string]any)
	if !ok {
		return "", false
	}
	v, ok := raw[kind].(string)
	if !ok {
		return "", false
	}
	// A value hidden by "models not to use" is never adopted, even if it is still in the prefs
	// (model_deny.go). Dropping it to "unset" makes the caller fall back to the recommended
	// model or the CLI default. "recommended" is a sentinel rather than a real model id, so it
	// passes through untouched.
	if v != chatx.AssistantRecommendedModel && sessionx.ModelHidden(kind, v) {
		return "", false
	}
	return v, true
}

func assistantChatModelPref(kind string) (string, bool) {
	return assistantModelPref("assistantModels", kind)
}

// aiShortModelPref / aiProseModelPref split the AI-assist model in two by purpose
// (Settings > AI assist, docs/log/84): short is for short labels (titles, branch names, reply
// suggestions), prose is for text a person reads and adopts (file edit suggestions, plan
// updates).
//
// BOTH inherit from the old assistantUtilityModels key, which covered the two at once and,
// under a name that only spoke of titles and suggestions, also replaced the prose-side default
// (sonnet class). Inheriting it on both sides is what carries the pre-split behaviour over
// unchanged; the point of the split is that the two can now move independently.
func aiShortModelPref(kind string) (string, bool) {
	return aiModelPref("aiShortModels", kind)
}

func aiProseModelPref(kind string) (string, bool) {
	return aiModelPref("aiProseModels", kind)
}

func aiModelPref(key, kind string) (string, bool) {
	if v, ok := assistantModelPref(key, kind); ok {
		return v, true
	}
	return assistantModelPref("assistantUtilityModels", kind)
}

// chatAutoTurnLimit is the per-conversation ceiling on unattended auto turns
// (docs/log/30, Settings > Assistant, "auto-reply limit"). Missing/invalid ⇒
// defaultAutoTurns; always clamped to [1, maxAutoTurnLimit] — there is no
// unlimited mode, the clamp is the runaway stop.
func chatAutoTurnLimit() int {
	v, ok := uiprefs.Read()["assistantAutoTurnLimit"].(float64)
	if !ok {
		return chatx.DefaultAutoTurns
	}
	n := int(v)
	if n < 1 {
		return 1
	}
	if n > chatx.MaxAutoTurnLimit {
		return chatx.MaxAutoTurnLimit
	}
	return n
}

// chatAutoTurnModel is the dedicated model for the operator's automatic turns
// (Settings > Assistant, "auto-reply model"). Empty = keep the conversation's model. Checking
// and summarising a report is routine work, so it can be moved to a light model (haiku and
// the like) — auto turns outnumber user turns (measured: 121 vs 107 over 5 days), so the unit
// price here goes straight to the bill. Applies to claude conversations only (codex/opencode
// read c.Model directly and have no override point — gated in runReportAutoTurn).
func chatAutoTurnModel() string {
	v, _ := uiprefs.Read()["assistantAutoTurnModel"].(string)
	// A claude-only setting, so it is judged against claude's hidden-model list. Hidden ⇒ empty,
	// i.e. keep the conversation's model (model_deny.go).
	return sessionx.VisibleModel(session.KindClaude, strings.TrimSpace(v))
}

func handleGetUIPrefs(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, uiprefs.Read())
}

func handlePutUIPrefs(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, uiprefs.MaxBytes+1))
	if err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_request", "read failed")
		return
	}
	if len(body) > uiprefs.MaxBytes {
		httpx.WriteErr(w, http.StatusRequestEntityTooLarge, "too_large", "prefs too large")
		return
	}
	// Validate it's a JSON object before persisting (the Console owns the shape).
	var obj map[string]any
	if json.Unmarshal(body, &obj) != nil {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_json", "body must be a JSON object")
		return
	}
	if err := os.MkdirAll(filepath.Dir(uiprefs.Path()), 0o700); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "mkdir_failed", err.Error())
		return
	}
	// Only a write that shrinks accumulated data keeps a copy of the previous version before
	// replacing it. Copying on every write would flush the version worth restoring on the very
	// next one — what matters is holding on to the state just before the accident.
	if lost := uiprefs.ShrunkKeys(uiprefs.Read(), obj); len(lost) > 0 {
		if old, err := os.ReadFile(uiprefs.Path()); err == nil && len(old) > 0 {
			if err := os.WriteFile(uiprefs.BackupPath(), old, 0o600); err != nil {
				log.Printf("ui-prefs: backup before shrinking write failed: %v", err)
			}
		}
		log.Printf("ui-prefs: incoming prefs drop accumulated keys %v (previous copy kept at %s)",
			lost, uiprefs.BackupPath())
	}
	before := uiprefs.OpencodeCatalog()
	peerBefore := uiprefs.PeerMessaging()
	if err := os.WriteFile(uiprefs.Path(), body, 0o600); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "write_failed", err.Error())
		return
	}
	// Toggling peer messaging (docs/log/58) changes the launch argument of the session-side af
	// server (--peer-messaging). Each CLI's native MCP config is written at materialize time, so
	// without rewriting it here the toggle does nothing at all. Sessions already running have
	// read their config, so it takes effect from the next session launched (which is what the UI
	// text says too).
	if uiprefs.PeerMessaging() != peerBefore {
		mcpx.MaterializeAll()
	}
	// Switching the tier changes the env that is injected (the free tier drops
	// OPENCODE_API_KEY). Treated like a key change: without recreating the running serve, it
	// keeps the old environment.
	if after := uiprefs.OpencodeCatalog(); after != before {
		opencode.ApplyUsageChange(before + " → " + after)
	}
	httpx.WriteJSON(w, http.StatusOK, obj)
}
