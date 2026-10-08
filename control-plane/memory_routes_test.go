package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestMemoryRoutesProxiedByCP pins the proxy registrations for agent memory versioning
// (docs/log/39 P1/P2). The CP proxies by explicit allowlist, so a route added on the
// Agent side only is a 404 from the Console — a gap that has recurred. Only presence in
// the CP's route table is checked here; what the proxied endpoint answers is the Agent
// side's tests.
func TestMemoryRoutesProxiedByCP(t *testing.T) {
	_, mux := smokeEnv(t)
	for _, c := range []struct{ method, path, want string }{
		{"GET", "/api/agents/memory/roots", "GET /api/agents/memory/roots"},
		{"GET", "/api/agents/memory/snapshots", "GET /api/agents/memory/snapshots"},
		{"POST", "/api/agents/memory/snapshots", "POST /api/agents/memory/snapshots"},
		{"GET", "/api/agents/memory/diff", "GET /api/agents/memory/diff"},
		{"GET", "/api/agents/memory/tree", "GET /api/agents/memory/tree"},
		{"POST", "/api/agents/memory/restore", "POST /api/agents/memory/restore"},
		{"PUT", "/api/agents/memory/settings", "PUT /api/agents/memory/settings"},
		{"GET", "/api/agents/memory/export", "GET /api/agents/memory/export"},
		{"POST", "/api/agents/memory/import", "POST /api/agents/memory/import"},
		{"POST", "/api/agents/memory/import/apply", "POST /api/agents/memory/import/apply"},
		{"GET", "/api/agents/memory/entries/changes", "GET /api/agents/memory/entries/changes"},
		{"GET", "/api/agents/memory/entries/diff", "GET /api/agents/memory/entries/diff"},
		{"POST", "/api/agents/memory/entries/revert", "POST /api/agents/memory/entries/revert"},
		{"GET", "/api/agents/memory/entries/list", "GET /api/agents/memory/entries/list"},
		{"POST", "/api/agents/memory/entries/pin", "POST /api/agents/memory/entries/pin"},
		{"GET", "/api/agents/memory/claude-import", "GET /api/agents/memory/claude-import"},
		{"GET", "/api/agents/memory/claude-import/preview", "GET /api/agents/memory/claude-import/preview"},
		{"POST", "/api/agents/memory/claude-import", "POST /api/agents/memory/claude-import"},
		// Must not be swallowed by the existing pattern route /api/agents/{kind}/models.
		{"GET", "/api/agents/codex/models", "GET /api/agents/{kind}/models"},
	} {
		req := httptest.NewRequest(c.method, c.path, nil)
		if _, pattern := mux.Handler(req); pattern != c.want {
			t.Errorf("%s %s resolved to %q, want %q", c.method, c.path, pattern, c.want)
		}
	}
}

// TestMemorySnapshotIsAudited: manual snapshot / restore / import mutate, so they are
// audited. export only reads, but it is the one path that takes personal memory out of
// the environment, so docs/log/39 ★4 requires it audited as an exception. target comes
// from the URL only; the body is never read.
func TestMemorySnapshotIsAudited(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/agents/memory/snapshots", nil)
	action, _, ok := auditActionTarget(req)
	if !ok || action != "memory.snapshot" {
		t.Fatalf("auditActionTarget = (%q, ok=%v), want memory.snapshot", action, ok)
	}
	// restore takes the source rev for target from the query hint, not the body.
	req = httptest.NewRequest(http.MethodPost, "/api/agents/memory/restore?rev=deadbeef", nil)
	action, target, ok := auditActionTarget(req)
	if !ok || action != "memory.restore" || target != "deadbeef" {
		t.Fatalf("auditActionTarget = (%q, %q, ok=%v), want memory.restore/deadbeef", action, target, ok)
	}
	// import records receipt and apply as separate rows: receiving without applying is
	// an ordinary outcome.
	req = httptest.NewRequest(http.MethodPost, "/api/agents/memory/import", nil)
	if action, _, ok := auditActionTarget(req); !ok || action != "memory.import" {
		t.Fatalf("auditActionTarget = (%q, ok=%v), want memory.import", action, ok)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/agents/memory/import/apply?importId=20260727T010203Z", nil)
	action, target, ok = auditActionTarget(req)
	if !ok || action != "memory.import.apply" || target != "20260727T010203Z" {
		t.Fatalf("auditActionTarget = (%q, %q, ok=%v), want memory.import.apply", action, target, ok)
	}
	// export is a GET but takes data out, so it is audited (target is the format alone).
	req = httptest.NewRequest(http.MethodGet, "/api/agents/memory/export?format=bundle", nil)
	action, target, ok = auditActionTarget(req)
	if !ok || action != "memory.export" || target != "bundle" {
		t.Fatalf("auditActionTarget = (%q, %q, ok=%v), want memory.export/bundle", action, target, ok)
	}
	// Reverting an AF memory change is audited, with the commit from the query hint.
	req = httptest.NewRequest(http.MethodPost, "/api/agents/memory/entries/revert?commit=abc1234", nil)
	action, target, ok = auditActionTarget(req)
	if !ok || action != "memory.entry.revert" || target != "abc1234" {
		t.Fatalf("auditActionTarget = (%q, %q, ok=%v), want memory.entry.revert/abc1234", action, target, ok)
	}
	// A hint that is not a commit id is not copied into the ledger; the event is still kept.
	req = httptest.NewRequest(http.MethodPost, "/api/agents/memory/entries/revert?commit=not-a-commit+value", nil)
	action, target, ok = auditActionTarget(req)
	if !ok || action != "memory.entry.revert" || target != "" {
		t.Fatalf("auditActionTarget = (%q, %q, ok=%v), want memory.entry.revert with an empty target", action, target, ok)
	}
	// Pinning is audited with an id-shaped project hint only; the memory name never reaches the ledger.
	req = httptest.NewRequest(http.MethodPost, "/api/agents/memory/entries/pin?project=agent-fleet-0123456789ab", nil)
	action, target, ok = auditActionTarget(req)
	if !ok || action != "memory.entry.pin" || target != "agent-fleet-0123456789ab" {
		t.Fatalf("auditActionTarget = (%q, %q, ok=%v), want memory.entry.pin/agent-fleet-0123456789ab", action, target, ok)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/agents/memory/entries/pin", nil)
	if action, target, ok = auditActionTarget(req); !ok || action != "memory.entry.pin" || target != "" {
		t.Fatalf("auditActionTarget = (%q, %q, ok=%v), want memory.entry.pin with an empty target", action, target, ok)
	}
	// The claude-memory import is audited; only an id-shaped project hint reaches the ledger.
	req = httptest.NewRequest(http.MethodPost, "/api/agents/memory/claude-import?project=agent-fleet-0123456789ab", nil)
	action, target, ok = auditActionTarget(req)
	if !ok || action != "memory.claude_import" || target != "agent-fleet-0123456789ab" {
		t.Fatalf("auditActionTarget = (%q, %q, ok=%v), want memory.claude_import/agent-fleet-0123456789ab", action, target, ok)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/agents/memory/claude-import?project=%2Fhome%2Fdev%2Frepos", nil)
	action, target, ok = auditActionTarget(req)
	if !ok || action != "memory.claude_import" || target != "" {
		t.Fatalf("auditActionTarget = (%q, %q, ok=%v), want memory.claude_import with an empty target", action, target, ok)
	}
	// The other read-only routes are not audited.
	for _, p := range []string{"/api/agents/memory/snapshots", "/api/agents/memory/tree", "/api/agents/memory/diff", "/api/agents/memory/claude-import", "/api/agents/memory/claude-import/preview"} {
		if _, _, ok := auditActionTarget(httptest.NewRequest(http.MethodGet, p, nil)); ok {
			t.Errorf("GET %s should not be audited", p)
		}
	}
}
