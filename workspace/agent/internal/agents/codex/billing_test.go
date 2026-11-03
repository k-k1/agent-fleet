package codex

import (
	"fmt"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

func billingRollout(rateLimits string) [][]byte {
	return [][]byte{
		[]byte(`{"timestamp":"2026-07-04T03:10:00Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}}`),
		[]byte(`{"timestamp":"2026-07-04T03:10:01Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}}`),
		[]byte(fmt.Sprintf(`{"timestamp":"2026-07-04T03:10:02Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":5}}%s}}`, rateLimits)),
	}
}

// A call is "included" only on the evidence of its own token_count event: a ChatGPT plan and no
// exhausted window. Everything else is unresolved, never metered.
func TestCallBillingFromRateLimits(t *testing.T) {
	win := func(pct string) string {
		return `{"used_percent":` + pct + `,"window_minutes":300,"resets_at":1783142674}`
	}
	cases := map[string]struct {
		rateLimits string
		billing    string
	}{
		"plus within allowance": {`,"rate_limits":{"primary":` + win("3.0") + `,"secondary":null,"plan_type":"plus","rate_limit_reached_type":null}`, usagex.BillingSubscriptionIncluded},
		"window exhausted":      {`,"rate_limits":{"primary":` + win("100.0") + `,"plan_type":"plus"}`, ""},
		"limit reached":         {`,"rate_limits":{"primary":` + win("40") + `,"plan_type":"plus","rate_limit_reached_type":"primary"}`, ""},
		"no rate_limits":        {``, ""},
		"null rate_limits":      {`,"rate_limits":null`, ""},
		"no plan":               {`,"rate_limits":{"primary":` + win("3") + `}`, ""},
		"no window":             {`,"rate_limits":{"plan_type":"plus"}`, ""},
		"usage based plan":      {`,"rate_limits":{"primary":` + win("3") + `,"plan_type":"self_serve_business_usage_based"}`, ""},
	}
	for name, c := range cases {
		turns, _ := parseRollout(billingRollout(c.rateLimits))
		var got *int
		for i := range turns {
			if turns[i].Role == "assistant" {
				got = &i
			}
		}
		if got == nil {
			t.Fatalf("%s: no assistant turn in %+v", name, turns)
		}
		tn := turns[*got]
		if tn.Billing != c.billing {
			t.Errorf("%s: Billing = %q, want %q", name, tn.Billing, c.billing)
		}
		wantRoute := ""
		if c.billing != "" {
			wantRoute = usagex.RouteChatGPT
		}
		if tn.Route != wantRoute {
			t.Errorf("%s: Route = %q, want %q", name, tn.Route, wantRoute)
		}
	}
}
