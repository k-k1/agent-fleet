package main

import (
	"net/http/httptest"
	"testing"
)

// Pins the CP-side relay registration for the mirror's skill picker (ADR0034). The CP
// routes by an explicit allowlist, so a route added only on the Agent answers 404 from the
// Console — a gap that keeps recurring. The response shape itself is covered by the
// Agent's session_skills_test.go.
func TestSessionSkillsRouteProxiedByCP(t *testing.T) {
	_, mux := smokeEnv(t)
	req := httptest.NewRequest("GET", "/api/sessions/x/skills", nil)
	if _, pattern := mux.Handler(req); pattern != "GET /api/sessions/{name}/skills" {
		t.Errorf("resolved to %q, want GET /api/sessions/{name}/skills", pattern)
	}
}
