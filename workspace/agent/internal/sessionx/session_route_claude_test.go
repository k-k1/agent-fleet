package sessionx

import (
	"fmt"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func setAnthropicKey(t *testing.T, key string) {
	t.Helper()
	if err := secrets.Update(func(d *secrets.Data) error {
		if d.Opencode == nil {
			d.Opencode = map[string]string{}
		}
		if key == "" {
			delete(d.Opencode, "ANTHROPIC_API_KEY")
		} else {
			d.Opencode["ANTHROPIC_API_KEY"] = key
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// claude's real adapter through the real create handler: the launch carries the key in its
// environment only, and a missing key stops the create before anything is made.
func TestCreateClaudeRouteThroughTheRealAdapter(t *testing.T) {
	p := newCreateProbe(t)
	var launched []session.Meta
	launchTmuxFn = func(m session.Meta, _ bool) error { launched = append(launched, m); return nil }
	body := func(route map[string]any, extra map[string]any) map[string]any {
		b := map[string]any{"kind": "claude", "dir": p.repo, "route": route}
		for k, v := range extra {
			b[k] = v
		}
		return b
	}

	// No key connected: refused, nothing created.
	before := p.sideEffects()
	rec := p.post(body(map[string]any{"route": "anthropic-api"}, nil), "")
	if rec.Code != http.StatusConflict || routeCode(t, rec.Body.String()) != routes.CodeRouteUnavailable {
		t.Errorf("no key: %d %s, want 409 %s", rec.Code, rec.Body.String(), routes.CodeRouteUnavailable)
	}
	if d := before.diff(p.sideEffects()); d != "" || len(launched) != 0 {
		t.Errorf("a refused create left %q and launched %d", d, len(launched))
	}

	// Another connection's reference: forbidden, key or no key.
	setAnthropicKey(t, "sk-ant-api03-test")
	rec = p.post(body(map[string]any{"route": "anthropic-api", "connection": "other-member-conn"}, nil), "")
	if rec.Code != http.StatusForbidden || routeCode(t, rec.Body.String()) != routes.CodeRouteForbidden {
		t.Errorf("foreign connection: %d %s, want 403 %s", rec.Code, rec.Body.String(), routes.CodeRouteForbidden)
	}

	// The subscription route needs no key.
	setAnthropicKey(t, "")
	if rec = p.post(body(map[string]any{"route": "anthropic-subscription"}, nil), ""); rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Errorf("subscription create: %d %s", rec.Code, rec.Body.String())
	}

	// Allowed: stored on the meta.
	setAnthropicKey(t, "sk-ant-api03-test")
	launched = nil
	if rec = p.post(body(map[string]any{"route": "anthropic-api"}, nil), ""); rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("api create: %d %s", rec.Code, rec.Body.String())
	}
	if len(launched) != 1 || launched[0].Route == nil || launched[0].Route.Route != "anthropic-api" {
		t.Fatalf("launched %+v", launched)
	}

	// Terminal only: claude has no managed driver, so a managed create naming a route is refused
	// visibly, not run on a default.
	launched = nil
	before = p.sideEffects()
	rec = p.post(body(map[string]any{"route": "anthropic-api"}, map[string]any{"driver": "managed"}), "")
	if rec.Code != http.StatusBadRequest || routeCode(t, rec.Body.String()) != "driver_unsupported" || len(launched) != 0 {
		t.Errorf("managed claude with a route: %d %s launched=%d, want driver_unsupported", rec.Code, rec.Body.String(), len(launched))
	}
	if d := before.diff(p.sideEffects()); d != "" {
		t.Errorf("left side effects: %s", d)
	}
}

// A stored selection whose key was later removed stops the start with the route code.
func TestClaudeStartRefusedWhenTheKeyIsGone(t *testing.T) {
	setAnthropicKeyStore(t)
	m := session.Meta{Name: "ck1", Kind: "claude", Route: &routes.Selection{Route: "anthropic-api"}}
	if ref := startRefusalOf(m); ref == nil || ref.Code != routes.CodeRouteUnavailable {
		t.Errorf("start with no key: %+v", ref)
	}
	setAnthropicKey(t, "sk-ant-api03-test")
	if ref := startRefusalOf(m); ref != nil {
		t.Errorf("start with a key: %+v", ref)
	}
}

func setAnthropicKeyStore(t *testing.T) { t.Helper(); isolateAgentState(t) }

// decision 9 at the send gate: an expired OAuth login refuses an empty selection and a
// subscription session, and does not refuse an API-route session.
func TestSendGateJudgesTheSessionsRoute(t *testing.T) {
	isolateAgentState(t)
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	now := time.Now()
	creds := fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"tok","expiresAt":%d,"refreshTokenExpiresAt":%d}}`,
		now.Add(-48*time.Hour).UnixMilli(), now.Add(-24*time.Hour).UnixMilli())
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(creds), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		route *routes.Selection
		want  string
	}{
		"empty selection": {nil, "auth"},
		"subscription":    {&routes.Selection{Route: "anthropic-subscription"}, "auth"},
		"api":             {&routes.Selection{Route: "anthropic-api"}, ""},
	} {
		n := "gate-" + name[:3]
		session.WriteMeta(session.Meta{Name: n, Kind: "claude", Dir: t.TempDir(), Route: tc.route, CreatedAt: "2026-10-29T10:00:00+09:00"})
		if got := promptBlocker(n); got != tc.want {
			t.Errorf("%s: promptBlocker = %q, want %q", name, got, tc.want)
		}
		m, _ := session.ReadMeta(n)
		if got := authExpiredFor(m); got != (tc.want != "") {
			t.Errorf("%s: authExpiredFor = %v", name, got)
		}
	}
}

// Finding: an API-key failure must not be "recovered" by signing in to the subscription. The
// OAuth login moment advances, and a session on an explicit anthropic-api route is still not
// resumed; an empty selection on the same failure is (the unchanged control).
func TestAPIRouteIsNotResumedOnAnOAuthRenewal(t *testing.T) {
	f := newAbortFixture(t)
	cut := time.Now()
	a := authAbort(cut)
	setLoginAt(t, cut.Add(10*time.Minute)) // the subscription login is renewed AFTER the failure
	late := cut.Add(10*time.Minute + authResumeSettle + time.Second)

	api := session.Meta{Name: "ar-api", Dir: "/tmp/ar-api", Kind: session.KindClaude, Route: &routes.Selection{Route: "anthropic-api"}}
	authResumeAttempt(api, arState(t, api.Name), a, cut.Add(time.Minute))
	authResumeAttempt(api, arState(t, api.Name), a, late)
	authResumeStep(api, a, true, late)
	if len(f.sent) != 0 {
		t.Fatalf("an API-key session was resumed on the OAuth login's renewal: %v", f.sent)
	}
	if _, open := authResumeStates.Read(api.Name); open {
		t.Error("an episode was opened for an API-key session")
	}

	plain := session.Meta{Name: "ar-plain", Dir: "/tmp/ar-plain", Kind: session.KindClaude}
	authResumeAttempt(plain, arState(t, plain.Name), a, cut.Add(time.Minute))
	authResumeAttempt(plain, arState(t, plain.Name), a, late)
	if len(f.sent) != 1 {
		t.Fatalf("control: an empty selection should still resume once renewed, sent %d", len(f.sent))
	}
}

// A stored anthropic-api selection is bound to the key it was chosen on: replacing the key in
// Settings moves nothing, the next start is refused (no silent change of bill). The stored form
// carries a fingerprint and never the key.
func TestAPIRouteIsBoundToTheKeyInstance(t *testing.T) {
	p := newCreateProbe(t)
	var launched []session.Meta
	launchTmuxFn = func(m session.Meta, _ bool) error { launched = append(launched, m); return nil }
	setAnthropicKey(t, "sk-ant-api03-account-a")
	rec := p.post(map[string]any{"kind": "claude", "dir": p.repo, "route": map[string]any{"route": "anthropic-api"}}, "")
	if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	m := launched[0]
	if m.Route == nil || !strings.HasPrefix(m.Route.Connection, "ANTHROPIC_API_KEY@") || strings.Contains(m.Route.Connection, "account-a") {
		t.Fatalf("stored selection = %+v, want a fingerprint-bound reference without the key", m.Route)
	}
	if ref := startRefusalOf(m); ref != nil {
		t.Errorf("start on the same key: %+v", ref)
	}
	setAnthropicKey(t, "sk-ant-api03-account-b")
	if ref := startRefusalOf(m); ref == nil || ref.Code != routes.CodeRouteUnavailable {
		t.Errorf("start after the key was replaced: %+v, want %s", ref, routes.CodeRouteUnavailable)
	}
	setAnthropicKey(t, "")
	if ref := startRefusalOf(m); ref == nil || ref.Code != routes.CodeRouteUnavailable {
		t.Errorf("start after the key was removed: %+v", ref)
	}
	// The wire reports the selection, so the Control Plane can compare it.
	if w := wireSession(m, false); w.Route == nil || w.Route.Route != "anthropic-api" {
		t.Errorf("wire route = %+v", w.Route)
	}
	if w := wireSession(session.Meta{Name: "x", Kind: "claude", Dir: t.TempDir()}, false); w.Route != nil {
		t.Errorf("wire route of a session with none = %+v", w.Route)
	}
}

// A settings file that sets the route's credential or endpoint is refused at create, with the
// route's own code, before anything is made; no selection is unaffected.
func TestClaudeRouteRefusedOnSettingsConflict(t *testing.T) {
	p := newCreateProbe(t)
	cfg := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	setAnthropicKey(t, "sk-ant-api03-test")
	if err := os.WriteFile(filepath.Join(cfg, "settings.json"), []byte(`{"env":{"ANTHROPIC_API_KEY":"sk-other"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	before := p.sideEffects()
	for _, route := range []string{"anthropic-api", "anthropic-subscription"} {
		rec := p.post(map[string]any{"kind": "claude", "dir": p.repo, "route": map[string]any{"route": route}}, "")
		if rec.Code != http.StatusConflict || routeCode(t, rec.Body.String()) != routes.CodeRouteSettings {
			t.Errorf("%s: %d %s, want 409 %s", route, rec.Code, rec.Body.String(), routes.CodeRouteSettings)
		}
	}
	if d := before.diff(p.sideEffects()); d != "" {
		t.Errorf("left side effects: %s", d)
	}
	var launched int
	launchTmuxFn = func(session.Meta, bool) error { launched++; return nil }
	if rec := p.post(map[string]any{"kind": "claude", "dir": p.repo}, ""); rec.Code != http.StatusOK && rec.Code != http.StatusCreated || launched != 1 {
		t.Errorf("no selection with the same settings: %d launched=%d, want unchanged", rec.Code, launched)
	}
}

// A settings edit after launch stops the running session and says why; a session whose route
// still holds, and one with no selection, keep running.
func TestSweepRouteGuardsStopsAConflictedSession(t *testing.T) {
	isolateAgentState(t)
	setAnthropicKey(t, "sk-ant-api03-test")
	cfg := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	mk := func(name string, r *routes.Selection) session.Meta {
		m := session.Meta{Name: name, Kind: "claude", Dir: t.TempDir(), Route: r, CreatedAt: "2026-10-29T10:00:00+09:00"}
		session.WriteMeta(m)
		if out, err := tmuxx.Cmd("new-session", "-d", "-s", session.TmuxName(name), "sleep 120").CombinedOutput(); err != nil {
			t.Fatalf("tmux: %v %s", err, out)
		}
		return m
	}
	api := mk("rg-api", &routes.Selection{Route: "anthropic-api"})
	plain := mk("rg-none", nil)
	alive := func(m session.Meta) bool { return tmuxx.HasSession(session.TmuxName(m.Name)) }

	SweepRouteGuards(time.Now())
	if !alive(api) || !alive(plain) {
		t.Fatal("a session whose route holds was stopped")
	}
	if err := os.WriteFile(filepath.Join(cfg, "settings.json"), []byte(`{"env":{"ANTHROPIC_API_KEY":"sk-other"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	SweepRouteGuards(time.Now())
	if alive(api) {
		t.Error("the conflicted session is still running")
	}
	if !alive(plain) {
		t.Error("a session with no selection was stopped")
	}
	// Starting it again is refused with the cause, not run on the default.
	m, _ := session.ReadMeta(api.Name)
	if ref := startRefusalOf(m); ref == nil || ref.Code != routes.CodeRouteSettings {
		t.Errorf("restart: %+v, want %s", ref, routes.CodeRouteSettings)
	}
}
