package claude

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// Explicit routes for claude (ADR 0128 P1b), Terminal only: claude has no managed driver, so a
// selection on a managed launch is refused by the create's own driver check before it gets here.
//
// A route's credential reaches THIS session's process and no other, through two things:
//   - LaunchPlan.Env (`tmux new-session -e`), for the one credential the route adds;
//   - `env -u` in front of the CLI, for everything that would otherwise override it. claude
//     prefers CLAUDE_CODE_OAUTH_TOKEN / ANTHROPIC_API_KEY from the environment over its OAuth
//     file (authexpiry.go), and the pane inherits the tmux server's environment, so without
//     the unset a key in the Agent's environment would move a subscription session onto the
//     metered bill and a stale OAuth token would shadow an API key.
//
// The Agent's own environment never carries a route credential.

// Route ids this harness declares.
const (
	RouteSubscription = "anthropic-subscription"
	RouteAPI          = "anthropic-api"
)

// apiKeyConnection is the slot of anthropic-api: the member's Anthropic API key as Settings >
// Connections stores it (the provider-key map, under its environment variable name,
// secrets.Data.Opencode). It is the only possible connection, so an empty reference means it.
// The store is the member's own, so permission to USE the connection is the same as owning it,
// and another member's connection can never be named from here.
//
// The reference a session STORES is bound to the key instance chosen: "ANTHROPIC_API_KEY@<fp>",
// fp being a short sha256 of the key (no secret can be recovered from it). A bare slot name
// would follow whatever key is in the slot at the next start, so replacing the key would move the
// session to another account's bill; the bound form stops it instead (decision 3).
const apiKeyConnection = "ANTHROPIC_API_KEY"

// keyFingerprint identifies one key instance without revealing it.
func keyFingerprint(key string) string {
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:])[:16]
}

// splitConnection reads a connection reference into its slot and the bound fingerprint ("" for
// an unbound reference).
func splitConnection(conn string) (slot, fp string) {
	slot, fp, _ = strings.Cut(strings.TrimSpace(conn), "@")
	return slot, fp
}

// loadAPIKey reads the member's Anthropic API key; replaceable in tests.
var loadAPIKey = func() (string, error) {
	s, err := secrets.Load()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(s.Opencode[apiKeyConnection]), nil
}

// routeUnset are the variables that would route a claude process somewhere other than the
// selected route, removed from the pane for both routes. The credential variables of the other
// route are added per route below.
var routeUnset = []string{
	"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL",
	"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY",
}

// Routes implements agents.RouteAdapter.
func (agentImpl) Routes() []string { return []string{RouteAPI, RouteSubscription} }

// ApplyRoute implements agents.RouteAdapter.
func (agentImpl) ApplyRoute(_ session.Meta, sel agents.RouteSelection) (agents.RoutePlan, error) {
	if ref := checkRouteSelection(sel); ref != nil {
		return agents.RoutePlan{}, ref
	}
	unset := append([]string(nil), routeUnset...)
	switch sel.Route {
	case RouteAPI:
		key, _ := loadAPIKey()
		if key = strings.TrimSpace(key); key == "" {
			return agents.RoutePlan{}, noAPIKey()
		}
		if _, fp := splitConnection(sel.Connection); fp != "" && fp != keyFingerprint(key) {
			return agents.RoutePlan{}, keyReplaced()
		}
		// The login of the other route must not shadow the key.
		unset = append(unset, "CLAUDE_CODE_OAUTH_TOKEN")
		return agents.RoutePlan{Env: []string{"ANTHROPIC_API_KEY=" + key}, Unset: unset}, nil
	default: // RouteSubscription: the OAuth login in claude's own credentials file
		unset = append(unset, "ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN")
		return agents.RoutePlan{Unset: unset}, nil
	}
}

// ResolveRoute implements agents.RouteAdapter. A session with an explicit selection runs on it:
// the credential is fixed at process start and the selection changes only through the switch of
// decision 10 (which stops the process first), so the selection in force now is the evidence for
// a turn run now. A turn of earlier is judged by Meta.RouteAt (stampRoute). A session with none
// reports nothing, as before.
func (agentImpl) ResolveRoute(m session.Meta, _ agents.TurnInfo) agents.ResolvedRoute {
	if m.Route == nil {
		return agents.ResolvedRoute{}
	}
	return agents.ResolvedRoute{Route: m.Route.Route, Connection: m.Route.Connection}
}

// CheckRoute implements agents.RouteChecker: the live half of the validation. The model needs
// no check (both routes serve every Claude model the CLI offers).
func (agentImpl) CheckRoute(_ context.Context, sel agents.RouteSelection, _ string) *agents.RouteRefusal {
	if ref := checkRouteSelection(sel); ref != nil {
		return ref
	}
	if sel.Route == RouteAPI {
		key, err := loadAPIKey()
		if err != nil || strings.TrimSpace(key) == "" {
			return noAPIKey()
		}
		if _, fp := splitConnection(sel.Connection); fp != "" && fp != keyFingerprint(strings.TrimSpace(key)) {
			return keyReplaced()
		}
	}
	// What the CLI's own settings would override is judged where the working copy is known
	// (BuildLaunch); the user's own settings do not depend on it, so they are refused here, early.
	return settingsConflict(userSettingFiles())
}

// BindRoute implements agents.RouteBinder: the stored form of an accepted selection. The key is
// read once, here, and a fingerprint already on the selection is checked against THAT read, so a
// key replaced between the check and the binding is refused instead of being bound silently.
func (agentImpl) BindRoute(sel agents.RouteSelection) (agents.RouteSelection, *agents.RouteRefusal) {
	if sel.Route != RouteAPI {
		return sel, nil
	}
	key, err := loadAPIKey()
	if key = strings.TrimSpace(key); err != nil || key == "" {
		return sel, noAPIKey()
	}
	fp := keyFingerprint(key)
	if _, want := splitConnection(sel.Connection); want != "" && want != fp {
		return sel, keyReplaced()
	}
	sel.Connection = apiKeyConnection + "@" + fp
	return sel, nil
}

// RouteHeld implements agents.RouteWatcher: the CLI re-applies the env block and apiKeyHelper of
// its settings files while it runs, so a file edited after launch (by the member, another session
// in this Workspace, or the CLI) can take the credential or endpoint over from the route.
func (agentImpl) RouteHeld(m session.Meta) *agents.RouteRefusal {
	if m.Route == nil {
		return nil
	}
	return settingsConflict(append(userSettingFiles(), projectSettingFiles(m)...))
}

// checkRouteSelection is the connection reference check: whose connection it is.
func checkRouteSelection(sel agents.RouteSelection) *agents.RouteRefusal {
	conn := strings.TrimSpace(sel.Connection)
	slot, _ := splitConnection(conn)
	switch sel.Route {
	case RouteAPI:
		if conn != "" && slot != apiKeyConnection {
			return &agents.RouteRefusal{Status: http.StatusForbidden, Code: routes.CodeRouteForbidden,
				Message: "connection " + conn + " is not your Anthropic API key connection; a route runs only on a connection of your own"}
		}
	case RouteSubscription:
		if conn != "" {
			return &agents.RouteRefusal{Status: http.StatusForbidden, Code: routes.CodeRouteForbidden,
				Message: "anthropic-subscription runs on this workspace's own Claude login and takes no connection reference"}
		}
	}
	return nil
}

func keyReplaced() *agents.RouteRefusal {
	return &agents.RouteRefusal{Status: http.StatusConflict, Code: routes.CodeRouteUnavailable,
		Message: "the Anthropic API key this session was started on has been replaced or removed; a session never moves to another key (and another bill). Start a new session on the current key"}
}

// The CLI re-applies the env block and apiKeyHelper of its settings files after it starts (user,
// project, local, managed), over the process environment, and `env -u` cannot remove them. A
// route whose credential or endpoint they would override cannot be honoured, so the launch is
// refused with the file named instead of running on whatever they say.
var settingsConflictEnv = []string{
	"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "CLAUDE_CODE_OAUTH_TOKEN",
	"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY",
}

// managedSettingsDir is where the organisation's managed settings live; a var for tests.
var managedSettingsDir = "/etc/claude-code"

// userSettingFiles are the settings files that do not depend on the working copy.
func userSettingFiles() []string {
	files := []string{
		filepath.Join(ConfigDir(), "settings.json"),
		filepath.Join(ConfigDir(), "settings.local.json"),
		filepath.Join(managedSettingsDir, "managed-settings.json"),
	}
	if more, _ := filepath.Glob(filepath.Join(managedSettingsDir, "managed-settings.d", "*.json")); len(more) > 0 {
		files = append(files, more...)
	}
	return files
}

// projectSettingFiles are the working copy's own settings files.
func projectSettingFiles(m session.Meta) []string {
	var files []string
	seen := map[string]bool{}
	for _, d := range []string{m.Dir, m.CWD()} {
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		files = append(files, filepath.Join(d, ".claude", "settings.json"), filepath.Join(d, ".claude", "settings.local.json"))
	}
	return files
}

// settingsConflict reads each file's env block and apiKeyHelper. A file that is absent or not
// valid JSON overrides nothing the Agent can see, so it is skipped.
func settingsConflict(files []string) *agents.RouteRefusal {
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var doc struct {
			Env          map[string]json.RawMessage `json:"env"`
			APIKeyHelper json.RawMessage            `json:"apiKeyHelper"`
		}
		if json.Unmarshal(b, &doc) != nil {
			continue
		}
		var hit string
		for _, k := range settingsConflictEnv {
			if _, ok := doc.Env[k]; ok {
				hit = "env." + k
				break
			}
		}
		if hit == "" && len(doc.APIKeyHelper) > 0 && string(doc.APIKeyHelper) != "null" && string(doc.APIKeyHelper) != `""` {
			hit = "apiKeyHelper"
		}
		if hit != "" {
			return &agents.RouteRefusal{Status: http.StatusConflict, Code: routes.CodeRouteSettings,
				Message: f + " sets " + hit + ", which the CLI re-applies over the session's route credential; remove it, or start the session without an explicit route"}
		}
	}
	return nil
}

func noAPIKey() *agents.RouteRefusal {
	return &agents.RouteRefusal{Status: http.StatusConflict, Code: routes.CodeRouteUnavailable,
		Message: "no Anthropic API key is connected; add one under Settings > Connections > Agents > the OpenCode card > API keys (Anthropic preset), or use the subscription route"}
}

// AuthExpiredFor is AuthExpired judged for one session (ADR 0128 decision 9).
//
//   - No selection: exactly AuthExpired, the Workspace-wide verdict.
//   - anthropic-api: false. The OAuth login is not what this session runs on, so its expiry
//     says nothing about it; whether the key works is only learnt by a turn.
//   - anthropic-subscription: the OAuth file alone, judged as if no token were in the
//     environment. The launch removes those variables, so the file is what the session uses,
//     and a token in the Agent's environment must not hide that it expired.
func AuthExpiredFor(m session.Meta) bool {
	if m.Route == nil {
		return AuthExpired()
	}
	switch m.Route.Route {
	case RouteAPI:
		return false
	case RouteSubscription:
		c, ok := readCreds()
		return ok && expiryOf(c, false).Dead(time.Now())
	}
	return AuthExpired()
}

// pinnedSettings merges a route's pin into the launch `--settings` JSON, in the same single
// object (no second flag, so no flag-merging behaviour is relied on). Each name in pin becomes an
// empty `env` entry and `apiKeyHelper` is emptied. The flag layer outranks the user, project and
// local settings files, so an edit to one of them after the pane started cannot put a value back
// (measured, claude 2.1.318, against a local server and a temporary config dir: a user-settings
// ANTHROPIC_BASE_URL beat the process environment, an empty flag-layer value beat that, and
// edits to user settings while a stream-json session ran changed neither the endpoint nor the
// credential headers; empty values are accepted and an empty ANTHROPIC_AUTH_TOKEN sends no
// Authorization header). Two things it cannot pin: the managed settings layer outranks the flag
// layer, and the route's own ANTHROPIC_API_KEY must not go on the command line; both stay with
// settingsConflict and RouteHeld. nil pin returns base untouched.
func pinnedSettings(base string, pin []string) string {
	if len(pin) == 0 {
		return base
	}
	var doc map[string]any
	if json.Unmarshal([]byte(base), &doc) != nil {
		return base
	}
	env, _ := doc["env"].(map[string]any)
	if env == nil {
		env = map[string]any{}
	}
	for _, name := range pin {
		env[name] = ""
	}
	doc["env"] = env
	doc["apiKeyHelper"] = ""
	b, err := json.Marshal(doc)
	if err != nil {
		return base
	}
	return string(b)
}

// routeLaunchProgram puts the route's `env -u` in front of the CLI command.
func routeLaunchProgram(program string, unset []string) string {
	if len(unset) == 0 {
		return program
	}
	return "env -u " + strings.Join(unset, " -u ") + " " + program
}

// SwitchTarget implements agents.RouteSwitcher (ADR 0128 decision 10): the metered route a
// session on the subscription may be moved to is anthropic-api, and only a session whose route
// is provably the subscription is offered it.
//
// "Provably" means an explicit anthropic-subscription selection: its launch removes every
// override from the process and pins the settings layer, so the window that stopped it is the
// subscription's. A session with no explicit selection (decision 2) resolves its route per turn
// from whatever its pane inherited, and nothing the Agent can read afterwards says which: the
// current environment, settings files and OAuth record are not the process's (an API key can
// survive an Agent restart in the tmux environment while the Agent's own is clean, and a settings
// key can be removed after launch). So it is refused as unproven rather than guessed; starting a
// new session on anthropic-subscription is the way to be able to switch.
func (agentImpl) SwitchTarget(m session.Meta) (agents.RouteSelection, *agents.RouteRefusal) {
	if m.Route == nil {
		return agents.RouteSelection{}, &agents.RouteRefusal{Status: http.StatusConflict, Code: routes.CodeSwitchUnproven,
			Message: "this session has no explicit route, so it cannot be shown to run on the subscription; " +
				"start a new session on the anthropic-subscription route to be able to switch"}
	}
	if m.Route.Route != RouteSubscription {
		return agents.RouteSelection{}, &agents.RouteRefusal{Status: http.StatusConflict, Code: routes.CodeSwitchNotSubscription,
			Message: "this session runs on " + m.Route.Route + ", not on the subscription; only a stopped subscription can be switched away from"}
	}
	return agents.RouteSelection{Route: RouteAPI}, nil
}

// stampRoute records on each turn the route the session ran on and, where the route proves it,
// how the call was charged. Only an explicit selection is stamped: with none, the turns stay
// unresolved exactly as before (claude's transcript carries no login or usage-credit signal).
// anthropic-api is metered by construction: the key is the only credential in the process. The
// subscription route stays unresolved, because past a window usage credits can bill the same
// login (decision 8).
//
// A session that switched routes (decision 10) files each turn under the selection in force at
// the turn's own timestamp (Meta.RouteAt), never under the one it holds now; a turn whose
// timestamp cannot be read stays unresolved.
func stampRoute(m session.Meta, turns []transcript.Turn) []transcript.Turn {
	out, _ := stampRouteChecked(m, turns)
	return out
}

// stampRouteChecked is stampRoute that says when the route could not be resolved (ok false, and
// no turns): the attribution is then unknown, not the old one.
func stampRouteChecked(m session.Meta, turns []transcript.Turn) ([]transcript.Turn, bool) {
	m, ok := withCurrentRoute(m)
	if !ok {
		return nil, false
	}
	if m.Route == nil && len(m.RouteSwitches) == 0 {
		return turns, true
	}
	// A copy: the turn list may be a memoized one shared with other readers.
	turns = append([]transcript.Turn(nil), turns...)
	for i := range turns {
		var at time.Time
		if t, err := time.Parse(time.RFC3339Nano, turns[i].TS); err == nil {
			at = t
		}
		sel, ok := m.RouteAt(at)
		if !ok || sel == nil {
			continue
		}
		turns[i].Route = sel.Route
		if sel.Route == RouteAPI {
			turns[i].Billing = usagex.BillingMetered
		}
	}
	return turns, true
}

// UsesAPIKey reports that the session runs on an explicit anthropic-api selection, so its
// credential is the API key and not the OAuth login.
func UsesAPIKey(m session.Meta) bool { return m.Route != nil && m.Route.Route == RouteAPI }

// withCurrentRoute lays the route selection and its boundaries as they are on disk now over m.
// A collector holds a meta listed before it parsed the transcript, and a switch in between would
// file API turns under the subscription (and the source stamp would then keep the row). Called
// AFTER the transcript is read: a meta newer than the lines is safe because each turn is placed
// by its own timestamp, an older one is not.
//
// ok is false when the meta exists but cannot be read (a writer truncates it before writing):
// a failed read proves nothing about the selection, so the caller must not attribute turns by
// the stale m. A meta that does not exist (the session is gone) leaves m as it is.
func withCurrentRoute(m session.Meta) (session.Meta, bool) {
	for try := 0; try < 10; try++ {
		if cur, ok := session.ReadMeta(m.Name); ok {
			m.Route, m.RouteSwitches = cur.Route, cur.RouteSwitches
			return m, true
		}
		if _, err := os.Stat(session.MetaPath(m.Name)); os.IsNotExist(err) {
			return m, true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return m, false
}
