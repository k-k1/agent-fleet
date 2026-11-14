package chatx

import (
	"maps"
	"slices"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// chatBackendRoutes is the routes each chat backend can run on (ADR 0138 D2). A backend runs on
// the same credential its kind's sessions use with no selection (the claude chat on the normal
// CLAUDE_CONFIG_DIR, the codex chat on the shared auth.json), so these equal the kind's fixed
// routes; the one a call ran on is resolved per login mode when the call starts.
// TestChatBackendRoutes holds this table to chatBackends.
var chatBackendRoutes = map[string][]string{
	session.KindClaude:   {routes.RouteAnthropicSubscription},
	session.KindCodex:    {routes.RouteChatGPTSubscription, routes.RouteOpenAIAPI},
	session.KindOpencode: {routes.RouteAFEngineLLM, routes.RouteAnthropicAPI, routes.RouteCustomAPI, routes.RouteGeminiAPI, routes.RouteOpenAIAPI, routes.RouteOpenRouterAPI, routes.RouteOpencodeAccount, routes.RouteOpencodeGo, routes.RouteOpencodeZen, routes.RouteSakanaAPI},
	session.KindAgy:      {routes.RouteAgyLogin},
	session.KindCursor:   {routes.RouteCursorLogin},
	lcppKind:             {routes.RouteAFEngineLLM, routes.RouteMemberLlamacpp},
	session.KindMuse:     {routes.RouteMetaAPI, routes.RouteMuseLogin},
}

// ChatBackendRoutes returns every chat backend's routes by kind, each list sorted.
func ChatBackendRoutes() map[string][]string {
	out := make(map[string][]string, len(chatBackendRoutes))
	for k, rs := range chatBackendRoutes {
		out[k] = slices.Sorted(slices.Values(rs))
	}
	return out
}

// chatBackendKinds is the kinds with a production chat backend, sorted.
func chatBackendKinds() []string { return slices.Sorted(maps.Keys(chatBackends)) }
