package mcpx

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// create_session in the container advertises route / route_connection, forwards them as the
// Agent's selection object, and never reads a wrong type or a lone connection as "no route".
func TestCreateSessionRoute(t *testing.T) {
	withFleetSpawn(t, true)
	mcpPeerMessagingEnabled = true
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	session.WriteMeta(session.Meta{Name: "parent1", Kind: session.KindClaude, Origin: session.OriginUser})
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		bodies = append(bodies, m)
		_, _ = w.Write([]byte(`{"name":"slot09"}`))
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	t.Setenv("AGENT_ADDR", u.Host)

	for name, args := range map[string]map[string]any{
		"object route":    {"dir": "/r", "initial_prompt": "x", "route": map[string]any{"route": "anthropic-api"}},
		"number route":    {"dir": "/r", "initial_prompt": "x", "route": 1},
		"connection only": {"dir": "/r", "initial_prompt": "x", "route_connection": "ANTHROPIC_API_KEY"},
	} {
		if resp := callTool("create_session", args); !strings.Contains(resp, `"isError":true`) {
			t.Errorf("%s: %s, want an error", name, resp)
		}
	}
	if len(bodies) != 0 {
		t.Fatalf("a refused route reached the Agent: %v", bodies)
	}

	if resp := callTool("create_session", map[string]any{"dir": "/r", "initial_prompt": "x"}); strings.Contains(resp, `"isError":true`) {
		t.Fatalf("no route: %s", resp)
	}
	if _, has := bodies[0]["route"]; has {
		t.Errorf("a create without a route sends one: %v", bodies[0]["route"])
	}
	resp := callTool("create_session", map[string]any{"dir": "/r", "initial_prompt": "x", "route": "anthropic-api", "route_connection": "ANTHROPIC_API_KEY"})
	if strings.Contains(resp, `"isError":true`) || len(bodies) != 2 {
		t.Fatalf("with a route: %s (bodies %d)", resp, len(bodies))
	}
	sel, _ := bodies[1]["route"].(map[string]any)
	if sel["route"] != "anthropic-api" || sel["connection"] != "ANTHROPIC_API_KEY" {
		t.Errorf("selection = %v", bodies[1]["route"])
	}
	// Another route is another child: the idempotency keys differ.
	callTool("create_session", map[string]any{"dir": "/r", "initial_prompt": "x", "route": "anthropic-subscription"})
	if len(bodies) != 3 || bodies[1]["idempotency_key"] == bodies[2]["idempotency_key"] || bodies[0]["idempotency_key"] == bodies[1]["idempotency_key"] {
		t.Errorf("idempotency keys do not tell routes apart")
	}
}
