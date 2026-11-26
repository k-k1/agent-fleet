// session_wire_place_test.go — a session's stored place survives both sources of GET /api/sessions.
//
// A space and a repository can share a folder name, so the Console groups by the place the Agent
// stored at create (#5219). While the Workspace is stopped the list comes from the DB mirror, which
// has to keep that place, or a space session lands under the repository of the same name.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

func TestSessionsPayloadKeepsPlaceThroughTheMirror(t *testing.T) {
	ctx := context.Background()
	st, err := openMigratedSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	tenant, _ := st.EnsureDefaultTenant(ctx)
	ident, _ := st.UpsertIdentity(ctx, "owner@example.com", "owner", "")
	member, _ := st.EnsureMembership(ctx, ident.ID, tenant.ID, "member")
	ws := store.Workspace{ID: "ws-place", TenantID: tenant.ID, MembershipID: member.ID,
		ContainerName: "owner", Network: "test", DataDir: "/data/owner", AgentPort: "1", AgentToken: "tok",
		State: "running", CreatedAt: store.NowTS()}
	if err := st.CreateWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}

	// The collision: a space "app" and a repository "app" report the same repo (the Agent's
	// basename rule). Only place tells them apart. "legacy" predates the stored place.
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sessions":[
			{"name":"inrepo","kind":"claude","dir":"/home/dev/repos/app","repo":"app","createdAt":"2026-11-26T10:00:00+09:00","alive":true,
			 "place":{"kind":"repo","id":"app"}},
			{"name":"inspace","kind":"claude","dir":"/home/dev/spaces/app","repo":"app","space":"app","createdAt":"2026-11-26T09:00:00+09:00","alive":false,
			 "place":{"kind":"space","id":"app"}},
			{"name":"inwt","kind":"codex","dir":"/home/dev/repos/app@wip","repo":"app@wip","createdAt":"2026-11-26T08:00:00+09:00","alive":false,
			 "place":{"kind":"worktree","id":"app@wip","parent":"app"}},
			{"name":"legacy","kind":"claude","dir":"/home/dev/repos/app","repo":"app","createdAt":"2026-11-26T07:00:00+09:00","alive":false}
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
		byName := map[string]map[string]any{}
		for _, s := range body.Sessions {
			byName[s["name"].(string)] = s
		}
		if len(byName) != 4 {
			t.Fatalf("%s: sessions = %v, want four", state, body.Sessions)
		}
		return byName
	}
	want := map[string]map[string]any{
		"inrepo":  {"kind": "repo", "id": "app"},
		"inspace": {"kind": "space", "id": "app"},
		"inwt":    {"kind": "worktree", "id": "app@wip", "parent": "app"},
	}
	check := func(state string, got map[string]map[string]any) {
		t.Helper()
		for name, w := range want {
			p, ok := got[name]["place"].(map[string]any)
			if !ok || len(p) != len(w) {
				t.Errorf("%s: %s place = %v, want %v", state, name, got[name]["place"], w)
				continue
			}
			for k, v := range w {
				if p[k] != v {
					t.Errorf("%s: %s place.%s = %v, want %v", state, name, k, p[k], v)
				}
			}
		}
		if v, ok := got["legacy"]["place"]; ok {
			t.Errorf("%s: legacy place = %v, want absent (the Console falls back to the folder)", state, v)
		}
		for _, name := range []string{"inrepo", "inwt", "legacy"} {
			if v, ok := got[name]["space"]; ok {
				t.Errorf("%s: %s space = %v, want absent", state, name, v)
			}
		}
	}

	running := payload("running") // also fills the mirror
	check("running", running)
	if running["inspace"]["space"] != "app" {
		t.Errorf("running: inspace space = %v, want app (relayed from the Agent)", running["inspace"]["space"])
	}
	stopped := payload("stopped") // the mirror alone
	check("stopped", stopped)
	// Not derived from Place while stopped: the Console would ask the stopped Agent for space names.
	if v, ok := stopped["inspace"]["space"]; ok {
		t.Errorf("stopped: inspace space = %v, want absent", v)
	}
}

// A mirror row whose place column holds something unreadable is served without a place, never
// fails the list: the Console then falls back to the folder name as for an older session.
func TestMirrorPlaceColumnTolerance(t *testing.T) {
	ctx := context.Background()
	st, err := openMigratedSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	tenant, _ := st.EnsureDefaultTenant(ctx)
	ident, _ := st.UpsertIdentity(ctx, "owner@example.com", "owner", "")
	member, _ := st.EnsureMembership(ctx, ident.ID, tenant.ID, "member")
	ws := store.Workspace{ID: "ws-place2", TenantID: tenant.ID, MembershipID: member.ID,
		ContainerName: "owner", Network: "test", DataDir: "/data/owner", AgentPort: "1", AgentToken: "tok",
		State: "stopped", CreatedAt: store.NowTS()}
	if err := st.CreateWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}
	rows := sessionRows([]sessionWire{
		{Name: "ok", Kind: "claude", Place: &sessionPlaceWire{Kind: "home"}},
		{Name: "bad", Kind: "claude"},
		{Name: "nokind", Kind: "claude", Place: &sessionPlaceWire{ID: "app"}},
		{Name: "rawnokind", Kind: "claude"},
	})
	if err := st.ReplaceSessions(ctx, ws.ID, rows); err != nil {
		t.Fatal(err)
	}
	// Written straight to the column: ReplaceSessions would store NULL for either value.
	for name, v := range map[string]string{"bad": "{not json", "rawnokind": `{"id":"app"}`} {
		if _, err := st.DB().ExecContext(ctx, `UPDATE session SET place=? WHERE workspace_id=? AND name=?`, v, ws.ID, name); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.ListSessions(ctx, ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]store.SessionRow{}
	for _, r := range got {
		byName[r.Name] = r
	}
	if p := byName["ok"].Place; p == nil || p.Kind != "home" || p.ID != "" {
		t.Errorf("ok place = %+v, want home", p)
	}
	if p := byName["bad"].Place; p != nil {
		t.Errorf("unreadable place column = %+v, want nil", p)
	}
	if p := byName["nokind"].Place; p != nil {
		t.Errorf("a place without a kind = %+v, want nil (stored as NULL)", p)
	}
	if p := byName["rawnokind"].Place; p != nil {
		t.Errorf("a stored place without a kind = %+v, want nil", p)
	}
	var raw sql.NullString
	if err := st.DB().QueryRowContext(ctx, `SELECT place FROM session WHERE workspace_id=? AND name=?`, ws.ID, "nokind").Scan(&raw); err != nil || raw.Valid {
		t.Errorf("a written place without a kind = %+v (%v), want NULL", raw, err)
	}
}
