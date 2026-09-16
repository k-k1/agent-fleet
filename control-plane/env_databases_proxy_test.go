package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// TestEnvDatabasesProxyForwardsPath verifies that proxy.rest strips "/api" and passes the
// remainder of the URL — including the {engine} and {action} wildcard segments — unchanged
// to the Agent. A POST to /api/env/databases/mysql/start must arrive at the Agent as
// /env/databases/mysql/start; a GET to /api/env/databases must arrive as /env/databases.
// This guards decision 9 of ADR 0086 P1: the CP is a transparent relay here, never a
// transformer.
func TestEnvDatabasesProxyForwardsPath(t *testing.T) {
	var agentPaths []string
	var calls atomic.Int32
	proxy, res, _, cleanup := newFSProxyTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		agentPaths = append(agentPaths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"engines":[]}`))
	}))
	defer cleanup()

	// GET /api/env/databases → Agent sees /env/databases
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/env/databases", nil)
	proxy.rest(rec, req, res)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/env/databases: status %d, body %s", rec.Code, rec.Body.String())
	}

	// POST /api/env/databases/mysql/start → Agent sees /env/databases/mysql/start
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/api/env/databases/mysql/start", strings.NewReader("{}"))
	proxy.rest(rec2, req2, res)
	if rec2.Code != http.StatusOK {
		t.Fatalf("POST /api/env/databases/mysql/start: status %d, body %s", rec2.Code, rec2.Body.String())
	}

	// POST /api/env/databases/postgres/stop?purge=1 → Agent sees /env/databases/postgres/stop?purge=1
	rec3 := httptest.NewRecorder()
	req3 := httptest.NewRequest(http.MethodPost, "/api/env/databases/postgres/stop?purge=1", strings.NewReader("{}"))
	proxy.rest(rec3, req3, res)
	if rec3.Code != http.StatusOK {
		t.Fatalf("POST /api/env/databases/postgres/stop?purge=1: status %d, body %s", rec3.Code, rec3.Body.String())
	}

	if calls.Load() != 3 {
		t.Fatalf("expected 3 agent calls, got %d", calls.Load())
	}
	if agentPaths[0] != "/env/databases" {
		t.Errorf("GET path: got %q, want /env/databases", agentPaths[0])
	}
	if agentPaths[1] != "/env/databases/mysql/start" {
		t.Errorf("POST start path: got %q, want /env/databases/mysql/start", agentPaths[1])
	}
	if agentPaths[2] != "/env/databases/postgres/stop" {
		t.Errorf("POST stop path: got %q, want /env/databases/postgres/stop", agentPaths[2])
	}
}
