package opencode

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// The route of a call is the provider id opencode wrote on that call's message — not the model
// id and not the session's current model, which can change mid-session.
func TestParseMessageRecordsRouteFromProvider(t *testing.T) {
	cases := []struct {
		provider, want string
	}{
		{"opencode", usagex.RouteOpencodeZen},
		{"opencode-go", usagex.RouteOpencodeGo},
		{"anthropic", ""}, // a key or a login: not provable from the provider id
		{"", ""},
	}
	for _, c := range cases {
		data := []byte(`{"role":"assistant","modelID":"glm-5.2","providerID":"` + c.provider +
			`","tokens":{"input":10,"output":2,"cache":{"read":0,"write":0}},"time":{"created":2000,"completed":3000}}`)
		parts := [][]byte{[]byte(`{"type":"text","text":"hi"}`)}
		turn, ok := parseMessage("m1", data, parts, 0, nil)
		if !ok {
			t.Fatalf("provider %q: message dropped", c.provider)
		}
		if turn.Route != c.want {
			t.Errorf("provider %q: Route = %q, want %q", c.provider, turn.Route, c.want)
		}
		if turn.Billing != "" {
			t.Errorf("provider %q: Billing = %q, want unresolved (opencode billing is #3285)", c.provider, turn.Billing)
		}
	}
}
