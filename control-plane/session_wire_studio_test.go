// session_wire_studio_test.go — the studio binding survives both sources of GET /api/sessions.
//
// sessionsPayload answers from the Agent while the Workspace runs and from the DB mirror
// while it is stopped. The Console routes a studio-bound session to the studio pane by
// sessionWire.studio (ADR 0100 decision 10), so the key has to cross both: the relay
// (TestAgentSessionsRelayKeepsFields) and the mirror write/read that this file drives.
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

func TestSessionsPayloadKeepsStudioThroughTheMirror(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	tenant, _ := st.EnsureDefaultTenant(ctx)
	ident, _ := st.UpsertIdentity(ctx, "owner@example.com", "owner", "")
	member, _ := st.EnsureMembership(ctx, ident.ID, tenant.ID, "member")
	ws := store.Workspace{ID: "ws-studio", TenantID: tenant.ID, MembershipID: member.ID,
		ContainerName: "owner", Network: "test", DataDir: "/data/owner", AgentPort: "1", AgentToken: "tok",
		State: "running", CreatedAt: store.NowTS()}
	if err := st.CreateWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}

	const studio = "0b9d1f2e-7c4a-4e1b-9a3d-5f6e7a8b9c0d"
	// One live studio session, one stopped studio session, one plain session: the mirror
	// keeps every row whatever its state, and an unbound row must stay unbound.
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sessions":[
			{"name":"live","kind":"claude","createdAt":"2026-09-23T10:00:00+09:00","alive":true,
			 "studio":"` + studio + `","initialPromptState":"delivered"},
			{"name":"parked","kind":"codex","createdAt":"2026-09-23T09:00:00+09:00","alive":false,
			 "studio":"` + studio + `","initialPromptState":"unknown"},
			{"name":"plain","kind":"claude","createdAt":"2026-09-23T08:00:00+09:00","alive":false}
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
		if len(byName) != 3 {
			t.Fatalf("%s: sessions = %v, want live, parked and plain", state, body.Sessions)
		}
		return byName
	}

	// Running: the Agent's list, which also fills the mirror.
	running := payload("running")
	if running["live"]["studio"] != studio || running["parked"]["studio"] != studio {
		t.Errorf("running: studio = %v / %v, want %s on both bound sessions",
			running["live"]["studio"], running["parked"]["studio"], studio)
	}
	if running["live"]["initialPromptState"] != "delivered" {
		t.Errorf("running: initialPromptState = %v, want delivered", running["live"]["initialPromptState"])
	}

	// Stopped: the mirror alone. The binding has to come back, or the first paint after a
	// stop opens a studio session in the mirror pane.
	stopped := payload("stopped")
	for _, name := range []string{"live", "parked"} {
		if stopped[name]["studio"] != studio {
			t.Errorf("stopped: %s studio = %v, want %s", name, stopped[name]["studio"], studio)
		}
		// No mirror column on purpose: delivery only happens in a running Workspace.
		if v, ok := stopped[name]["initialPromptState"]; ok {
			t.Errorf("stopped: %s initialPromptState = %v, want absent", name, v)
		}
	}
	if v, ok := stopped["plain"]["studio"]; ok {
		t.Errorf("stopped: plain studio = %v, want absent", v)
	}
}
