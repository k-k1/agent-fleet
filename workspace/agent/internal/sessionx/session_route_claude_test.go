package sessionx

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
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
