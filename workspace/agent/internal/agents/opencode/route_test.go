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

func routeMsg(provider string, withText bool, cost float64) v2Msg {
	m := v2Msg{Type: "assistant", Model: v2ModelRef{ID: "glm-5.2", ProviderID: provider}, Cost: cost}
	m.Time.Created = 2000
	m.Time.Completed = 3000
	m.Tokens.Input, m.Tokens.Output = 10, 2
	if withText {
		m.Content = []v2Content{{Type: "text", Text: "hi"}}
	}
	return m
}

// A run of assistant messages is one turn; its route is the one every message shares. The last
// message's provider alone would file an earlier step's cost under a route that did not serve it.
func TestV2SessionTurnsRunRoute(t *testing.T) {
	user := v2Msg{Type: "user"}
	user.Time.Created = 1000
	cases := map[string]struct {
		providers []string
		want      string
	}{
		"same provider":    {[]string{"opencode-go", "opencode-go"}, usagex.RouteOpencodeGo},
		"provider switch":  {[]string{"opencode", "opencode-go"}, ""},
		"empty then known": {[]string{"", "opencode-go"}, ""},
		"known then empty": {[]string{"opencode-go", ""}, ""},
	}
	for name, c := range cases {
		for _, costOnly := range []bool{false, true} {
			msgs := []v2Msg{user}
			for _, p := range c.providers {
				msgs = append(msgs, routeMsg(p, !costOnly, 0.25))
			}
			turns := v2SessionTurns(msgs, "/w", 0, false, true, nil)
			var got []string
			for _, tn := range turns {
				if tn.turn.Role == "assistant" {
					if tn.turn.CostOnly != costOnly {
						t.Fatalf("%s: CostOnly = %v, want %v", name, tn.turn.CostOnly, costOnly)
					}
					got = append(got, tn.turn.Route)
				}
			}
			if len(got) != 1 || got[0] != c.want {
				t.Errorf("%s (costOnly=%v): routes = %q, want one %q", name, costOnly, got, c.want)
			}
		}
	}
}

// A Zen call that carries a cost was charged to the balance; a zero cost (a free model or an
// empty step), the Go route and a direct provider prove nothing.
func TestBillingOfOpencodeCall(t *testing.T) {
	for _, c := range []struct {
		route string
		cost  float64
		want  string
	}{
		{usagex.RouteOpencodeZen, 0.02, usagex.BillingMetered},
		{usagex.RouteOpencodeZen, 0, ""},
		{usagex.RouteOpencodeGo, 0.02, ""},
		{"", 0.02, ""},
	} {
		if got := usagex.BillingOfOpencodeCall(c.route, c.cost); got != c.want {
			t.Errorf("(%q, %v) = %q, want %q", c.route, c.cost, got, c.want)
		}
	}
}

func TestParseMessageRecordsBillingFromRouteAndCost(t *testing.T) {
	for _, c := range []struct {
		provider string
		cost     string
		want     string
	}{{"opencode", "0.03", usagex.BillingMetered}, {"opencode", "0", ""}, {"opencode-go", "0.03", ""}} {
		data := []byte(`{"role":"assistant","modelID":"glm-5.2","providerID":"` + c.provider + `","cost":` + c.cost +
			`,"tokens":{"input":10,"output":2,"cache":{"read":0,"write":0}},"time":{"created":2000,"completed":3000}}`)
		turn, ok := parseMessage("m1", data, [][]byte{[]byte(`{"type":"text","text":"hi"}`)}, 0, nil)
		if !ok || turn.Billing != c.want {
			t.Errorf("%s cost %s: ok=%v Billing=%q, want %q", c.provider, c.cost, ok, turn.Billing, c.want)
		}
	}
}

// A run is metered only when every message in it is; one unpriced step hides inside a summed cost.
func TestV2SessionTurnsRunBilling(t *testing.T) {
	user := v2Msg{Type: "user"}
	user.Time.Created = 1000
	for name, c := range map[string]struct {
		costs []float64
		want  string
	}{
		"all priced":     {[]float64{0.1, 0.2}, usagex.BillingMetered},
		"one free step":  {[]float64{0.1, 0}, ""},
		"free then paid": {[]float64{0, 0.1}, ""},
	} {
		for _, text := range []bool{true, false} {
			msgs := []v2Msg{user}
			for _, cost := range c.costs {
				msgs = append(msgs, routeMsg("opencode", text, cost))
			}
			var got []string
			for _, tn := range v2SessionTurns(msgs, "/w", 0, false, true, nil) {
				if tn.turn.Role == "assistant" {
					got = append(got, tn.turn.Billing)
				}
			}
			if len(got) != 1 || got[0] != c.want {
				t.Errorf("%s (text=%v): billing = %q, want one %q", name, text, got, c.want)
			}
		}
	}
}
