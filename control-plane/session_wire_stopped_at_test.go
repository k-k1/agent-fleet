// session_wire_stopped_at_test.go — a session's stop time survives both sources of GET /api/sessions.
//
// The Console's Active view lists a stopped session for a day after the Agent first saw it stopped
// (#5155). While the Workspace is stopped the list comes from the DB mirror, so the instant has to
// be stored there, or the morning-after view is empty exactly when it matters.
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

func TestSessionsPayloadKeepsStoppedAtThroughTheMirror(t *testing.T) {
	ctx := context.Background()
	st, err := openMigratedSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	tenant, _ := st.EnsureDefaultTenant(ctx)
	ident, _ := st.UpsertIdentity(ctx, "owner@example.com", "owner", "")
	member, _ := st.EnsureMembership(ctx, ident.ID, tenant.ID, "member")
	ws := store.Workspace{ID: "ws-stopped-at", TenantID: tenant.ID, MembershipID: member.ID,
		ContainerName: "owner", Network: "test", DataDir: "/data/owner", AgentPort: "1", AgentToken: "tok",
		State: "running", CreatedAt: store.NowTS()}
	if err := st.CreateWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}
	// "revived" reports a stamp while alive (it must not be mirrored); "old" predates the stamp.
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sessions":[
			{"name":"gone","kind":"claude","dir":"/d/a","repo":"a","createdAt":"2026-11-26T09:00:00+09:00","alive":false,"stoppedAt":"2026-11-26T10:00:00Z"},
			{"name":"revived","kind":"claude","dir":"/d/b","repo":"b","createdAt":"2026-11-26T08:00:00+09:00","alive":true,"stoppedAt":"2026-11-26T07:00:00Z"},
			{"name":"old","kind":"claude","dir":"/d/c","repo":"c","createdAt":"2026-11-26T07:00:00+09:00","alive":false}
		]}`))
	}))
	t.Cleanup(agent.Close)

	api := workspaceAPI{memberAuth: memberAuth{mgr: &manager{store: st}}}
	payload := func(state string) map[string]map[string]any {
		t.Helper()
		res := &resolved{rt: stubRuntime{endpoint: agent.URL, token: "tok", state: state}, ws: ws}
		raw, err := json.Marshal(api.sessionsPayload(ctx, res))
		if err != nil {
			t.Fatal(err)
		}
		var body struct {
			Sessions []map[string]any `json:"sessions"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatal(err)
		}
		by := map[string]map[string]any{}
		for _, s := range body.Sessions {
			by[s["name"].(string)] = s
		}
		return by
	}
	check := func(state string, got map[string]map[string]any, revived any) {
		t.Helper()
		if got["gone"]["stoppedAt"] != "2026-11-26T10:00:00Z" {
			t.Errorf("%s: gone stoppedAt = %v, want the Agent's instant", state, got["gone"]["stoppedAt"])
		}
		if v, ok := got["old"]["stoppedAt"]; ok {
			t.Errorf("%s: old stoppedAt = %v, want absent (unknown, not recent)", state, v)
		}
		if got["revived"]["stoppedAt"] != revived {
			t.Errorf("%s: revived stoppedAt = %v, want %v", state, got["revived"]["stoppedAt"], revived)
		}
	}
	check("running", payload("running"), "2026-11-26T07:00:00Z") // relayed as the Agent sent it; also fills the mirror
	stopped := payload("stopped")
	check("stopped", stopped, nil) // a live row's stamp is not mirrored
}

// A stop time that is not an RFC3339 instant (a NUL byte included, which Postgres TEXT refuses and
// which would roll back the whole mirror swap) is dropped; the valid rows are still stored.
func TestMirrorDropsAStoppedAtThatIsNotATime(t *testing.T) {
	ctx := context.Background()
	st, err := openMigratedSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	tenant, _ := st.EnsureDefaultTenant(ctx)
	ident, _ := st.UpsertIdentity(ctx, "owner@example.com", "owner", "")
	member, _ := st.EnsureMembership(ctx, ident.ID, tenant.ID, "member")
	ws := store.Workspace{ID: "ws-stopped-at2", TenantID: tenant.ID, MembershipID: member.ID,
		ContainerName: "owner", Network: "test", DataDir: "/data/owner", AgentPort: "1", AgentToken: "tok",
		State: "stopped", CreatedAt: store.NowTS()}
	if err := st.CreateWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}
	rows := sessionRows([]sessionWire{
		{Name: "good", Kind: "claude", StoppedAt: "2026-11-26T10:00:00Z"},
		{Name: "nul", Kind: "claude", StoppedAt: "2026-11-26T10:00:00Z\x00"},
		{Name: "junk", Kind: "claude", StoppedAt: "yesterday"},
	})
	if err := st.ReplaceSessions(ctx, ws.ID, rows); err != nil {
		t.Fatal(err)
	}
	got, err := st.ListSessions(ctx, ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]string{}
	for _, r := range got {
		by[r.Name] = r.StoppedAt
	}
	if len(by) != 3 || by["good"] != "2026-11-26T10:00:00Z" || by["nul"] != "" || by["junk"] != "" {
		t.Errorf("stored stopped_at = %v, want only good kept", by)
	}
}
