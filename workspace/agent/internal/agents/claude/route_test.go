package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

func withAPIKey(t *testing.T, key string, err error) {
	t.Helper()
	prev := loadAPIKey
	loadAPIKey = func() (string, error) { return key, err }
	t.Cleanup(func() { loadAPIKey = prev })
}

func sel(route, conn string) *routes.Selection {
	return &routes.Selection{Route: route, Connection: conn}
}

func TestClaudeDeclaresItsRoutesAndIsAnAdapter(t *testing.T) {
	var a agents.Agent = agentImpl{}
	ra, ok := a.(agents.RouteAdapter)
	if !ok {
		t.Fatal("claude is not a RouteAdapter")
	}
	if _, ok := a.(agents.RouteChecker); !ok {
		t.Fatal("claude is not a RouteChecker")
	}
	got := ra.Routes()
	slices.Sort(got)
	if !slices.Equal(got, []string{RouteAPI, RouteSubscription, RouteBedrock}) {
		t.Errorf("Routes = %v", got)
	}
}

// The credential of one route reaches its own session and the other route's credentials are
// removed from the process; nothing goes through the Agent's own environment.
func TestApplyRouteIsolatesCredentials(t *testing.T) {
	withAPIKey(t, "sk-ant-api03-test-0123456789abcdef", nil)
	api, err := agentImpl{}.ApplyRoute(session.Meta{}, *sel(RouteAPI, ""))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(api.Env, []string{"ANTHROPIC_API_KEY=sk-ant-api03-test-0123456789abcdef"}) {
		t.Errorf("api env = %v", api.Env)
	}
	if !slices.Contains(api.Unset, "CLAUDE_CODE_OAUTH_TOKEN") || slices.Contains(api.Unset, "ANTHROPIC_API_KEY") {
		t.Errorf("api unset = %v: the OAuth token must go, the key must stay", api.Unset)
	}
	sub, err := agentImpl{}.ApplyRoute(session.Meta{}, *sel(RouteSubscription, ""))
	if err != nil {
		t.Fatal(err)
	}
	if len(sub.Env) != 0 {
		t.Errorf("subscription env = %v, want none", sub.Env)
	}
	for _, v := range []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL"} {
		if !slices.Contains(sub.Unset, v) {
			t.Errorf("subscription unset = %v, lacks %s", sub.Unset, v)
		}
	}
	for _, v := range []string{"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX"} {
		if !slices.Contains(api.Unset, v) {
			t.Errorf("api unset = %v, lacks %s", api.Unset, v)
		}
	}
}

func TestRouteChecksWhoseConnectionAndWhetherItIsThere(t *testing.T) {
	ctx := context.Background()
	withAPIKey(t, "sk-ant-api03-test-0123456789abcdef", nil)
	ok := func(s *routes.Selection) *agents.RouteRefusal { return agentImpl{}.CheckRoute(ctx, *s, "") }
	if ref := ok(sel(RouteAPI, "")); ref != nil {
		t.Errorf("own key, empty ref: %+v", ref)
	}
	if ref := ok(sel(RouteAPI, "ANTHROPIC_API_KEY")); ref != nil {
		t.Errorf("own key, named ref: %+v", ref)
	}
	if ref := ok(sel(RouteAPI, "someone-elses-key")); ref == nil || ref.Code != routes.CodeRouteForbidden || ref.Status != http.StatusForbidden {
		t.Errorf("another connection: %+v, want forbidden", ref)
	}
	if ref := ok(sel(RouteSubscription, "")); ref != nil {
		t.Errorf("subscription: %+v", ref)
	}
	if ref := ok(sel(RouteSubscription, "x")); ref == nil || ref.Code != routes.CodeRouteForbidden {
		t.Errorf("subscription with a connection: %+v, want forbidden", ref)
	}
	for name, err := range map[string]error{"unreadable store": errors.New("sealed")} {
		withAPIKey(t, "", err)
		if ref := ok(sel(RouteAPI, "")); ref == nil || ref.Code != routes.CodeRouteUnavailable {
			t.Errorf("%s: %+v, want unavailable", name, ref)
		}
	}
	withAPIKey(t, "  ", nil)
	if ref := ok(sel(RouteAPI, "")); ref == nil || ref.Code != routes.CodeRouteUnavailable {
		t.Errorf("no key: %+v, want unavailable", ref)
	}
	// The subscription route needs no key.
	if ref := ok(sel(RouteSubscription, "")); ref != nil {
		t.Errorf("subscription without a key: %+v", ref)
	}
	if _, err := (agentImpl{}).ApplyRoute(session.Meta{}, *sel(RouteAPI, "")); err == nil {
		t.Error("ApplyRoute with no key must fail, not launch without a credential")
	}
}

func TestBuildLaunchRouteEnvAndProgram(t *testing.T) {
	withAPIKey(t, "sk-ant-api03-secret-0123456789abcdef", nil)
	isolateSlot(t)
	base := session.Meta{Kind: session.KindClaude, Name: "r-none", Dir: t.TempDir()}
	none, err := launchOf(t, base)
	if err != nil {
		t.Fatal(err)
	}
	// An empty selection: no environment added, nothing removed beyond what it always removed.
	if len(none.Env) != 0 || strings.Contains(none.Program, "ANTHROPIC_API_KEY") || strings.Contains(none.Program, "CLAUDE_CODE_OAUTH_TOKEN") {
		t.Errorf("empty selection changed the launch: env=%v program=%q", none.Env, none.Program)
	}

	api := base
	api.Name, api.Route = "r-api", sel(RouteAPI, "")
	plan, err := launchOf(t, api)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(plan.Env, []string{"ANTHROPIC_API_KEY=sk-ant-api03-secret-0123456789abcdef"}) {
		t.Errorf("api plan env = %v", plan.Env)
	}
	if strings.Contains(plan.Program, "sk-ant") {
		t.Errorf("the key is in the command string (visible in /proc cmdline): %q", plan.Program)
	}
	if !strings.HasPrefix(plan.Program, "env -u ") || !strings.Contains(plan.Program, "-u CLAUDE_CODE_OAUTH_TOKEN") ||
		strings.Contains(plan.Program, "-u ANTHROPIC_API_KEY") || !strings.Contains(plan.Program, "claude ") {
		t.Errorf("api program = %q", plan.Program)
	}

	sub := base
	sub.Name, sub.Route = "r-sub", sel(RouteSubscription, "")
	plan, err = launchOf(t, sub)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Env) != 0 || !strings.Contains(plan.Program, "-u ANTHROPIC_API_KEY") || !strings.Contains(plan.Program, "-u CLAUDE_CODE_OAUTH_TOKEN") {
		t.Errorf("subscription plan = env %v program %q", plan.Env, plan.Program)
	}

	// A missing key stops the launch; it never falls through to a keyless launch.
	withAPIKey(t, "", nil)
	if _, err := launchOf(t, api); err == nil {
		t.Error("an api session launched with no key")
	}
}

// decision 9: the guard judges the session's own route.
func TestAuthExpiredForIsPerRoute(t *testing.T) {
	dir := isolateClaudeConfig(t)
	now := time.Now()
	writeCredsExpiry(t, dir, now.Add(-48*time.Hour), now.Add(-24*time.Hour)) // a dead login
	empty := session.Meta{Kind: session.KindClaude}
	api := session.Meta{Kind: session.KindClaude, Route: sel(RouteAPI, "")}
	sub := session.Meta{Kind: session.KindClaude, Route: sel(RouteSubscription, "")}

	if !AuthExpired() || !AuthExpiredFor(empty) {
		t.Fatal("the fixture's login should read as expired for an empty selection")
	}
	if AuthExpiredFor(api) {
		t.Error("an anthropic-api session is refused because the OAuth login expired")
	}
	if !AuthExpiredFor(sub) {
		t.Error("a subscription session must still see its login expired")
	}

	// An environment token in the Agent hides the expiry for an empty selection (today's rule)
	// but not for a subscription session, which runs without it.
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "sk-ant-oat01-env")
	if AuthExpiredFor(empty) != AuthExpired() || AuthExpired() {
		t.Error("an empty selection must keep answering exactly AuthExpired")
	}
	if !AuthExpiredFor(sub) {
		t.Error("a token in the Agent's environment hid the subscription session's expiry")
	}

	// A live login: nothing is expired anywhere.
	writeCredsExpiry(t, dir, now.Add(time.Hour), now.Add(48*time.Hour))
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	for name, m := range map[string]session.Meta{"empty": empty, "api": api, "sub": sub} {
		if AuthExpiredFor(m) {
			t.Errorf("%s: expired with a live login", name)
		}
	}
}

// The ledger sees the session's route, and API turns as metered; no selection stays unresolved.
func TestStampRoute(t *testing.T) {
	turns := []transcript.Turn{{}, {}}
	if got := stampRoute(session.Meta{}, turns); got[0].Route != "" || got[0].Billing != "" {
		t.Errorf("no selection stamped %+v", got[0])
	}
	got := stampRoute(session.Meta{Route: sel(RouteAPI, "")}, turns)
	if got[1].Route != RouteAPI || got[1].Billing != usagex.BillingMetered {
		t.Errorf("api turn = %+v", got[1])
	}
	if turns[0].Route != "" {
		t.Error("stampRoute wrote into the shared slice")
	}
	got = stampRoute(session.Meta{Route: sel(RouteSubscription, "")}, turns)
	if got[0].Route != RouteSubscription || got[0].Billing != "" {
		t.Errorf("subscription turn = %+v, want the route and an unresolved billing", got[0])
	}
	r := agentImpl{}.ResolveRoute(session.Meta{Route: sel(RouteAPI, "c")}, agents.TurnInfo{})
	if r.Route != RouteAPI || r.Connection != "c" || (agentImpl{}.ResolveRoute(session.Meta{}, agents.TurnInfo{}) != agents.ResolvedRoute{}) {
		t.Errorf("ResolveRoute = %+v", r)
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The CLI re-applies the env block and apiKeyHelper of its settings files over the process
// environment, which `env -u` cannot reach: an explicit route whose credential or endpoint they
// would override is refused with the file named, in every layer, for both routes; an empty
// selection launches as it always did whatever the files say.
func TestRouteLaunchRefusedOnSettingsConflict(t *testing.T) {
	withAPIKey(t, "sk-ant-api03-test-0123456789abcdef", nil)
	isolateSlot(t)
	cfg := isolateClaudeConfig(t)
	managed := t.TempDir()
	prev := managedSettingsDir
	managedSettingsDir = managed
	t.Cleanup(func() { managedSettingsDir = prev })

	dir := t.TempDir()
	layers := map[string]string{
		"user":          filepath.Join(cfg, "settings.json"),
		"user local":    filepath.Join(cfg, "settings.local.json"),
		"project":       filepath.Join(dir, ".claude", "settings.json"),
		"project local": filepath.Join(dir, ".claude", "settings.local.json"),
		"managed":       filepath.Join(managed, "managed-settings.json"),
		"managed.d":     filepath.Join(managed, "managed-settings.d", "10-x.json"),
	}
	for layer, path := range layers {
		for _, body := range []string{
			`{"env":{"ANTHROPIC_API_KEY":"sk-other"}}`,                 // the reverse route's key
			`{"env":{"CLAUDE_CODE_OAUTH_TOKEN":"sk-ant-oat01-x"}}`,     // a subscription token
			`{"env":{"ANTHROPIC_BASE_URL":"https://gateway.invalid"}}`, // a gateway for the injected key
			`{"env":{"ANTHROPIC_AUTH_TOKEN":"t"}}`,
			`{"apiKeyHelper":"/bin/get-key"}`,
		} {
			writeFile(t, path, body)
			for _, route := range []string{RouteAPI, RouteSubscription} {
				m := session.Meta{Kind: session.KindClaude, Name: "sc-" + strings.ReplaceAll(layer, " ", "-"), Dir: dir, Route: sel(route, "")}
				_, err := launchOf(t, m)
				var ref *agents.RouteRefusal
				if !errors.As(err, &ref) || ref.Code != routes.CodeRouteSettings || !strings.Contains(ref.Message, path) {
					t.Errorf("%s %s %s: err=%v, want a %s refusal naming the file", layer, body, route, err, routes.CodeRouteSettings)
				}
			}
			// Control: no selection, same file, the launch is exactly what it was.
			plain := session.Meta{Kind: session.KindClaude, Name: "sc-plain", Dir: dir}
			if plan, err := launchOf(t, plain); err != nil || len(plan.Env) != 0 || strings.HasPrefix(plan.Program, "env -u ") {
				t.Errorf("%s %s: an empty selection was affected: %v %+v", layer, body, err, plan)
			}
			_ = os.Remove(path)
		}
	}
	// Settings that do not touch the route are fine, and a file that is not JSON overrides nothing.
	writeFile(t, layers["project"], `{"env":{"FOO":"1"},"apiKeyHelper":""}`)
	writeFile(t, layers["user"], `not json`)
	m := session.Meta{Kind: session.KindClaude, Name: "sc-ok", Dir: dir, Route: sel(RouteAPI, "")}
	if _, err := launchOf(t, m); err != nil {
		t.Errorf("unrelated settings refused the launch: %v", err)
	}
}

// A stored connection reference is bound to the key instance: the fingerprint is not the key,
// a replaced key is refused at start, and an unbound reference (a fresh selection) is accepted.
func TestAPIKeyBinding(t *testing.T) {
	ctx := context.Background()
	withAPIKey(t, "sk-ant-api03-account-a", nil)
	bound, ref := agentImpl{}.BindRoute(*sel(RouteAPI, ""))
	if ref != nil || !strings.HasPrefix(bound.Connection, "ANTHROPIC_API_KEY@") || strings.Contains(bound.Connection, "account-a") {
		t.Fatalf("BindRoute = %+v %v", bound, ref)
	}
	if ref := (agentImpl{}).CheckRoute(ctx, bound, ""); ref != nil {
		t.Errorf("the same key: %+v", ref)
	}
	plan, err := agentImpl{}.ApplyRoute(session.Meta{}, bound)
	if err != nil || plan.Env[0] != "ANTHROPIC_API_KEY=sk-ant-api03-account-a" {
		t.Fatalf("ApplyRoute on the same key: %+v %v", plan, err)
	}
	withAPIKey(t, "sk-ant-api03-account-b", nil)
	if ref := (agentImpl{}).CheckRoute(ctx, bound, ""); ref == nil || ref.Code != routes.CodeRouteUnavailable {
		t.Errorf("replaced key: %+v, want unavailable", ref)
	}
	if _, err := (agentImpl{}).ApplyRoute(session.Meta{}, bound); err == nil {
		t.Error("ApplyRoute launched on a replaced key")
	}
	// A new selection on the new key binds to it; the subscription route binds to nothing.
	if b2, _ := (agentImpl{}).BindRoute(*sel(RouteAPI, "")); b2.Connection == bound.Connection {
		t.Error("two keys share a fingerprint")
	}
	if b, _ := (agentImpl{}).BindRoute(*sel(RouteSubscription, "")); b.Connection != "" {
		t.Errorf("subscription bound to %q", b.Connection)
	}
	withAPIKey(t, "", nil)
	if _, ref := (agentImpl{}).BindRoute(*sel(RouteAPI, "")); ref == nil {
		t.Error("bound a selection with no key")
	}
}

// BindRoute reads the key once and checks a fingerprint already on the selection against that
// read: a key replaced between CheckRoute and BindRoute is refused, never re-bound silently.
func TestBindRouteDoesNotRebindAReplacedKey(t *testing.T) {
	withAPIKey(t, "sk-ant-api03-account-a", nil)
	boundA, _ := agentImpl{}.BindRoute(*sel(RouteAPI, ""))
	if ref := (agentImpl{}).CheckRoute(context.Background(), boundA, ""); ref != nil {
		t.Fatalf("check on A: %+v", ref)
	}
	withAPIKey(t, "sk-ant-api03-account-b", nil) // replaced after the check
	if got, ref := (agentImpl{}).BindRoute(boundA); ref == nil || ref.Code != routes.CodeRouteUnavailable {
		t.Errorf("BindRoute of A over key B = %+v %v, want a refusal", got, ref)
	}
	withAPIKey(t, "", errors.New("sealed"))
	if _, ref := (agentImpl{}).BindRoute(boundA); ref == nil || ref.Code != routes.CodeRouteUnavailable {
		t.Errorf("an unreadable store bound anyway: %+v", ref)
	}
}

// Foundry is another selector the CLI honours: removed for both routes, and refused in settings.
func TestFoundryIsIsolated(t *testing.T) {
	withAPIKey(t, "sk-ant-api03-test-0123456789abcdef", nil)
	for _, route := range []string{RouteAPI, RouteSubscription} {
		plan, err := agentImpl{}.ApplyRoute(session.Meta{}, *sel(route, ""))
		if err != nil || !slices.Contains(plan.Unset, "CLAUDE_CODE_USE_FOUNDRY") {
			t.Errorf("%s: unset %v err %v, want CLAUDE_CODE_USE_FOUNDRY", route, plan.Unset, err)
		}
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".claude", "settings.json"), `{"env":{"CLAUDE_CODE_USE_FOUNDRY":"1"}}`)
	isolateClaudeConfig(t)
	m := session.Meta{Kind: session.KindClaude, Name: "f1", Dir: dir, Route: sel(RouteAPI, "")}
	if ref := (agentImpl{}).RouteHeld(m); ref == nil || ref.Code != routes.CodeRouteSettings {
		t.Errorf("RouteHeld = %+v", ref)
	}
}

// A running session's route is watched: a settings file that starts overriding it after launch is
// reported, an untouched one is not, and a session with no selection is never judged.
func TestRouteHeldWatchesRunningSessions(t *testing.T) {
	cfg := isolateClaudeConfig(t)
	dir := t.TempDir()
	managed := t.TempDir()
	prev := managedSettingsDir
	managedSettingsDir = managed
	t.Cleanup(func() { managedSettingsDir = prev })
	api := session.Meta{Kind: session.KindClaude, Name: "w1", Dir: dir, Route: sel(RouteAPI, "")}
	none := session.Meta{Kind: session.KindClaude, Name: "w2", Dir: dir}
	if ref := (agentImpl{}).RouteHeld(api); ref != nil {
		t.Fatalf("held route reported: %+v", ref)
	}
	writeFile(t, filepath.Join(cfg, "settings.json"), `{"env":{"ANTHROPIC_BASE_URL":"https://gateway.invalid"}}`)
	if ref := (agentImpl{}).RouteHeld(api); ref == nil || ref.Code != routes.CodeRouteSettings {
		t.Errorf("a later settings edit went unnoticed: %+v", ref)
	}
	if ref := (agentImpl{}).RouteHeld(none); ref != nil {
		t.Errorf("a session with no selection was judged: %+v", ref)
	}
}

func approvalsIn(t *testing.T) (approved, rejected []string, root map[string]any) {
	t.Helper()
	b, err := os.ReadFile(claudeJSONPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &root); err != nil {
		t.Fatal(err)
	}
	resp, _ := root["customApiKeyResponses"].(map[string]any)
	for _, v := range resp["approved"].([]any) {
		approved = append(approved, fmt.Sprint(v))
	}
	for _, v := range resp["rejected"].([]any) {
		rejected = append(rejected, fmt.Sprint(v))
	}
	return
}

// The CLI's "use this API key?" prompt preselects No (= the claude.ai login), so an anthropic-api
// launch records the Yes beforehand: only the key's last 20 characters, and an earlier No for the
// same key is withdrawn.
func TestBuildLaunchPreApprovesTheBoundKey(t *testing.T) {
	const key = "sk-ant-api03-SECRETSECRETSECRETSECRETSECRET-tailtailtailtail"
	withAPIKey(t, key, nil)
	isolateSlot(t)
	suffix := key[len(key)-20:]
	pre := `{"keep":{"a":1},"customApiKeyResponses":{"approved":["other"],"rejected":["` + suffix + `","x"]}}`
	if err := os.WriteFile(claudeJSONPath(), []byte(pre), 0o600); err != nil {
		t.Fatal(err)
	}
	m := session.Meta{Kind: session.KindClaude, Name: "r-appr", Dir: t.TempDir(), Route: sel(RouteAPI, "")}
	if _, err := launchOf(t, m); err != nil {
		t.Fatal(err)
	}
	approved, rejected, root := approvalsIn(t)
	if !slices.Equal(approved, []string{"other", suffix}) || !slices.Equal(rejected, []string{"x"}) {
		t.Errorf("approved=%v rejected=%v", approved, rejected)
	}
	if root["keep"] == nil {
		t.Error("an unrelated key of .claude.json was lost")
	}
	b, _ := os.ReadFile(claudeJSONPath())
	if strings.Contains(string(b), key) {
		t.Error("the whole key was written to .claude.json")
	}
	// A second launch changes nothing.
	if _, err := launchOf(t, m); err != nil {
		t.Fatal(err)
	}
	if a2, _, _ := approvalsIn(t); !slices.Equal(a2, approved) {
		t.Errorf("approval duplicated: %v", a2)
	}
}

// No selection and the subscription route record no approval: nothing about them changes.
func TestBuildLaunchWithoutAPIRouteRecordsNoApproval(t *testing.T) {
	withAPIKey(t, "sk-ant-api03-whatever-whatever-whatever", nil)
	isolateSlot(t)
	for _, r := range []*routes.Selection{nil, sel(RouteSubscription, "")} {
		m := session.Meta{Kind: session.KindClaude, Name: "r-noappr", Dir: t.TempDir(), Route: r}
		if _, err := launchOf(t, m); err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(claudeJSONPath())
		if strings.Contains(string(b), "customApiKeyResponses") {
			t.Errorf("route %v wrote an approval: %s", r, b)
		}
	}
}

// A config dir that cannot hold the file stops the launch: a pane that waits on the prompt is not
// an option. A file in place of the directory fails for every uid, root included.
func TestBuildLaunchRefusesWhenApprovalCannotBeStored(t *testing.T) {
	withAPIKey(t, "sk-ant-api03-whatever-whatever-whatever", nil)
	isolateSlot(t)
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", blocker)
	m := session.Meta{Kind: session.KindClaude, Name: "r-bad", Dir: t.TempDir(), Route: sel(RouteAPI, "")}
	_, err := launchOf(t, m)
	var ref *agents.RouteRefusal
	if !errors.As(err, &ref) || ref.Code != routes.CodeRouteUnavailable {
		t.Errorf("err = %v, want a route_unavailable refusal", err)
	}
}

// A corrupt .claude.json is set aside and rebuilt for the trust step; the approval then lands.
// A key too short to store without writing it whole is refused.
func TestApprovalSelfHealsCorruptFileAndRefusesShortKey(t *testing.T) {
	isolateSlot(t)
	const key = "sk-ant-api03-whatever-whatever-whatever"
	if err := os.WriteFile(claudeJSONPath(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	ensureFolderTrusted(t.TempDir())
	if err := approveAPIKey(key); err != nil {
		t.Fatal(err)
	}
	if a, _, _ := approvalsIn(t); len(a) != 1 {
		t.Errorf("approved = %v", a)
	}
	if err := approveAPIKey("short-key"); err == nil {
		t.Error("a short key was accepted")
	}
	if b, _ := os.ReadFile(claudeJSONPath()); strings.Contains(string(b), "short-key") {
		t.Error("short key written")
	}
}

// Concurrent launches lose no update and leave valid JSON.
func TestApprovalsAreSerialised(t *testing.T) {
	isolateSlot(t)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ensureFolderTrusted(t.TempDir())
			_ = approveAPIKey(fmt.Sprintf("sk-ant-api03-xxxxxxxxxxxxxxxx-yyyyyyyyyy%04d", i))
		}()
	}
	wg.Wait()
	if a, _, _ := approvalsIn(t); len(a) != 12 {
		t.Errorf("approved = %d entries, want 12", len(a))
	}
}

// The refusal names the place the key is really entered.
func TestNoAPIKeyRefusalNamesTheRealSettingsPath(t *testing.T) {
	msg := noAPIKey().Message
	if !strings.Contains(msg, "Settings > Connections > Agents > the OpenCode card > API keys") || strings.Contains(msg, "provider keys") {
		t.Errorf("refusal = %q", msg)
	}
}

// The flag layer pins what `env -u` removes, in the one --settings object, so a settings edit made
// while the pane runs cannot put it back; the route's own key is never in it.
func TestRouteLaunchPinsOverridableVariablesInTheFlagLayer(t *testing.T) {
	const key = "sk-ant-api03-pin-0123456789abcdefghij"
	withAPIKey(t, key, nil)
	isolateSlot(t)
	base := session.Meta{Kind: session.KindClaude, Dir: t.TempDir()}

	envOf := func(plan agents.LaunchPlan) (map[string]any, map[string]any) {
		doc := launchSettingsOf(t, plan.Program)
		env, _ := doc["env"].(map[string]any)
		return doc, env
	}

	api := base
	api.Name, api.Route = "pin-api", sel(RouteAPI, "")
	plan, err := launchOf(t, api)
	if err != nil {
		t.Fatal(err)
	}
	doc, env := envOf(plan)
	for _, v := range []string{"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_USE_BEDROCK",
		"CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY", "CLAUDE_CODE_OAUTH_TOKEN"} {
		if got, ok := env[v]; !ok || got != "" {
			t.Errorf("api: %s not pinned empty (env=%v)", v, env)
		}
	}
	if _, ok := env["ANTHROPIC_API_KEY"]; ok {
		t.Error("api: the route's own key variable must not be in the flag layer")
	}
	if h, ok := doc["apiKeyHelper"]; !ok || h != "" {
		t.Errorf("api: apiKeyHelper = %v, want pinned empty", h)
	}
	if doc["permissions"] == nil {
		t.Error("the launch's own settings were lost in the merge")
	}
	if strings.Contains(plan.Program, "sk-ant") {
		t.Error("the key is in the command line")
	}

	sub := base
	sub.Name, sub.Route = "pin-sub", sel(RouteSubscription, "")
	plan, err = launchOf(t, sub)
	if err != nil {
		t.Fatal(err)
	}
	_, env = envOf(plan)
	for _, v := range []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_BASE_URL"} {
		if got, ok := env[v]; !ok || got != "" {
			t.Errorf("subscription: %s not pinned empty (env=%v)", v, env)
		}
	}

	// No selection: the settings are exactly the launch's own, nothing pinned.
	none := base
	none.Name = "pin-none"
	plan, err = launchOf(t, none)
	if err != nil {
		t.Fatal(err)
	}
	doc = launchSettingsOf(t, plan.Program)
	if _, ok := doc["env"]; ok {
		t.Errorf("no route: env block appeared: %v", doc["env"])
	}
	if _, ok := doc["apiKeyHelper"]; ok {
		t.Error("no route: apiKeyHelper appeared")
	}
}

// timedErr is apiErr stamped with a transcript timestamp.
func timedErr(text string, status int, at time.Time) string {
	var rec map[string]any
	_ = json.Unmarshal([]byte(apiErr(text, status)), &rec)
	rec["timestamp"] = at.UTC().Format(time.RFC3339Nano)
	b, _ := json.Marshal(rec)
	return string(b)
}

// After a switch the ledger files each turn under the selection in force at its own timestamp
// (decision 8): a turn from before the boundary is not refiled under the new route, and one the
// clock cannot place stays unresolved.
func TestStampRouteFollowsTheSwitchBoundary(t *testing.T) {
	at := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	turn := func(ts string) transcript.Turn { return transcript.Turn{TS: ts} }
	turns := []transcript.Turn{
		turn(at.Add(-time.Hour).Format(time.RFC3339)), turn(at.Add(time.Hour).Format(time.RFC3339)), turn(""),
	}
	explicit := session.Meta{Route: sel(RouteAPI, "ANTHROPIC_API_KEY@abc"), RouteSwitches: []session.RouteSwitch{
		{At: at.Format(time.RFC3339Nano), From: sel(RouteSubscription, ""), To: *sel(RouteAPI, "ANTHROPIC_API_KEY@abc")},
	}}
	got := stampRoute(explicit, turns)
	if got[0].Route != RouteSubscription || got[0].Billing != "" {
		t.Errorf("before the boundary = %+v, want subscription, billing unresolved", got[0])
	}
	if got[1].Route != RouteAPI || got[1].Billing != usagex.BillingMetered {
		t.Errorf("after the boundary = %+v, want anthropic-api metered", got[1])
	}
	if got[2].Route != "" || got[2].Billing != "" {
		t.Errorf("turn without a timestamp = %+v, want unresolved", got[2])
	}
	// A session that had NO explicit selection before: its earlier turns stay unresolved, as they
	// always were.
	empty := explicit
	empty.RouteSwitches = []session.RouteSwitch{{At: at.Format(time.RFC3339Nano), To: *sel(RouteAPI, "ANTHROPIC_API_KEY@abc")}}
	got = stampRoute(empty, turns)
	if got[0].Route != "" || got[1].Route != RouteAPI {
		t.Errorf("empty-before: %+v / %+v", got[0], got[1])
	}
}

// A limit recorded on the route a session left is not evidence of a limit on the one it entered,
// while the transcript tail still shows it; a limit written after the boundary is.
func TestAtLimitIgnoresALimitFromBeforeTheSwitch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	tr := agentImpl{}.UsageLimitTraits()
	boundary := time.Now()
	msg := "You've hit your 5-hour limit · resets 1:50am (Asia/Tokyo)"
	for i, tc := range []struct {
		name  string
		at    time.Time
		since bool
		want  bool
	}{
		{"never switched", boundary.Add(-time.Hour), false, true},
		{"limit before the switch", boundary.Add(-time.Hour), true, false},
		{"limit after the switch", boundary.Add(time.Hour), true, true},
		{"limit with no timestamp, switched", time.Time{}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := session.Meta{Name: fmt.Sprintf("swl%d", i), Dir: filepath.Join(home, fmt.Sprintf("w%d", i)), Kind: session.KindClaude,
				Route: sel(RouteAPI, "")}
			if tc.since {
				m.RouteSwitches = []session.RouteSwitch{{At: boundary.UTC().Format(time.RFC3339Nano), From: sel(RouteSubscription, ""), To: *sel(RouteAPI, "")}}
			}
			line := apiErr(msg, 429)
			if !tc.at.IsZero() {
				line = timedErr(msg, 429, tc.at)
			}
			dir := filepath.Join(ConfigDir(), "projects", "-proj")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, session.UUID(m.Dir, m.Name)+".jsonl"), []byte(userLine("go")+"\n"+line+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, at := tr.AtLimit(m); at != tc.want {
				t.Fatalf("AtLimit = %v, want %v", at, tc.want)
			}
		})
	}
}

// Decision 10 / 2 / 6: which sessions the harness offers the metered route to. An empty selection
// is never one: nothing readable afterwards shows which credential its pane inherited.
func TestSwitchTarget(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	a := agentImpl{}
	code := func(r *agents.RouteRefusal) string {
		if r == nil {
			return ""
		}
		return r.Code
	}
	if to, ref := a.SwitchTarget(session.Meta{Route: sel(RouteSubscription, "")}); ref != nil || to.Route != RouteAPI {
		t.Errorf("explicit subscription = %+v %v", to, ref)
	}
	if _, ref := a.SwitchTarget(session.Meta{Route: sel(RouteAPI, "")}); code(ref) != routes.CodeSwitchNotSubscription {
		t.Errorf("api route = %v", ref)
	}
	// Even with a clean environment and a live OAuth login on disk: an API session that outlived
	// an Agent restart (its key in the pane's environment) looks exactly like this.
	creds := fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"tok","expiresAt":%d,"refreshTokenExpiresAt":%d}}`,
		time.Now().Add(time.Hour).UnixMilli(), time.Now().Add(24*time.Hour).UnixMilli())
	writeFile(t, filepath.Join(ConfigDir(), ".credentials.json"), creds)
	if _, ref := a.SwitchTarget(session.Meta{Name: "e1", Dir: filepath.Join(home, "w")}); code(ref) != routes.CodeSwitchUnproven {
		t.Errorf("empty selection = %v, want unproven", ref)
	}
}

// A collector holds a meta listed before a switch: the rows it makes after the switch still carry
// the boundary on disk, and its source stamp changes with it.
func TestUsageSourceUsesTheRouteOnDiskNow(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	m := session.Meta{Name: "ust1", Dir: filepath.Join(home, "w"), Kind: session.KindClaude, Route: sel(RouteSubscription, "")}
	at := time.Now().Add(-time.Hour)
	line := fmt.Sprintf(`{"type":"assistant","timestamp":%q,"requestId":"r1","message":{"id":"m1","model":"claude-x","role":"assistant","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":3,"output_tokens":4}}}`,
		time.Now().UTC().Format(time.RFC3339Nano))
	_ = at
	dir := filepath.Join(ConfigDir(), "projects", "-proj")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, session.UUID(m.Dir, m.Name)+".jsonl"), []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stale, ok := agentImpl{}.UsageSource(m)
	if !ok {
		t.Fatal("no source")
	}
	// The switch lands after the collector listed m, before it parses.
	switched := m
	switched.Route = sel(RouteAPI, "ANTHROPIC_API_KEY@abc")
	switched.RouteSwitches = []session.RouteSwitch{{At: time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano),
		From: sel(RouteSubscription, ""), To: *switched.Route}}
	session.WriteMeta(switched)
	turns, _ := stale.Turns()
	var got *transcript.Turn
	for i := range turns {
		if turns[i].Role == "assistant" {
			got = &turns[i]
		}
	}
	if got == nil || got.Route != RouteAPI || got.Billing != usagex.BillingMetered {
		t.Fatalf("stale collector filed the turn as %+v, want anthropic-api metered", got)
	}
	fresh, _ := agentImpl{}.UsageSource(switched)
	if fresh.Stamp == stale.Stamp {
		t.Error("the source stamp ignores the route, so a row filed before the switch would be kept")
	}
}

// A meta that is momentarily unreadable (a writer truncates before it writes) is not proof the
// selection in the collector's older copy still holds: the source declines to answer, and a
// later read of the restored meta is attributed correctly.
func TestUsageSourceDoesNotAttributeByAMetaItCannotRead(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	m := session.Meta{Name: "ust2", Dir: filepath.Join(home, "w"), Kind: session.KindClaude, Route: sel(RouteAPI, "ANTHROPIC_API_KEY@abc")}
	session.WriteMeta(m)
	line := fmt.Sprintf(`{"type":"assistant","timestamp":%q,"requestId":"r1","message":{"id":"m1","model":"claude-x","role":"assistant","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":3,"output_tokens":4}}}`,
		time.Now().UTC().Format(time.RFC3339Nano))
	dir := filepath.Join(ConfigDir(), "projects", "-proj")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, session.UUID(m.Dir, m.Name)+".jsonl"), []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	src, ok := agentImpl{}.UsageSource(m)
	if !ok {
		t.Fatal("no source")
	}
	stale := m
	stale.Route = sel(RouteSubscription, "") // the copy the collector listed before the switch
	if err := os.WriteFile(session.MetaPath(m.Name), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	turns, complete := src.Turns()
	for _, tn := range turns {
		if tn.Role == "assistant" && tn.Route == RouteSubscription {
			t.Fatalf("a turn was filed under the stale subscription: %+v", tn)
		}
	}
	if complete {
		t.Error("an unresolved attribution was reported as a complete read")
	}
	if _, ok := (agentImpl{}).UsageSource(stale); ok {
		t.Error("a stamp was issued without being able to read the route")
	}
	session.WriteMeta(m)
	if _, ok := (agentImpl{}).UsageSource(stale); !ok {
		t.Error("no source once the meta is readable again")
	}
}
