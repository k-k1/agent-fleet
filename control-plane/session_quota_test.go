package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIsUnmeteredKind(t *testing.T) {
	unmetered := []string{"shell", "ssm"}
	for _, k := range unmetered {
		if !isUnmeteredKind(k) {
			t.Errorf("isUnmeteredKind(%q) = false, want true", k)
		}
	}
	for _, k := range []string{"claude", "codex", "opencode", "copilot", "cursor", "agy", ""} {
		if isUnmeteredKind(k) {
			t.Errorf("isUnmeteredKind(%q) = true, want false", k)
		}
	}
}

// countSessions must count only alive sessions and must exclude the unmetered
// terminal kinds (shell/ssm) — those neither occupy nor are blocked by a quota slot.
func TestCountSessionsExcludesUnmeteredKinds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sessions":[
			{"alive":true,"kind":"claude"},
			{"alive":true,"kind":"codex"},
			{"alive":true,"kind":"shell"},
			{"alive":true,"kind":"ssm"},
			{"alive":false,"kind":"claude"}
		]}`))
	}))
	defer srv.Close()

	m := &manager{}
	n, err := m.countSessions(context.Background(), stubRuntime{endpoint: srv.URL})
	if err != nil {
		t.Fatalf("countSessions: %v", err)
	}
	if n != 2 { // claude + codex; shell/ssm excluded, dead claude excluded
		t.Errorf("countSessions = %d, want 2", n)
	}
}

func TestPeekSessionKind(t *testing.T) {
	cases := []struct {
		body string
		want string
	}{
		{`{"name":"x","kind":"shell"}`, "shell"},
		{`{"kind":"ssm","ssm_host_id":"h1"}`, "ssm"},
		{`{"name":"x","kind":"claude"}`, "claude"},
		{`{"name":"x"}`, ""}, // no kind field
		{`not json`, ""},     // parse failure → safe default
	}
	for _, c := range cases {
		r := httptest.NewRequest("POST", "/api/sessions", strings.NewReader(c.body))
		if got := peekSessionKind(r); got != c.want {
			t.Errorf("peekSessionKind(%q) = %q, want %q", c.body, got, c.want)
		}
		// the body must be restored so the proxy can forward it unchanged
		rest, _ := readAllBody(r)
		if string(rest) != c.body {
			t.Errorf("peekSessionKind(%q) left body = %q, want it restored", c.body, string(rest))
		}
	}
}
