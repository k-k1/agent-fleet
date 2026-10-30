package routehealth

import (
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func TestClassify(t *testing.T) {
	now := time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		kind  string
		msg   string
		class Class
		scope Scope
	}{
		// The kind's own reader: measured texts from agents/claude and agents/muse.
		{"claude window", session.KindClaude, "claude returned an error: You've hit your session limit · resets 7:50pm (Asia/Tokyo)", ClassWindow, ScopeKind},
		{"claude spend", session.KindClaude, "You've hit your org's monthly spend limit · run /usage-credits to raise it", ClassSpend, ScopeKind},
		{"claude temporary 429", session.KindClaude, "API Error: 429 temporarily limiting requests (not your usage limit)", ClassRateLimit, ScopeModel},
		{"muse quota", session.KindMuse, "API error 429 [request_id=8af4fc75]: Subscription quota exhausted. Your usage window resets at 2026-10-06T04:25:21Z. (rate_limit_error)", ClassWindow, ScopeKind},
		// Generic markers.
		{"not-a-limit negation elsewhere", session.KindCodex, "429: slow down (not your usage limit)", ClassRateLimit, ScopeModel},
		{"unknown model", session.KindCodex, "error: model_not_found: the requested model does not exist", ClassConfig, ""},
		{"404 is config", session.KindOpencode, "HTTP 404 from provider", ClassConfig, ""},
		{"auth", session.KindCodex, "401 Unauthorized", ClassAuth, ""},
		{"codex usage limit", session.KindCodex, "You've hit your usage limit. Try again later.", ClassWindow, ScopeKind},
		{"rate limit", session.KindCursor, "Too Many Requests", ClassRateLimit, ScopeModel},
		{"bare 429", session.KindAgy, "request failed with status 429", ClassRateLimit, ScopeModel},
		{"overloaded", session.KindClaude, "API Error: 529 Overloaded", ClassServer, ScopeModel},
		{"bare 503", session.KindOpencode, "upstream returned 503", ClassServer, ScopeModel},
		{"status inside an id is no status", session.KindCodex, "failed: request id ab4049f2", ClassOther, ""},
		{"unexplained", session.KindCodex, "exit status 1", ClassOther, ""},
		// Post-merge review of #2784.
		{"claude per-model limit", session.KindClaude, "You've reached your Fable 5 limit. Run /usage-credits to continue or switch models with /model.", ClassWindow, ScopeModel},
		{"a number that is no status", session.KindCodex, "max_tokens must be greater than 512", ClassOther, ""},
		{"a status inside an id", session.KindCodex, "request_id=req-401-abc: HTTP 503", ClassServer, ScopeModel},
		{"status code in JSON", session.KindOpencode, `{"statusCode":429,"message":"slow down"}`, ClassRateLimit, ScopeModel},
		{"leading status", session.KindAgy, "502: upstream hiccup", ClassServer, ScopeModel},
		{"per-minute quota", session.KindCodex, "429: Quota exceeded for requests per minute", ClassRateLimit, ScopeModel},
		{"disk quota", session.KindMuse, "disk quota exceeded", ClassOther, ""},
		{"subscription quota (not muse)", session.KindCodex, "Subscription quota exhausted.", ClassWindow, ScopeKind},
		{"billing quota", session.KindOpencode, "You exceeded your current quota, please check your plan and billing details.", ClassSpend, ScopeKind},
		{"billing quota with a 429", session.KindOpencode, "API error 429: You exceeded your current quota, please check your plan and billing details.", ClassSpend, ScopeKind},
		{"insufficient balance", session.KindOpencode, "Insufficient balance. Manage your billing here.", ClassSpend, ScopeKind},
	}
	for _, c := range cases {
		v := Classify(c.kind, c.msg, now)
		if v.Class != c.class || v.Scope != c.scope {
			t.Errorf("%s: got (%s, %s), want (%s, %s)", c.name, v.Class, v.Scope, c.class, c.scope)
		}
	}
}

// TestOnlyAvailabilityClassesCool keeps config, auth and unknown failures out of the table.
func TestOnlyAvailabilityClassesCool(t *testing.T) {
	want := map[Class]bool{ClassWindow: true, ClassSpend: true, ClassRateLimit: true, ClassServer: true,
		ClassConfig: false, ClassAuth: false, ClassOther: false}
	for c, cools := range want {
		if got := (Verdict{Class: c}).Cools(); got != cools {
			t.Errorf("%s: Cools() = %v, want %v", c, got, cools)
		}
	}
}
