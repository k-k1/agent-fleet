package main

import (
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
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
		{"GET", "/api/agents/memory-review", "GET /api/agents/memory-review"},
		{"POST", "/api/agents/memory-review/run", "POST /api/agents/memory-review/run"},
		{"POST", "/api/agents/memory-review/proposals/list", "POST /api/agents/memory-review/proposals/list"},
		{"POST", "/api/agents/memory-review/proposals/approve", "POST /api/agents/memory-review/proposals/approve"},
		{"POST", "/api/agents/memory-review/proposals/reject", "POST /api/agents/memory-review/proposals/reject"},
		{"POST", "/api/agents/memory-review/proposals/reject-all", "POST /api/agents/memory-review/proposals/reject-all"},
		{"POST", "/api/agents/memory-review/runs/revert", "POST /api/agents/memory-review/runs/revert"},
		{"POST", "/api/agents/memory/entries/member-save", "POST /api/agents/memory/entries/member-save"},
		{"GET", "/api/agents/memory/entries/purge-preview", "GET /api/agents/memory/entries/purge-preview"},
		{"POST", "/api/agents/memory/entries/purge", "POST /api/agents/memory/entries/purge"},
		{"GET", "/api/agents/memory/entries/member-read", "GET /api/agents/memory/entries/member-read"},
		{"GET", "/api/agents/memory/entries/projects", "GET /api/agents/memory/entries/projects"},
		{"GET", "/api/agents/memory/entries/project-move-preview", "GET /api/agents/memory/entries/project-move-preview"},
		{"POST", "/api/agents/memory/entries/project-move", "POST /api/agents/memory/entries/project-move"},
		{"GET", "/api/agents/memory/claude-import", "GET /api/agents/memory/claude-import"},
		{"GET", "/api/agents/memory/claude-import/preview", "GET /api/agents/memory/claude-import/preview"},
		{"POST", "/api/agents/memory/claude-import", "POST /api/agents/memory/claude-import"},
		{"GET", "/api/agents/memory/muse-import", "GET /api/agents/memory/muse-import"},
		{"GET", "/api/agents/memory/muse-import/preview", "GET /api/agents/memory/muse-import/preview"},
		{"POST", "/api/agents/memory/muse-import", "POST /api/agents/memory/muse-import"},
		{"GET", "/api/agents/memory/claude-export", "GET /api/agents/memory/claude-export"},
		{"GET", "/api/agents/memory/claude-export/preview", "GET /api/agents/memory/claude-export/preview"},
		{"POST", "/api/agents/memory/claude-export", "POST /api/agents/memory/claude-export"},
		{"GET", "/api/agents/memory/af-import/preview", "GET /api/agents/memory/af-import/preview"},
		{"POST", "/api/agents/memory/af-import", "POST /api/agents/memory/af-import"},
		{"GET", "/api/agents/memory/auto-sync", "GET /api/agents/memory/auto-sync"},
		{"GET", "/api/agents/memory/auto-sync/preview", "GET /api/agents/memory/auto-sync/preview"},
		{"POST", "/api/agents/memory/auto-sync", "POST /api/agents/memory/auto-sync"},
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
	// "Review now" is audited with a session-name-shaped hint only.
	req = httptest.NewRequest(http.MethodPost, "/api/agents/memory-review/run?session=my_session-1", nil)
	if action, target, ok = auditActionTarget(req); !ok || action != "memory.review.run" || target != "my_session-1" {
		t.Fatalf("auditActionTarget(review run) = (%q, %q, ok=%v)", action, target, ok)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/agents/memory-review/run?session=%2Fhome%2Fdev+secret", nil)
	if action, target, ok = auditActionTarget(req); !ok || action != "memory.review.run" || target != "" {
		t.Fatalf("auditActionTarget(review run, odd hint) = (%q, %q, ok=%v)", action, target, ok)
	}
	// The member's decisions on the review's queue are audited with an id-shaped hint only (a
	// proposal or run id); anything else, text included, is dropped.
	for _, c := range []struct{ path, action, want string }{
		{"/api/agents/memory-review/proposals/approve?id=0123456789abcdef0123", "memory.review.approve", "0123456789abcdef0123"},
		{"/api/agents/memory-review/proposals/approve?id=my+secret+text", "memory.review.approve", ""},
		{"/api/agents/memory-review/proposals/reject?id=0123456789abcdef0123", "memory.review.reject", "0123456789abcdef0123"},
		{"/api/agents/memory-review/proposals/reject?id=0123456789ABCDEF0123", "memory.review.reject", ""},
		{"/api/agents/memory-review/proposals/reject-all?run=0123456789abcdef0123", "memory.review.reject_all", "0123456789abcdef0123"},
		{"/api/agents/memory-review/proposals/reject-all", "memory.review.reject_all", ""},
		{"/api/agents/memory-review/runs/revert?run=0123456789abcdef0123", "memory.review.revert_run", "0123456789abcdef0123"},
		{"/api/agents/memory-review/runs/revert?run=%2Fhome%2Fdev", "memory.review.revert_run", ""},
	} {
		req = httptest.NewRequest(http.MethodPost, c.path, nil)
		if action, target, ok = auditActionTarget(req); !ok || action != c.action || target != c.want {
			t.Fatalf("auditActionTarget(%s) = (%q, %q, ok=%v), want %s/%q", c.path, action, target, ok, c.action, c.want)
		}
	}
	req = httptest.NewRequest(http.MethodPost, "/api/agents/memory/entries/pin", nil)
	if action, target, ok = auditActionTarget(req); !ok || action != "memory.entry.pin" || target != "" {
		t.Fatalf("auditActionTarget = (%q, %q, ok=%v), want memory.entry.pin with an empty target", action, target, ok)
	}
	// A member save is audited by project id only; the memory name never reaches the ledger.
	req = httptest.NewRequest(http.MethodPost, "/api/agents/memory/entries/member-save?project=agent-fleet-0123456789ab&name=ghp_secretlooking", nil)
	if action, target, ok = auditActionTarget(req); !ok || action != "memory.entry.save" || target != "agent-fleet-0123456789ab" {
		t.Fatalf("auditActionTarget(member-save) = (%q, %q, ok=%v), want memory.entry.save/agent-fleet-0123456789ab", action, target, ok)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/agents/memory/entries/member-save?project=%2Fhome%2Fdev", nil)
	if action, target, ok = auditActionTarget(req); !ok || action != "memory.entry.save" || target != "" {
		t.Fatalf("auditActionTarget(member-save, bad hint) = (%q, %q, ok=%v), want memory.entry.save with an empty target", action, target, ok)
	}
	// A purge of a memory's history is audited by project id only; the memory name never reaches the ledger.
	req = httptest.NewRequest(http.MethodPost, "/api/agents/memory/entries/purge?project=agent-fleet-0123456789ab&name=ghp_secretlooking", nil)
	if action, target, ok = auditActionTarget(req); !ok || action != "memory.entry.purge" || target != "agent-fleet-0123456789ab" {
		t.Fatalf("auditActionTarget(purge) = (%q, %q, ok=%v), want memory.entry.purge/agent-fleet-0123456789ab", action, target, ok)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/agents/memory/entries/purge?project=%2Fhome%2Fdev", nil)
	if action, target, ok = auditActionTarget(req); !ok || action != "memory.entry.purge" || target != "" {
		t.Fatalf("auditActionTarget(purge, bad hint) = (%q, %q, ok=%v), want memory.entry.purge with an empty target", action, target, ok)
	}
	// Moving an orphaned project's memories is audited by the source project id only.
	req = httptest.NewRequest(http.MethodPost, "/api/agents/memory/entries/project-move?project=app-0123456789ab", nil)
	if action, target, ok = auditActionTarget(req); !ok || action != "memory.project.move" || target != "app-0123456789ab" {
		t.Fatalf("auditActionTarget(project-move) = (%q, %q, ok=%v), want memory.project.move/app-0123456789ab", action, target, ok)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/agents/memory/entries/project-move?project=%2Fhome%2Fdev", nil)
	if action, target, ok = auditActionTarget(req); !ok || action != "memory.project.move" || target != "" {
		t.Fatalf("auditActionTarget(project-move, bad hint) = (%q, %q, ok=%v), want an empty target", action, target, ok)
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
	// The muse-memory import is audited the same way.
	req = httptest.NewRequest(http.MethodPost, "/api/agents/memory/muse-import?project=agent-fleet-0123456789ab", nil)
	action, target, ok = auditActionTarget(req)
	if !ok || action != "memory.muse_import" || target != "agent-fleet-0123456789ab" {
		t.Fatalf("auditActionTarget = (%q, %q, ok=%v), want memory.muse_import/agent-fleet-0123456789ab", action, target, ok)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/agents/memory/muse-import?project=%2Fhome%2Fdev%2Frepos", nil)
	action, target, ok = auditActionTarget(req)
	if !ok || action != "memory.muse_import" || target != "" {
		t.Fatalf("auditActionTarget = (%q, %q, ok=%v), want memory.muse_import with an empty target", action, target, ok)
	}
	// The write-back into claude's memory is audited the same way.
	req = httptest.NewRequest(http.MethodPost, "/api/agents/memory/claude-export?project=agent-fleet-0123456789ab", nil)
	action, target, ok = auditActionTarget(req)
	if !ok || action != "memory.claude_export" || target != "agent-fleet-0123456789ab" {
		t.Fatalf("auditActionTarget = (%q, %q, ok=%v), want memory.claude_export/agent-fleet-0123456789ab", action, target, ok)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/agents/memory/claude-export?project=%2Fhome%2Fdev%2Frepos", nil)
	action, target, ok = auditActionTarget(req)
	if !ok || action != "memory.claude_export" || target != "" {
		t.Fatalf("auditActionTarget = (%q, %q, ok=%v), want memory.claude_export with an empty target", action, target, ok)
	}
	// Importing AF memory out of a bundle (#1983) is audited by import id only.
	for q, want := range map[string]string{
		"importId=20261014T093000Z": "20261014T093000Z", "importId=20261014T093000Z-2": "20261014T093000Z-2",
		"importId=%2Fhome%2Fdev": "", "importId=ghp_secretlooking": "",
	} {
		req = httptest.NewRequest(http.MethodPost, "/api/agents/memory/af-import?"+q, nil)
		action, target, ok = auditActionTarget(req)
		if !ok || action != "memory.af_import" || target != want {
			t.Fatalf("auditActionTarget(af-import?%s) = (%q, %q, ok=%v), want memory.af_import/%q", q, action, target, ok, want)
		}
	}
	// The member's part in the switch's automatic run (#1972) is audited by action word only.
	for q, want := range map[string]string{
		"action=retry": "retry", "action=abandon": "abandon", "action=wake&to=on": "wake:on",
		"action=wake&to=off": "wake:off", "action=wake&to=%2Fhome": "wake", "action=ghp_secretlooking": "",
	} {
		req = httptest.NewRequest(http.MethodPost, "/api/agents/memory/auto-sync?"+q, nil)
		action, target, ok = auditActionTarget(req)
		if !ok || action != "memory.auto_sync" || target != want {
			t.Fatalf("auditActionTarget(auto-sync?%s) = (%q, %q, ok=%v), want memory.auto_sync/%q", q, action, target, ok, want)
		}
	}
	// The other read-only routes are not audited.
	for _, p := range []string{"/api/agents/memory/snapshots", "/api/agents/memory/tree", "/api/agents/memory/diff", "/api/agents/memory/claude-import", "/api/agents/memory/claude-import/preview", "/api/agents/memory/muse-import", "/api/agents/memory/muse-import/preview", "/api/agents/memory/claude-export", "/api/agents/memory/claude-export/preview", "/api/agents/memory/auto-sync", "/api/agents/memory/auto-sync/preview", "/api/agents/memory/af-import/preview"} {
		if _, _, ok := auditActionTarget(httptest.NewRequest(http.MethodGet, p, nil)); ok {
			t.Errorf("GET %s should not be audited", p)
		}
	}
}

// A transport failure on a memory import preview must not put the (unscanned) query in the CP log.
func TestMemoryImportRelayLogDoesNotQuoteQuery(t *testing.T) {
	proxy, res, _, cleanup := newFSProxyTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	logs := &lockedBuf{}
	old := log.Writer()
	log.SetOutput(logs)
	t.Cleanup(func() { log.SetOutput(old) })
	cleanup() // the Agent is stopped
	secret := "ghp_" + strings.Repeat("q7Zx", 9)
	for _, p := range []string{
		"/api/agents/memory/muse-import/preview?dir=" + secret,
		"/api/agents/memory/claude-import/preview?slug=" + secret,
	} {
		rec := httptest.NewRecorder()
		proxy.rest(rec, httptest.NewRequest(http.MethodGet, p, nil), res)
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("%s: %d", p, rec.Code)
		}
	}
	if !strings.Contains(logs.String(), "agent proxy: GET /api/agents/memory/muse-import/preview") {
		t.Fatalf("the failure was not logged: %q", logs.String())
	}
	if strings.Contains(logs.String(), secret) {
		t.Errorf("the query reached the CP log: %s", logs.String())
	}
}

// An approval that published the memory but could not clear the proposal from the queue answers 500
// decision_incomplete. The commit exists, so the CP audits the approval anyway (the member may never
// retry); any other failure is not audited, and the response reaches the caller untouched.
func TestReviewApprovalPublishedButNotClearedIsAudited(t *testing.T) {
	const id = "0123456789abcdef0123"
	cases := []struct {
		name, body string
		status     int
		wantAudit  bool
		wantDetail string
	}{
		{"published, queue not cleared", `{"error":{"code":"decision_incomplete","message":"published, but removing it failed"}}`, 500, true, "published_queue_not_cleared"},
		{"another failure", `{"error":{"code":"internal","message":"the proposal could not be published"}}`, 500, false, ""},
		{"stale", `{"error":{"code":"proposal_stale","message":"x"}}`, 409, false, ""},
		{"approved", `{"commit":"abc","revision":1}`, 200, true, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			proxy, res, st, cleanup := newFSProxyTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/agents/memory-review/proposals/approve" || r.Header.Get("X-AF-Member-Origin") == "" {
					t.Errorf("upstream request = %s, mark %q", r.URL.Path, r.Header.Get("X-AF-Member-Origin"))
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(c.status)
				_, _ = w.Write([]byte(c.body))
			}))
			defer cleanup()
			req := httptest.NewRequest(http.MethodPost, "/api/agents/memory-review/proposals/approve?id="+id, strings.NewReader(`{"id":"`+id+`"}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			proxy.rest(rec, req, res)
			if rec.Code != c.status || rec.Body.String() != c.body {
				t.Fatalf("response = %d %s", rec.Code, rec.Body.String())
			}
			rows, err := st.ListAuditByTenant(context.Background(), res.ws.TenantID, 10)
			if err != nil {
				t.Fatal(err)
			}
			if !c.wantAudit {
				if len(rows) != 0 {
					t.Fatalf("audited: %+v", rows)
				}
				return
			}
			if len(rows) != 1 || rows[0].Action != "memory.review.approve" || rows[0].Target != id ||
				rows[0].Detail != c.wantDetail || rows[0].HTTPStatus != c.status {
				t.Fatalf("audit = %+v", rows)
			}
		})
	}
}
