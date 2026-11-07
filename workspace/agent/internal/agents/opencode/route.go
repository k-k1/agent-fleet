package opencode

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// Explicit routes for opencode (ADR 0128 P2c): `bedrock` on a member's Settings AWS profile marked
// as a model route, through the AF route gateway, Terminal only.
//
// Measured on opencode 2.2.6 against a stand-in gateway (ADR 0128, the P2c note):
//   - its built-in amazon-bedrock provider sends ConverseStream (`POST <baseURL>/model/<id>/
//     converse-stream`), and `providers.amazon-bedrock.settings.baseURL` points it anywhere;
//   - AWS_BEARER_TOKEN_BEDROCK alone activates the provider and is sent as `Authorization: Bearer`;
//     with `settings.auth = "bearer"` a missing bearer fails the call instead of falling back to
//     the AWS credential chain, and static keys, ~/.aws files and AWS_PROFILE never reach a call;
//   - it cannot use a Unix socket (a unix:// base URL is refused), so the gateway's loopback port
//     is used, with the caller attributed through /proc (routegw/peer_tcp.go);
//   - the model call leaves from the private `serve --stdio` child of the TUI, inside the pane's
//     process tree; on af's shared serve it would leave from a daemon in no session's tree, which
//     is why the route is Terminal only and the pane runs --standalone;
//   - inline config (OPENCODE_CONFIG_CONTENT) outranks the global and project files for the
//     provider's settings, but NOT for `enabled_providers`, where a file wins: a file that sets it
//     is refused (configConflict);
//   - a 401 or a 503 with `x-should-retry: false` goes out once and is shown at once with the
//     gateway's message;
//   - the title is generated on `small_model`, which defaults to an OpenAI model sent to
//     `<baseURL>/responses`; the route pins it to the session's model.
//
// A Managed session runs on af's shared serve, whose process is in no session's tree and whose
// one environment cannot carry a per-session bearer, so it is never offered the route
// (CheckRouteDriver).

// RouteBedrock is the gateway route.
const RouteBedrock = "bedrock"

// bedrockGateEnv opens the bedrock route for opencode on this Agent. ADR 0128 decision 14 ships a
// harness × route row only after live turns on real Bedrock; until then the row is declared (the
// Control Plane's snapshot and a stored selection know it) but refused at every check, bind and
// launch and not offered, unless the Agent runs with AF_OPENCODE_BEDROCK_ROUTE=1 for the
// acceptance run. It is separate from claude's gate: each row passes its own acceptance.
const bedrockGateEnv = "AF_OPENCODE_BEDROCK_ROUTE"

func bedrockOpen() bool { return os.Getenv(bedrockGateEnv) == "1" }

func bedrockGated() *agents.RouteRefusal {
	return &agents.RouteRefusal{Status: http.StatusConflict, Code: routes.CodeRouteUnavailable,
		Message: "opencode on the bedrock route has not passed its live acceptance yet (ADR 0128 decision 14) and is off on this workspace; it is opened for the acceptance run with " + bedrockGateEnv + "=1 on the workspace's Agent (a Control Plane started with AF_WS_OPENCODE_BEDROCK_ROUTE=1 sets it at the workspace's next Start; a running workspace needs Stop → Start)"}
}

// bedrockProvider is opencode's own provider id for Bedrock.
const bedrockProvider = "amazon-bedrock"

// bedrockModelRe is the model part of an amazon-bedrock id (an optional #variant after it).
var bedrockModelRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,299}$`)

// bedrockModel reports whether model is one of opencode's amazon-bedrock models, the only ones
// the bedrock route serves (ADR 0128 decision 6: a session on a route stays on its models).
func bedrockModel(model string) bool {
	prov, id, ok := splitModel(model)
	if !ok || prov != bedrockProvider {
		return false
	}
	base, _, _ := strings.Cut(id, "#")
	return bedrockModelRe.MatchString(base)
}

// Routes implements agents.RouteAdapter.
func (agentImpl) Routes() []string { return []string{RouteBedrock} }

// OfferedRoutes implements agents.RouteOfferer: bedrock only while its gate is open.
func (agentImpl) OfferedRoutes() []string {
	if bedrockOpen() {
		return []string{RouteBedrock}
	}
	return nil
}

// CheckRouteDriver implements agents.RouteDriverChecker: the route needs the pane's own private
// server (see the package note above), so a Managed session is refused.
func (agentImpl) CheckRouteDriver(route, driver string) *agents.RouteRefusal {
	if route == RouteBedrock && driver != session.DriverTUI {
		return &agents.RouteRefusal{Status: http.StatusBadRequest, Code: routes.CodeRouteUnsupported,
			Message: "opencode runs on the bedrock route only as a Terminal (CLI) session: a Managed session shares one opencode server, which cannot carry a route of its own"}
	}
	return nil
}

// RouteOwnsModels implements agents.RouteModelOwner: a bedrock session's model is checked by
// CheckRoute against the route, not against the kind's live catalogue (which lists Bedrock models
// only when the route is offered, and never for the shared serve).
func (agentImpl) RouteOwnsModels(route string) bool { return route == RouteBedrock }

func checkSelection(sel agents.RouteSelection) *agents.RouteRefusal {
	if sel.Route != RouteBedrock {
		return &agents.RouteRefusal{Status: http.StatusBadRequest, Code: routes.CodeRouteUnsupported,
			Message: "opencode does not run on route " + sel.Route}
	}
	if _, _, ok := routes.ParseAWSConnection(strings.TrimSpace(sel.Connection)); !ok {
		return &agents.RouteRefusal{Status: http.StatusBadRequest, Code: routes.CodeRouteUnavailable,
			Message: "the bedrock route needs a connection naming one of your Settings AWS profiles marked as a model route, as <profile id>@<region> (the region may be empty for the profile's own)"}
	}
	return nil
}

func noGateway() *agents.RouteRefusal {
	return &agents.RouteRefusal{Status: http.StatusServiceUnavailable, Code: routes.CodeRouteUnavailable,
		Message: "the AF route gateway is not running in this workspace, so the bedrock route cannot be used; restart the workspace, or start the session on another route"}
}

func notBedrockModel(model string) *agents.RouteRefusal {
	msg := "the bedrock route runs only opencode's Amazon Bedrock models (amazon-bedrock/<model id>); pick one of them"
	if strings.TrimSpace(model) == "" {
		msg = "the bedrock route needs an Amazon Bedrock model (amazon-bedrock/<model id>): opencode's own default model is not on the route"
	}
	return &agents.RouteRefusal{Status: http.StatusBadRequest, Code: routes.CodeRouteModel, Message: msg}
}

// CheckRoute implements agents.RouteChecker.
func (agentImpl) CheckRoute(ctx context.Context, sel agents.RouteSelection, model string) *agents.RouteRefusal {
	if ref := checkSelection(sel); ref != nil {
		return ref
	}
	if !bedrockOpen() {
		return bedrockGated()
	}
	if !bedrockModel(model) {
		return notBedrockModel(model)
	}
	gw := agents.CurrentRouteGateway()
	if gw == nil {
		return noGateway()
	}
	if ref := gw.CheckRoute(ctx, sel, model); ref != nil {
		return ref
	}
	// The project's files are judged where the working copy is known (the launch).
	return configConflict(globalConfigFiles())
}

// BindRoute implements agents.RouteBinder: the gateway binds the selection to the profile's
// identity and region as they are now, so a session never moves to another account.
func (agentImpl) BindRoute(sel agents.RouteSelection) (agents.RouteSelection, *agents.RouteRefusal) {
	if ref := checkSelection(sel); ref != nil {
		return sel, ref
	}
	if !bedrockOpen() {
		return sel, bedrockGated()
	}
	gw := agents.CurrentRouteGateway()
	if gw == nil {
		return sel, noGateway()
	}
	return gw.Bind(sel)
}

// ResolveRoute implements agents.RouteAdapter: the selection is fixed for the session's life (the
// launch pins the provider and allows no other), so the selection in force is the evidence.
func (agentImpl) ResolveRoute(m session.Meta, _ agents.TurnInfo) agents.ResolvedRoute {
	if m.Route == nil {
		return agents.ResolvedRoute{}
	}
	return agents.ResolvedRoute{Route: m.Route.Route, Connection: m.Route.Connection}
}

// RouteHeld implements agents.RouteWatcher: opencode re-reads its config files while it runs, and
// one that sets enabled_providers outranks the launch's pin.
func (agentImpl) RouteHeld(m session.Meta) *agents.RouteRefusal {
	if m.Route == nil {
		return nil
	}
	return configConflict(append(globalConfigFiles(), projectConfigFiles(m)...))
}

// routeUnset is what the route's pane must not inherit: other config sources that could widen
// the provider list or replace the inline pin (OPENCODE_CONFIG_DIR measured to be read). The
// workspace's provider keys and engine token are simply not put in the pane's environment.
var routeUnset = []string{"OPENCODE_CONFIG", "OPENCODE_CONFIG_DIR", "OPENCODE_CLI_CONFIG_CONTENT"}

// routeConfigKey carries the route's inline config in RoutePlan.Settings.
const routeConfigKey = "OPENCODE_CONFIG_CONTENT"

// routeConfig is the inline config of a bedrock pane: only the amazon-bedrock provider, sent to
// the gateway's loopback port with the bearer from the environment (never the AWS credential
// chain), and the title model pinned to the session's model so no call goes to another one.
func routeConfig(baseURL, model string) map[string]any {
	return map[string]any{
		"enabled_providers": []string{bedrockProvider},
		"small_model":       strings.TrimSpace(model),
		"providers": map[string]any{bedrockProvider: map[string]any{
			"settings": map[string]any{"baseURL": baseURL, "auth": "bearer"},
		}},
	}
}

// ApplyRoute implements agents.RouteAdapter: a gateway bearer for this session's processes, the
// gateway's loopback port, and the inline config that keeps the pane on that provider.
func (agentImpl) ApplyRoute(m session.Meta, sel agents.RouteSelection) (agents.RoutePlan, error) {
	if ref := checkSelection(sel); ref != nil {
		return agents.RoutePlan{}, ref
	}
	if !bedrockOpen() {
		return agents.RoutePlan{}, bedrockGated()
	}
	if !bedrockModel(m.Model) {
		return agents.RoutePlan{}, notBedrockModel(m.Model)
	}
	if ref := (agentImpl{}).CheckRouteDriver(sel.Route, m.DriverKind()); ref != nil {
		return agents.RoutePlan{}, ref
	}
	gw := agents.CurrentRouteGateway()
	if gw == nil {
		return agents.RoutePlan{}, noGateway()
	}
	g, ref := gw.IssueLoopback(m)
	if ref != nil {
		return agents.RoutePlan{}, ref
	}
	cfg, err := json.Marshal(routeConfig(g.BaseURL, m.Model))
	if err != nil {
		return agents.RoutePlan{}, err
	}
	return agents.RoutePlan{
		Env:      []string{"AWS_BEARER_TOKEN_BEDROCK=" + g.Token, "AWS_REGION=" + g.Region},
		Unset:    routeUnset,
		Settings: []string{routeConfigKey + "=" + string(cfg)},
	}, nil
}

// buildRouteLaunch is the Terminal launch of a session with an explicit route: a private server
// in the pane (--standalone, never af's shared serve), the route's environment and nothing of the
// workspace's provider keys or engine token, and the route's inline config on every launch.
func buildRouteLaunch(spec LaunchSpec, m session.Meta, ocSid string) (agents.LaunchPlan, error) {
	plan, err := agentImpl{}.ApplyRoute(m, *m.Route)
	if err != nil {
		return agents.LaunchPlan{}, err
	}
	if ref := configConflict(append(globalConfigFiles(), projectConfigFiles(m)...)); ref != nil {
		return agents.LaunchPlan{}, ref
	}
	var extra map[string]any
	for _, kv := range plan.Settings {
		if k, v, ok := strings.Cut(kv, "="); ok && k == routeConfigKey {
			if err := json.Unmarshal([]byte(v), &extra); err != nil {
				return agents.LaunchPlan{}, err
			}
		}
	}
	markSlotMode(ocSid, modeStandalone)
	resume, fork, err := standaloneTarget(m, ocSid)
	if err != nil {
		return agents.LaunchPlan{}, err
	}
	program := buildProgramV2With(spec, m.Model, m.Mode, resume, fork, extra)
	if len(plan.Unset) > 0 {
		program = "unset " + strings.Join(plan.Unset, " ") + "; " + program
	}
	env := append([]string{"AF_SESSION_SID=" + ocSid}, plan.Env...)
	return agents.LaunchPlan{Program: program, Cwd: m.CWD(), Env: env}, nil
}

// --- config files that would override the route ----------------------------------------------

// configFileNames are the config files opencode reads in a directory (config.json is not: measured).
var configFileNames = []string{"opencode.json", "opencode.jsonc"}

// globalConfigFiles are the member's own config files.
func globalConfigFiles() []string {
	dir := paths.OpencodeConfigDir()
	out := make([]string, 0, len(configFileNames))
	for _, n := range configFileNames {
		out = append(out, filepath.Join(dir, n))
	}
	return out
}

// projectConfigFiles are the config files of the session's working directory and every directory
// above it, each with its .opencode/ form. opencode walks up to the worktree root; walking to the
// filesystem root reads more than it does, which is the safe direction.
func projectConfigFiles(m session.Meta) []string {
	var out []string
	seen := map[string]bool{}
	for _, start := range []string{m.CWD(), m.Dir} {
		if start == "" {
			continue
		}
		for d := filepath.Clean(start); ; d = filepath.Dir(d) {
			if !seen[d] {
				seen[d] = true
				for _, n := range configFileNames {
					out = append(out, filepath.Join(d, n), filepath.Join(d, ".opencode", n))
				}
			}
			if d == filepath.Dir(d) {
				break
			}
		}
	}
	return out
}

// configConflict refuses a route whose pin a config file would override: measured on 2.2.6, a
// file's `enabled_providers` wins over the inline one, so a file that sets it can put the pane on
// another provider. A file that cannot be parsed even as JSONC is judged by whether the key
// appears in it at all.
func configConflict(files []string) *agents.RouteRefusal {
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var top map[string]json.RawMessage
		hit := false
		if json.Unmarshal(stripJSONC(b), &top) == nil {
			v, ok := top["enabled_providers"]
			hit = ok && string(v) != "null"
		} else {
			hit = strings.Contains(string(b), `"enabled_providers"`)
		}
		if hit {
			return &agents.RouteRefusal{Status: http.StatusConflict, Code: routes.CodeRouteSettings,
				Message: f + " sets enabled_providers, which opencode applies over the session's bedrock route; remove it, or start the session without an explicit route"}
		}
	}
	return nil
}

// stripJSONC turns JSONC into JSON: line and block comments, and a comma followed only by
// whitespace and a closing bracket, are removed outside strings.
func stripJSONC(b []byte) []byte {
	out := make([]byte, 0, len(b))
	inStr, esc := false, false
	for i := 0; i < len(b); i++ {
		c := b[i]
		if inStr {
			out = append(out, c)
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch {
		case c == '"':
			inStr = true
			out = append(out, c)
		case c == '/' && i+1 < len(b) && b[i+1] == '/':
			for i < len(b) && b[i] != '\n' {
				i++
			}
			if i < len(b) {
				out = append(out, '\n')
			}
		case c == '/' && i+1 < len(b) && b[i+1] == '*':
			i += 2
			for i+1 < len(b) && (b[i] != '*' || b[i+1] != '/') {
				i++
			}
			i++
		case c == ',' && closesNext(b[i+1:]):
		default:
			out = append(out, c)
		}
	}
	return out
}

// closesNext reports whether rest starts, after whitespace and comments, with a closing bracket.
func closesNext(rest []byte) bool {
	for i := 0; i < len(rest); i++ {
		switch c := rest[i]; {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
		case c == '/' && i+1 < len(rest) && rest[i+1] == '/':
			for i < len(rest) && rest[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(rest) && rest[i+1] == '*':
			i += 2
			for i+1 < len(rest) && (rest[i] != '*' || rest[i+1] != '/') {
				i++
			}
			i++
		default:
			return c == '}' || c == ']'
		}
	}
	return false
}

// --- the route's model menu ------------------------------------------------------------------

// routeModelsTTL is how long the bedrock route's menu is kept: the list is opencode's own
// catalogue for the provider, which changes with opencode's models.dev refresh, not per call.
const routeModelsTTL = 10 * time.Minute

var routeModels struct {
	mu   sync.Mutex
	at   time.Time
	list []string
}

// listBedrockModels runs the CLI; a var so tests need no opencode.
var listBedrockModels = func() ([]string, error) {
	spec := CurrentLaunch()
	if err := PrepareStart(spec); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, spec.Bin, "models", "--standalone")
	cmd.Env = bedrockListEnv(os.Environ())
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return parseModels(string(out)), nil
}

// bedrockListEnv is the environment that makes `opencode models` print exactly the
// amazon-bedrock models a bedrock pane is offered: the provider activated by a placeholder bearer
// (no call is made; measured on 2.2.6), the AWS credential chain blinded as for every listing, and
// the other providers left out by the same inline pin a pane gets.
func bedrockListEnv(base []string) []string {
	env := withoutAWSCredentialChain(base)
	cfg, _ := json.Marshal(map[string]any{"enabled_providers": []string{bedrockProvider}})
	return mergeCommandEnv(env, []string{
		"AWS_BEARER_TOKEN_BEDROCK=af-route-listing-placeholder",
		routeConfigKey + "=" + string(cfg),
		"OPENCODE_CONFIG=", "OPENCODE_CONFIG_DIR=",
	})
}

// BedrockRouteModels is the menu of the bedrock route: opencode's amazon-bedrock models, offered
// only while the route is (its gate open and the gateway running). This replaces the keepInMenu
// stopgap for these ids with the route's availability (ADR 0128 decision 11): outside the route
// they still cannot work and stay out of the default menu. A failed listing keeps the last good
// one.
func BedrockRouteModels() []string {
	if !bedrockOpen() || agents.CurrentRouteGateway() == nil {
		return nil
	}
	routeModels.mu.Lock()
	defer routeModels.mu.Unlock()
	if routeModels.list != nil && time.Since(routeModels.at) < routeModelsTTL {
		return routeModels.list
	}
	ids, err := listBedrockModels()
	if err != nil {
		log.Printf("opencode: listing the bedrock route's models failed: %v", err)
		return routeModels.list
	}
	var out []string
	for _, id := range ids {
		if bedrockModel(id) {
			out = append(out, id)
		}
	}
	routeModels.list, routeModels.at = out, time.Now()
	return out
}
