package chatx

import (
	"context"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/agy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/cursor"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/modelfallback"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// chatBackends is every headless chat backend by kind, the one list of them. The chat
// backends are chatx's own code, so their per-kind behaviour lives here as optional methods on
// the provider types rather than on agents.Agent (an agent package cannot import chatx).
//
// ChatProviders starts as a copy that tests swap Send stand-ins into. The capabilities are read
// from this map instead, so a swapped Send never changes availability, one-shots or
// recommendations — exactly as when each of them was a switch on the kind.
var chatBackends = map[string]ChatProvider{
	session.KindClaude:   ClaudeChat{},
	session.KindCodex:    codexChat{},
	session.KindOpencode: opencodeChat{},
	session.KindAgy:      agyChat{},
	session.KindCursor:   cursorChat{},
	lcppKind:             lcppChat{},
	session.KindMuse:     museChat{},
}

// backend is kind's production chat backend, nil when it has none.
func backend(kind string) ChatProvider { return chatBackends[kind] }

// kindedBackend names the kind a production backend drives (ChatProviderKind).
type kindedBackend interface{ kind() string }

// headlessChecker is a backend that can tell whether it is usable headless right now (a CLI
// login, a configured engine). It may shell out; headlessAgentAvailable caches it.
type headlessChecker interface{ headlessAvailable() bool }

// oneShotRunner is a backend that runs AI-assist one-shots (oneShotRunOn). A kind without one
// is never selected for a one-shot: it used to fall through to claude's path and be billed
// there (#1020).
type oneShotRunner interface {
	runOneShot(ctx context.Context, call *usagex.Call, run oneShotRun) (string, error)
}

// utilityRecommender is a backend with a computed "recommended" model for the short tier
// (recommendedUtilityModel); the rest answer "", the CLI default.
type utilityRecommender interface {
	recommendedUtility(v visibility) string
}

// safeOneShotModeler is a backend whose one-shot never runs on the CLI default: with no model
// named it falls back to safeOneShotModel, which is therefore also its recommendation.
type safeOneShotModeler interface {
	safeOneShotModel(v visibility) string
}

// envModelNamer is a backend whose operator override (AF_TITLE_MODEL_*) may be a display name
// that namedEnvModel resolves against its catalog; the rest filter it by visibility alone.
type envModelNamer interface {
	namedEnvModel(v visibility, m string) string
}

// callerModeler is a backend whose one-shot model, when the settings name none, is the one the
// caller passed (claudeModel), not the recommendation.
type callerModeler interface{ takesCallerModel() }

func (ClaudeChat) kind() string   { return session.KindClaude }
func (codexChat) kind() string    { return session.KindCodex }
func (opencodeChat) kind() string { return session.KindOpencode }
func (agyChat) kind() string      { return session.KindAgy }
func (cursorChat) kind() string   { return session.KindCursor }
func (lcppChat) kind() string     { return lcppKind }
func (museChat) kind() string     { return session.KindMuse }

func (ClaudeChat) headlessAvailable() bool { return claude.LoggedIn() }
func (codexChat) headlessAvailable() bool  { return codex.LoggedIn() }

// headlessAvailable: binary present AND actually usable (stored key / oauth / explicit
// free-tier opt-in) — opencode.Available() alone used to let assistant chat silently fall back
// to opencode's zero-auth free tier for any unconfigured workspace, which some tenants'
// security policy forbids. Default OFF, opt-in only.
func (opencodeChat) headlessAvailable() bool { return opencode.Available() && opencode.Connected() }
func (agyChat) headlessAvailable() bool      { return agy.SignedIn() }
func (cursorChat) headlessAvailable() bool   { return cursor.LoggedIn() }

// headlessAvailable is not a CLI login check — there is no CLI (ADR 0093 phase 1 §2).
// "available" here means this deployment's self-hosted chat engine exists AND has at least one
// enabled model right now; see lcppEngineAvailable's own comment for why that is the whole
// check (the Control Plane's catalogue already drops an engine with none).
func (lcppChat) headlessAvailable() bool { return lcppEngineAvailable() }
func (museChat) headlessAvailable() bool { return museAvailable() }

func (ClaudeChat) runOneShot(ctx context.Context, call *usagex.Call, run oneShotRun) (string, error) {
	return oneShotOnClaude(ctx, call, run)
}
func (codexChat) runOneShot(ctx context.Context, call *usagex.Call, run oneShotRun) (string, error) {
	return oneShotOnCodex(ctx, call, run)
}
func (opencodeChat) runOneShot(ctx context.Context, call *usagex.Call, run oneShotRun) (string, error) {
	return oneShotOnOpencode(ctx, call, run)
}
func (cursorChat) runOneShot(ctx context.Context, call *usagex.Call, run oneShotRun) (string, error) {
	return oneShotOnCursor(ctx, call, run)
}
func (agyChat) runOneShot(ctx context.Context, call *usagex.Call, run oneShotRun) (string, error) {
	return oneShotOnAgy(ctx, call, run)
}
func (museChat) runOneShot(ctx context.Context, call *usagex.Call, run oneShotRun) (string, error) {
	return oneShotOnMuse(ctx, call, run)
}

// recommendedUtility: claude needs no ranking, the "haiku" alias already moves to each new Haiku.
func (ClaudeChat) recommendedUtility(v visibility) string {
	return claudeFirstVisible(v, claudeShortTiers)
}

// recommendedUtility: the cheapest priced model codex's catalog lists (model_recommend.go),
// falling back to what it did before prices were read.
func (codexChat) recommendedUtility(v visibility) string {
	ids, _ := codexRecommendIDs(v)
	if m := cheapestListedModel(session.KindCodex, ids); m != "" {
		return m
	}
	return cheapOneShotModel(ids)
}

// recommendedUtility: opencode is deliberately NOT ranked. Its cheapest rows are the $0
// "-free" promotions, which are retired every few weeks and are a listing rather than an
// entitlement (see oneShotOnOpencode). The OpenCode Go route is pinned only when the live
// account catalog proves it is available.
func (opencodeChat) recommendedUtility(v visibility) string {
	return recommendedCatalogModel(v.ids(session.KindOpencode, opencode.Models()), modelfallback.OneShotOpencodeGo, "")
}

// recommendedUtility: the cheapest priced model agy's catalog lists, else its chat default.
func (agyChat) recommendedUtility(v visibility) string {
	if m := cheapestListedModel(session.KindAgy, agyRecommendIDs(v)); m != "" {
		return m
	}
	return agyNamedModel(v, defaultAgyChatModel)
}

// safeOneShotModel: a muse run with no --model lands on the contributor row (clamp 8), so the
// recommendation is the model museOneShot would fall back to anyway.
func (museChat) safeOneShotModel(v visibility) string { return museSafeOneShotModel(v) }

// namedEnvModel: an agy override may be a display name, resolved to its id (agyNamedModel).
func (agyChat) namedEnvModel(v visibility, m string) string { return agyNamedModel(v, m) }

func (ClaudeChat) takesCallerModel() {}
