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

// hiddenKinds is what the caller's tenant policy hides altogether. An answer that cannot be had
// hides nothing: the texts then name a kind that is refused when used, which is safe.
func (a API) hiddenKinds(ctx context.Context, prin *mcpPrincipal) hiddenKinds {
	ms, err := a.cp.Store().ListMemberships(ctx, prin.identityID)
	if err != nil {
		return nil
	}
	for _, m := range ms {
		if m.MembershipID == prin.membershipID {
			return a.cp.HiddenKinds(ctx, m.TenantID)
		}
	}
	return nil
}
