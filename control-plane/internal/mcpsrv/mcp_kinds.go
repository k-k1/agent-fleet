package mcpsrv

import (
	"context"
	"fmt"
	"strings"

	"github.com/k-k1/agent-fleet/control-plane/internal/agentkinds"
	"github.com/k-k1/agent-fleet/control-plane/internal/agentroutes"
)

// toolKinds is the kind catalog in the order the tool texts list it: claude, the default kind,
// first, then the catalog's own order. The enumerations a model reads are built from the
// catalog rather than kept by hand, because a kind left out of a hand list is one no model
// ever starts — and a hand list drifts from the Agent's copy (#2857).
func toolKinds() []agentkinds.Entry {
	all := agentkinds.All()
	out := make([]agentkinds.Entry, 0, len(all))
	for _, e := range all {
		if e.Kind == "claude" {
			out = append([]agentkinds.Entry{e}, out...)
		} else {
			out = append(out, e)
		}
	}
	return out
}

// hiddenKinds are the harnesses the caller's tenant policy forbids altogether (ADR 0138 D10a,
// #4241): the tool texts a member reads leave them out, as every other screen does. nil hides
// nothing. Using one is refused anyway (the CP and the Agent judge every create), so this is about
// what is shown, not what is enforced.
type hiddenKinds map[string]bool

// kindNames is the toolKinds names that keep says yes to and hidden does not hide.
func kindNames(hidden hiddenKinds, keep func(agentkinds.Entry) bool) []string {
	var out []string
	for _, e := range toolKinds() {
		if keep(e) && !hidden[e.Kind] {
			out = append(out, e.Kind)
		}
	}
	return out
}

// shownOf is kinds without the hidden ones, in their order.
func shownOf(hidden hiddenKinds, kinds ...string) []string {
	var out []string
	for _, k := range kinds {
		if !hidden[k] {
			out = append(out, k)
		}
	}
	return out
}

// isTerminalKind is a kind that runs a terminal, not an agent: it has no model to list. The
// catalog has no field for this (mcpClient "no" coincides today but answers another question).
func isTerminalKind(kind string) bool { return kind == "shell" || kind == "ssm" }

// modelKinds is every kind list_models answers for: each one that runs an agent.
func modelKinds(hidden hiddenKinds) []string {
	return kindNames(hidden, func(e agentkinds.Entry) bool { return !isTerminalKind(e.Kind) })
}

// createKinds is every kind create_session offers: all of them but ssm, which needs an
// ssm_target the tool has no parameter for.
func createKinds(hidden hiddenKinds) []string {
	return kindNames(hidden, func(e agentkinds.Entry) bool { return e.Kind != "ssm" })
}

// joinKinds joins kinds with sep, marking claude with defaultMark ("" for none).
func joinKinds(kinds []string, sep, defaultMark string) string {
	out := make([]string, len(kinds))
	for i, k := range kinds {
		out[i] = k
		if k == "claude" {
			out[i] += defaultMark
		}
	}
	return strings.Join(out, sep)
}

// listKinds is kinds as an English list: "a, b or c".
func listKinds(kinds []string, last string) string {
	if len(kinds) < 2 {
		return strings.Join(kinds, "")
	}
	return strings.Join(kinds[:len(kinds)-1], ", ") + " " + last + " " + kinds[len(kinds)-1]
}

// managedKindsSentence tells the caller which kinds create_session starts on the managed
// driver: the managed-default kinds that also have a Terminal route, then the managed-only ones.
// Either half is left out when every kind it would name is hidden; "" when both are.
func managedKindsSentence(hidden hiddenKinds) string {
	byDefault := kindNames(hidden, func(e agentkinds.Entry) bool { return e.ManagedDefault && !e.ManagedOnly })
	only := kindNames(hidden, func(e agentkinds.Entry) bool { return e.ManagedOnly })
	var parts []string
	if len(byDefault) > 0 {
		parts = append(parts, listKinds(byDefault, "and")+" sessions default to the managed driver, not TUI")
	}
	switch len(only) {
	case 0:
	case 1:
		parts = append(parts, only[0]+" is always managed")
	default:
		parts = append(parts, listKinds(only, "and")+" are always managed")
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "; ") + "."
}

// listModelsKindClauses are list_models' per-kind notes, in the order its description gives them.
// liveCatalogKinds share one clause.
var (
	liveCatalogKinds      = []string{"codex", "opencode", "agy", "copilot", "cursor", "kiro"}
	listModelsKindClauses = []struct{ kind, text string }{
		{"copilot", "copilot's reflects the account's Copilot plan (empty on Free = Auto only; omit model for auto routing)"},
		{"cursor", "cursor's is an account-linked catalog with effort folded into the model id (omit model for Auto, which a Free plan allows)"},
		{"kiro", "kiro's is account-linked and allows named models even on Free (default auto)"},
		{"lcpp", "lcpp needs no sign-in and returns the models in the deployment's own engine catalog (empty on a deployment with no engine)"},
		{"muse", "muse returns its signed-in account catalog"},
	}
)

// listModelsKindNotes is the part of list_models' description that speaks of each kind, without
// the hidden ones: "" when none is left.
func listModelsKindNotes(hidden hiddenKinds) string {
	var parts []string
	if !hidden["claude"] {
		parts = append(parts, "claude returns its fixed tier aliases")
	}
	switch live := shownOf(hidden, liveCatalogKinds...); len(live) {
	case 0:
	case 1:
		parts = append(parts, live[0]+" returns the live catalog reflecting the user's connected providers")
	default:
		parts = append(parts, listKinds(live, "and")+" return the live catalog reflecting the user's connected providers")
	}
	for _, c := range listModelsKindClauses {
		if !hidden[c.kind] {
			parts = append(parts, c.text)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "; ") + ". "
}

// createKindSentences are create_session's per-kind notes on its kind argument, in order.
var createKindSentences = []struct{ kind, text string }{
	{"agy", "agy is the Antigravity CLI (launchable only when connected)."},
	{"copilot", "copilot is the GitHub Copilot CLI (needs the GitHub connection + a Copilot subscription)."},
	{"cursor", "cursor is the Cursor CLI (launchable only when connected)."},
	{"kiro", "kiro is the Kiro CLI (launchable only when connected; defaults to the managed driver)."},
	{"lcpp", "lcpp is the built-in llama.cpp harness (no sign-in; always managed, no TUI route); pick its model from list_models(kind=\"lcpp\")."},
	{"muse", "muse is Meta's Muse Code (launchable only once installed on demand and signed in; always managed, no TUI route)."},
	{"shell", "shell is a raw shell with no agent guardrails — a shell session takes no initial_prompt over MCP (it would run as a command), and send_to_session does not reach it either."},
}

// createKindDesc is create_session's kind argument description, without the hidden kinds.
func createKindDesc(hidden hiddenKinds) string {
	out := "agent kind: " + joinKinds(createKinds(hidden), " | ", " (default)") + "."
	for _, s := range createKindSentences {
		if !hidden[s.kind] {
			out += " " + s.text
		}
	}
	return out
}

// createRouteArg reads create_session's route / route_connection arguments into the selection
// body field, and refuses with the Workspace untouched what the route snapshot proves invalid
// (ADR 0128 decision 12). Neither argument set is the unchanged case: it returns nil and the
// create carries no selection at all.
func createRouteArg(kind, route, connection string) (map[string]any, error) {
	route, connection = strings.TrimSpace(route), strings.TrimSpace(connection)
	if route == "" && connection == "" {
		return nil, nil
	}
	sel := &agentroutes.Selection{Route: route, Connection: connection}
	if _, err := agentroutes.CheckCreateRoute(kind, sel); err != nil {
		return nil, err
	}
	out := map[string]any{"route": route}
	if connection != "" {
		out["connection"] = connection
	}
	return out, nil
}

// createRouteArgs reads route / route_connection from the raw tool arguments. The key check
// does not look at types and argStr turns a non-string into "", which here would drop an
// explicit selection and create on the default route, so a value that is present and is not a
// string is refused. A JSON null counts as absent.
func createRouteArgs(kind string, args map[string]any) (map[string]any, error) {
	var vals [2]string
	for i, k := range [2]string{"route", "route_connection"} {
		v, present := args[k]
		if !present || v == nil {
			continue
		}
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("%s must be a string (got %T); the route is chosen by its id, e.g. route=\"anthropic-api\"", k, v)
		}
		vals[i] = s
	}
	return createRouteArg(kind, vals[0], vals[1])
}

// spaced is s followed by a space, or "" for an empty s: a sentence that may drop out of a text.
func spaced(s string) string {
	if s == "" {
		return ""
	}
	return s + " "
}

// hiddenKinds is what the caller's tenant policy hides altogether. It fails closed: when the
// membership or the policy cannot be read, every kind that runs a model is hidden, so the texts
// name none and list_models and create_session refuse every model kind until it can be read
// (D10a: a hidden item stays absent even while the policy is unknown).
func (a API) hiddenKinds(ctx context.Context, prin *mcpPrincipal) hiddenKinds {
	ms, err := a.cp.Store().ListMemberships(ctx, prin.identityID)
	if err != nil {
		return allModelKinds()
	}
	for _, m := range ms {
		if m.MembershipID == prin.membershipID {
			h, err := a.cp.HiddenKinds(ctx, m.TenantID)
			if err != nil {
				return allModelKinds()
			}
			return h
		}
	}
	return allModelKinds()
}

// allModelKinds hides every kind that runs on a model credential: the fail-closed answer.
func allModelKinds() hiddenKinds {
	out := hiddenKinds{}
	for _, k := range agentroutes.ModelKinds() {
		out[k] = true
	}
	return out
}

// verb picks the singular or plural form for a list of n kinds.
func verb(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// sessionUsageDesc is get_session_usage's description without the hidden kinds.
func sessionUsageDesc(hidden hiddenKinds) string {
	capable := shownOf(hidden, "claude", "codex", "opencode", "agy", "copilot", "cursor")
	scope := "shell/ssm excluded"
	if len(capable) > 0 {
		scope = strings.Join(capable, "/") + "; " + scope
	}
	out := "Per-session context fill and cumulative token consumption for transcript-capable sessions (" + scope + "). " +
		"Optional `name` narrows to one session; omitted returns all. `context` is the current context size (tokens with read/create/fresh breakdown, pct of window; absent until the first assistant reply, reset by auto-compaction). " +
		"`cumulative` sums consumption (logical turns, inTok/outTok/cacheRead/cacheCreate, spend = inTok+cacheCreate+outTok). "
	noTokens := shownOf(hidden, "agy", "cursor")
	if n := len(noTokens); n > 0 {
		out += "NOTE: " + listKinds(noTokens, "and") + " " + verb(n, "is", "are") + " listed but " + verb(n, "its", "their") +
			" transcripts record no token counts, so " + verb(n, "its", "their") + " `context` is absent and `cumulative` is all zeros — not a sign of no usage"
		if !hidden["agy"] {
			out += " (check get_agent_usage for agy's quota instead)"
		}
		out += ". "
	}
	if !hidden["kiro"] {
		also := " "
		if len(noTokens) > 0 {
			also = " also "
		}
		out += "kiro" + also + "lacks per-turn tokens, but while a managed (ACP) session is running it returns live `context` (pct + estimated tokens against the real window) and `cumulative.credits` (metered credit spend) from its _kiro.dev/metadata; a stopped or TUI kiro session has no `context`. "
	}
	if !hidden["copilot"] {
		out += "copilot records outTok only (inTok/cache stay 0, `context` absent). "
	}
	return out + "Use when asked which session is near its context limit or how much a session has consumed. When the all-sessions answer carries `pending` > 0 the list is partial (sessions whose transcripts are still being parsed are left out): call again shortly before totalling it. Subscription quota is get_agent_usage (separate tool)."
}

// agentUsageDesc is get_agent_usage's description without the hidden kinds.
func agentUsageDesc(hidden hiddenKinds) string {
	with := shownOf(hidden, "claude", "codex", "agy", "muse")
	without := shownOf(hidden, "opencode", "copilot", "cursor", "kiro")
	var scope []string
	if len(with) > 0 {
		scope = append(scope, listKinds(with, "and"))
	}
	if len(without) > 0 {
		scope = append(scope, listKinds(without, "and")+" "+verb(len(without), "has", "have")+" no usage source")
	}
	out := "Subscription usage and rate limits for the agent CLIs in your Workspace"
	if len(scope) > 0 {
		out += " (" + strings.Join(scope, "; ") + ")"
	}
	out += ". "
	if windows := shownOf(hidden, "claude", "codex"); len(windows) > 0 {
		out += listKinds(windows, "and") + " " + verb(len(windows), "reports", "report") + " fiveHour / sevenDay windows with pct (percent used, 0-100) and resetsAt (ISO instant the limit lifts)"
		if !hidden["codex"] {
			out += "; codex may add planType and resetCredits"
		}
		out += ". "
	}
	if !hidden["agy"] {
		out += "agy has a different shape: account / plan plus groups (each quota pool's label, remainingPct and resetsAt — e.g. the Starter pool). "
	}
	return out + "authed=false means that CLI has no subscription login; ageSec is the capture age in seconds. Use when asked how much quota remains or when a limit resets."
}

// createModelDesc is create_session's model argument description: claude's Bedrock clause only
// while claude is shown.
func createModelDesc(hidden hiddenKinds) string {
	out := "model override (optional)"
	if !hidden["claude"] {
		out += ". claude on route=bedrock requires one: a Bedrock model id or system inference profile id as Bedrock writes it (anthropic.… or <scope>.anthropic.…, scope us, eu, apac, jp, au, us-gov or global). Aliases such as opus, claude-… ids and ARNs are refused; the session then runs on that one model for every call, subagents included, with no fallback to another"
	}
	return out
}

// createRouteDesc is create_session's route argument description: each kind's routes only while
// the kind is shown. Which routes the policy forbids is #4323.
func createRouteDesc(hidden hiddenKinds) string {
	var accepts []string
	if !hidden["claude"] {
		accepts = append(accepts, "claude accepts anthropic-subscription, anthropic-api, bedrock or bedrock-deployment (Bedrock the deployment pays for, within the budget your tenant administrator sets)")
	}
	if !hidden["opencode"] {
		accepts = append(accepts, "opencode accepts bedrock (with an amazon-bedrock/<id> model)")
	}
	out := "explicit model route id (optional; omit to keep the kind's own default resolution). "
	if len(accepts) > 0 {
		out += strings.Join(accepts, ", ") + "; all Terminal sessions only, and bedrock / bedrock-deployment only on a workspace where their acceptance switch is set. Any other kind, or a route the kind does not declare, is refused"
	} else {
		out += "A route the kind does not declare is refused"
	}
	return out + " with the reason, never run on the default route instead."
}
