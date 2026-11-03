package mcpsrv

import (
	"strings"
	"testing"
)

// create_session's route arguments are checked against the route snapshot before the Workspace
// is touched; no arguments is the unchanged case and adds no field to the body.
func TestCreateRouteArg(t *testing.T) {
	if got, err := createRouteArg("claude", "", ""); got != nil || err != nil {
		t.Errorf("no route: %v %v, want nil, nil", got, err)
	}
	if got, err := createRouteArg("claude", "  ", " "); got != nil || err != nil {
		t.Errorf("blank route: %v %v, want nil, nil", got, err)
	}
	for name, a := range map[string][3]string{
		"known route, kind declares none": {"codex", "openai-api", ""},
		"not on claude's list":            {"claude", "bedrock", ""},
		"empty kind is claude":            {"", "bedrock", "c1"},
		"unknown route":                   {"claude", "nope", ""},
		"connection without route":        {"claude", "", "c1"},
	} {
		if got, err := createRouteArg(a[0], a[1], a[2]); err == nil || got != nil {
			t.Errorf("%s: %v %v, want a refusal", name, got, err)
		}
	}
}

// Through the tool itself: a selection is refused before the Agent is called, and a call without
// one reaches it with no route key.
func TestCreateSessionRouteThroughTheTool(t *testing.T) {
	cp := newMemCP(t, memStore(t), "running")
	for name, args := range map[string]map[string]any{
		"known route":      {"initial_prompt": "x", "route": "bedrock"},
		"with connection":  {"initial_prompt": "x", "route": "bedrock", "route_connection": "c1"},
		"unknown route":    {"initial_prompt": "x", "route": "nope"},
		"connection alone": {"initial_prompt": "x", "route_connection": "c1"},
		// The REST body's shape used by mistake, and other wrong types: refused, never dropped.
		"object route": {"initial_prompt": "x", "route": map[string]any{"route": "anthropic-api"}},
		"number route": {"initial_prompt": "x", "route": 1},
		"bool route":   {"initial_prompt": "x", "route": true},
		"array route":  {"initial_prompt": "x", "route": []any{"anthropic-api"}},
		"object conn":  {"initial_prompt": "x", "route": "anthropic-api", "route_connection": map[string]any{}},
		"number conn":  {"initial_prompt": "x", "route_connection": 7},
	} {
		text, isErr := memCall(t, cp, scopeWrite, "create_session", args)
		if !isErr || !strings.Contains(text, "route") || cp.calls != 0 {
			t.Errorf("%s: %q isErr=%v calls=%d, want a route refusal before the Agent", name, text, isErr, cp.calls)
		}
	}
	if _, isErr := memCall(t, cp, scopeWrite, "create_session", map[string]any{"initial_prompt": "x", "route": nil}); isErr {
		t.Fatal("a create without a route was refused")
	}
	if cp.calls != 1 || strings.Contains(cp.gotBody, `"route`) {
		t.Errorf("calls=%d body=%s, want one create with no route key", cp.calls, cp.gotBody)
	}
}
