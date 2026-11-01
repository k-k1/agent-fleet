package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// signInSteps are the token-storing steps of the agent sign-in flows, each audited as
// connection.<kind>.signin once the Agent says the credential is stored.
var signInSteps = map[string]string{
	"POST /api/connections/agy/complete":        "connection.agy.signin",
	"POST /api/connections/claude/complete":     "connection.claude.signin",
	"POST /api/connections/codex/device/poll":   "connection.codex.signin",
	"POST /api/connections/cursor/poll":         "connection.cursor.signin",
	"POST /api/connections/kiro/poll":           "connection.kiro.signin",
	"POST /api/connections/muse/poll":           "connection.muse.signin",
	"POST /api/connections/opencode/oauth/poll": "connection.opencode.signin",
}

// TestSignInStepsAuditOnlyWhenStored drives each sign-in step through the proxy twice: a poll the
// Agent answers "not yet" writes no row (the Console polls every couple of seconds), the answer
// that stored the credential writes exactly one.
func TestSignInStepsAuditOnlyWhenStored(t *testing.T) {
	only := func(route string) bool { _, ok := signInSteps[route]; return ok }
	agentPaths := func(answer string) map[string]string {
		out := map[string]string{}
		for route := range signInSteps {
			m, p, _ := strings.Cut(route, " ")
			out[m+" "+strings.TrimPrefix(p, "/api")] = answer
		}
		return out
	}
	for name, answer := range map[string]string{
		"pending":   `{"connected":false}`,
		"failed":    `{"connected":false,"failed":true,"detail":"expired"}`,
		"not json":  `pending`,
		"empty":     `{}`,
		"slow_down": `{"connected":false,"status":"slow_down"}`,
	} {
		for _, p := range probeRelaysWith(t, only, agentPaths(answer)) {
			if !p.relayed() {
				t.Errorf("%s (%s): not relayed to the Agent", p.route, name)
			}
			if len(p.audits) != 0 {
				t.Errorf("%s answered %s (%s): wrote %v, want no row", p.route, answer, name, p.audits)
			}
		}
	}
	stored := probeRelaysWith(t, only, agentPaths(`{"connected":true}`))
	if len(stored) != len(signInSteps) {
		t.Fatalf("probed %d sign-in steps, want %d", len(stored), len(signInSteps))
	}
	for _, p := range stored {
		if len(p.audits) != 1 || p.audits[0] != signInSteps[p.route] {
			t.Errorf("%s stored: audits %v, want [%s]", p.route, p.audits, signInSteps[p.route])
		}
	}
}

// TestCredentialWriteTargets pins the action and the target of each kind of credential write
// through the real table rows, and that nothing but a name from the URL can reach the target.
func TestCredentialWriteTargets(t *testing.T) {
	const secret = "s3cr3t-value"
	for _, c := range []struct {
		method, pattern, path string
		wantAction, wantTgt   string
	}{
		{"PUT", "/api/connections/git/{host}", "/api/connections/git/github.com", "connection.git.save", "github.com"},
		{"PUT", "/api/connections/git/{host}", "/api/connections/git/ghe.example.com:8443", "connection.git.save", "ghe.example.com:8443"},
		{"DELETE", "/api/connections/git/{host}", "/api/connections/git/bitbucket.org", "connection.git.delete", "bitbucket.org"},
		// A host that carries userinfo or a query is not a host: empty target, row still written.
		{"PUT", "/api/connections/git/{host}", "/api/connections/git/user:" + secret + "@evil.example", "connection.git.save", ""},
		{"PUT", "/api/connections/git/{host}", "/api/connections/git/h%3Ftoken=" + secret, "connection.git.save", ""},
		{"PUT", "/api/connections/aws", "/api/connections/aws", "connection.aws.save", ""},
		{"DELETE", "/api/connections/slack", "/api/connections/slack", "connection.slack.delete", ""},
		{"PUT", "/api/connections/opencode", "/api/connections/opencode", "connection.opencode.save", ""},
		{"DELETE", "/api/connections/opencode/{env}", "/api/connections/opencode/ANTHROPIC_API_KEY", "connection.opencode.delete", "ANTHROPIC_API_KEY"},
		{"DELETE", "/api/connections/opencode/{env}", "/api/connections/opencode/" + secret, "connection.opencode.delete", ""},
		{"DELETE", "/api/connections/opencode/oauth", "/api/connections/opencode/oauth", "connection.opencode.oauth_delete", ""},
		{"POST", "/api/connections/codex/api-key", "/api/connections/codex/api-key", "connection.codex.api_key", ""},
		{"POST", "/api/connections/muse/api-key", "/api/connections/muse/api-key", "connection.muse.api_key", ""},
		{"DELETE", "/api/connections/claude", "/api/connections/claude", "connection.claude.delete", ""},
		{"POST", "/api/mcp-servers", "/api/mcp-servers", "mcp.server.create", ""},
		{"PUT", "/api/mcp-servers/{id}", "/api/mcp-servers/github-1", "mcp.server.update", "github-1"},
		{"DELETE", "/api/mcp-servers/{id}", "/api/mcp-servers/github-1", "mcp.server.delete", "github-1"},
		{"PUT", "/api/mcp-servers/{id}/secrets", "/api/mcp-servers/github-1/secrets", "mcp.server.secrets", "github-1"},
		{"PUT", "/api/mcp-servers/{id}/oauth/client", "/api/mcp-servers/github-1/oauth/client", "mcp.server.oauth_client", "github-1"},
		{"PUT", "/api/mcp-servers/{id}", "/api/mcp-servers/https:%2F%2Fu:" + secret + "@h%2Fmcp", "mcp.server.update", ""},
		{"POST", "/api/sessions/{name}/spend-cap", "/api/sessions/s1/spend-cap", "session.spend_cap", "s1"},
		{"POST", "/api/sessions/{name}/lock", "/api/sessions/s1/lock", "session.lock", "s1"},
		{"POST", "/api/repos/{name}/lock", "/api/repos/app/lock", "repo.lock", "app"},
	} {
		mux := http.NewServeMux()
		var action, target string
		var ok bool
		mux.HandleFunc(c.method+" "+c.pattern, func(_ http.ResponseWriter, r *http.Request) {
			action, target, ok = auditActionTarget(r)
		})
		// The secret is in the body too: the target never reads it.
		r := httptest.NewRequest(c.method, c.path, strings.NewReader(`{"token":"`+secret+`","key":"`+secret+`"}`))
		mux.ServeHTTP(httptest.NewRecorder(), r)
		if !ok || action != c.wantAction || target != c.wantTgt {
			t.Errorf("%s %s: got (%q,%q,%v) want (%q,%q,true)", c.method, c.path, action, target, ok, c.wantAction, c.wantTgt)
		}
		if strings.Contains(action+target, secret) {
			t.Errorf("%s %s: the secret reached the audit row: %q %q", c.method, c.path, action, target)
		}
	}
}
