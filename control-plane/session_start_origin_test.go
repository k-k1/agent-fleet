package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The Console's Start (and an older Control Plane's relay of it) is a person's start: the relay
// passes it to the Agent as it came, with no origin parameter of its own, so a retired session is
// started by it and its mark taken off (#4249). Only the scheduler's start says it is automatic.
func TestConsoleStartRelaysAsADeliberateStart(t *testing.T) {
	var path, query string
	proxy, res, _, cleanup := newFSProxyTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, query = r.URL.Path, r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer cleanup()

	rec := httptest.NewRecorder()
	proxy.rest(rec, httptest.NewRequest(http.MethodPost, "/api/sessions/s1/start", nil), res)
	if rec.Code != http.StatusOK || path != "/sessions/s1/start" || query != "" {
		t.Fatalf("status %d, Agent saw %s?%s, want 200 and /sessions/s1/start with no query", rec.Code, path, query)
	}
}
