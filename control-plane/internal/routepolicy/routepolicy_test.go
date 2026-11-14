package routepolicy

import (
	"slices"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/agentroutes"
)

func must(t *testing.T, s string) Rule {
	t.Helper()
	r, err := ParseCeiling(s)
	if err != nil {
		t.Fatalf("ParseCeiling(%q): %v", s, err)
	}
	return r
}

// With no rule at any layer every route is allowed, the catalogued ones and any other: today's
// behaviour (D8).
func TestNoRestrictionAllowsEverything(t *testing.T) {
	var l Layers
	if l.Restricted() {
		t.Fatal("empty layers restrict")
	}
	for _, r := range agentroutes.Routes() {
		if !l.Allows(r.ID) {
			t.Errorf("%s refused with no rule", r.ID)
		}
	}
	if !l.Allows("a-route-from-the-future") {
		t.Error("an uncatalogued route is refused with no rule at all")
	}
	if r := must(t, ""); r.Restricted() {
		t.Errorf("an empty ceiling restricts: %+v", r)
	}
}

// The "No subscriptions" preset of §6: classes minus subscription and destinations minus
// opencode.ai.
func TestNoSubscriptionsPreset(t *testing.T) {
	r := must(t, "classes=-subscription;destinations=-opencode.ai")
	for id, want := range map[string]bool{
		"anthropic-subscription": false, "chatgpt-subscription": false, "muse-login": false, "agy-login": false,
		"copilot-login": false, "cursor-login": false, "kiro-login": false, "opencode-account": false, "opencode-go": false,
		"opencode-zen": false, // a metered key, but its destination is opencode.ai
		"anthropic-api": true, "openai-api": true, "meta-api": true, "bedrock": true, "bedrock-deployment": true,
		"af-engine-llm": true, "member-llamacpp": true, // unclassified, but destinations are restricted only by removal here
	} {
		if got := r.Allows(id); got != want {
			t.Errorf("%s: allowed %v, want %v", id, got, want)
		}
	}
	if !r.Allows("anthropic-api") || r.Allows("route-nobody-catalogued") {
		t.Error("an uncatalogued route must be refused under a restriction")
	}
}

// D1d: an exception reopens only what its own layer closed; deny wins inside a layer; across
// layers the sets intersect.
func TestExceptionsDoNotWiden(t *testing.T) {
	noSubs := must(t, "classes=-subscription")
	withCopilot := must(t, "classes=-subscription;except=copilot-login")
	if !withCopilot.Allows("copilot-login") || withCopilot.Allows("cursor-login") {
		t.Error("the exception preset: copilot allowed, cursor not")
	}
	l := Layers{TenantCap: noSubs, Tenant: withCopilot}
	if l.Allows("copilot-login") || !slices.Equal(l.ForbiddenBy("copilot-login"), []string{LayerTenantCap}) {
		t.Errorf("a tenant exception under a cap without it: allowed %v, forbidden by %v", l.Allows("copilot-login"), l.ForbiddenBy("copilot-login"))
	}
	l = Layers{Deployment: Rule{Deny: []string{"copilot-login"}}, Tenant: withCopilot}
	if l.Allows("copilot-login") {
		t.Error("a tenant exception against an upper explicit deny was allowed")
	}
	both := Rule{Classes: &[]string{"subscription"}, Except: []string{"copilot-login"}, Deny: []string{"copilot-login"}}
	if both.Allows("copilot-login") {
		t.Error("a route in both lists of one layer must be denied")
	}
	l = Layers{Ceiling: withCopilot, Deployment: withCopilot}
	if !l.Allows("copilot-login") {
		t.Error("the same exception at every layer must allow it")
	}
}

// Refused at save, and at CP start for the ceiling: an unknown route id in either list, a
// per-connection route in except, unknown vocabulary, unparsable spelling.
func TestValidationRefuses(t *testing.T) {
	for _, bad := range []string{
		"except=no-such-route", "deny=no-such-route", "except=member-llamacpp", "except=custom-api",
		"classes=subscriptions", "destinations=mars", "classes=-subscription,member-key", "classes",
		"classes=member-key;classes=provided", "colour=blue", "destinations=-nowhere",
	} {
		if _, err := ParseCeiling(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	if _, err := ParseCeiling("deny=member-llamacpp"); err != nil {
		t.Errorf("denying a per-connection route must be allowed: %v", err)
	}
	r := must(t, " destinations = aws-bedrock ; except = anthropic-api ")
	if r.Destinations == nil || !slices.Equal(*r.Destinations, []string{"aws-bedrock"}) || !slices.Equal(r.Except, []string{"anthropic-api"}) {
		t.Errorf("parsed %+v", r)
	}
	if !r.Allows("bedrock") || !r.Allows("anthropic-api") || r.Allows("openai-api") {
		t.Error("Bedrock only with an Anthropic exception")
	}
}

// A super_admin's rule may not allow what the ceiling forbids (§8 M1: with the ceiling at
// classes=provided, a call to widen it is refused).
func TestExceedsTheCeiling(t *testing.T) {
	ceiling := must(t, "classes=provided")
	if got := (Rule{}).Exceeds(ceiling); len(got) == 0 || !slices.Contains(got, "anthropic-api") {
		t.Errorf("no rule under classes=provided exceeds by %v", got)
	}
	narrower := Rule{Classes: &[]string{"provided"}, Deny: []string{"bedrock-tenant"}}
	if got := narrower.Exceeds(ceiling); len(got) != 0 {
		t.Errorf("a narrower rule exceeds by %v", got)
	}
	if got := (Rule{Classes: &[]string{"provided"}, Except: []string{"anthropic-api"}}).Exceeds(ceiling); !slices.Equal(got, []string{"anthropic-api"}) {
		t.Errorf("an exception the ceiling does not have exceeds by %v", got)
	}
}

func TestNormalize(t *testing.T) {
	a := Rule{Classes: &[]string{"provided", "member-key", "provided"}, Except: []string{"b", "a"}}.Normalize()
	if strings.Join(*a.Classes, ",") != "member-key,provided" || strings.Join(a.Except, ",") != "a,b" {
		t.Errorf("normalised %+v", a)
	}
	none := Rule{Classes: &[]string{}}.Normalize()
	if none.Classes == nil || len(*none.Classes) != 0 || !none.Restricted() || none.Allows("bedrock") {
		t.Errorf("an empty class list must allow nothing: %+v", none)
	}
}
