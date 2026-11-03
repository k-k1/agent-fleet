package codex

import "testing"

// rate_limits on a token_count event is the session's latest snapshot re-sent, not evidence about
// that call, so a rollout with a ChatGPT plan snapshot must still leave route and billing empty.
func TestTokenCountRateLimitsProveNoBilling(t *testing.T) {
	rl := `,"rate_limits":{"primary":{"used_percent":3.0,"window_minutes":300,"resets_at":1783142674},"secondary":null,"plan_type":"plus","rate_limit_reached_type":null}`
	turns, _ := parseRollout([][]byte{
		[]byte(`{"timestamp":"2026-07-04T03:10:00Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}}`),
		[]byte(`{"timestamp":"2026-07-04T03:10:01Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}}`),
		[]byte(`{"timestamp":"2026-07-04T03:10:02Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":5}}` + rl + `}}`),
	})
	seen := false
	for _, tn := range turns {
		if tn.Role == "assistant" {
			seen = true
			if tn.OutTok != 5 || tn.Route != "" || tn.Billing != "" {
				t.Errorf("assistant turn = out %d route %q billing %q, want 5 and unresolved", tn.OutTok, tn.Route, tn.Billing)
			}
		}
	}
	if !seen {
		t.Fatal("no assistant turn")
	}
}
