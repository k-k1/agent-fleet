// session_mirror_links_test.go — a session's work-item links survive both sources of
// GET /api/sessions (#3147): the relay while the Workspace runs, the DB mirror while it is
// stopped. The bounds on what the Agent sends are pinned in internal/store.
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

func TestSessionsPayloadKeepsLinksThroughTheMirror(t *testing.T) {
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
	ws := store.Workspace{ID: "ws-links", TenantID: tenant.ID, MembershipID: member.ID,
		ContainerName: "owner", Network: "test", DataDir: "/data/owner", AgentPort: "1", AgentToken: "tok",
		State: "running", CreatedAt: store.NowTS()}
	if err := st.CreateWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}

	const links = `[{"provider":"jira","key":"PROJ-1","role":"started","title":"Login"},` +
		`{"provider":"github","key":"o/r#45","role":"working","url":"https://github.com/o/r/pull/45","at":"2026-11-01T00:00:00Z","state":"merged"}]`
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sessions":[
			{"name":"linked","kind":"claude","createdAt":"2026-11-01T09:00:00+09:00","alive":true,"links":` + links + `},
			{"name":"plain","kind":"claude","createdAt":"2026-11-01T08:00:00+09:00","alive":false}
		]}`))
	}))
	t.Cleanup(agent.Close)

	api := workspaceAPI{memberAuth: memberAuth{mgr: &manager{store: st}}}
	payload := func(state string) map[string]map[string]json.RawMessage {
		t.Helper()
		res := &resolved{rt: stubRuntime{endpoint: agent.URL, token: "tok", state: state}, ws: ws}
		raw, err := json.Marshal(api.sessionsPayload(ctx, res))
		if err != nil {
			t.Fatal(err)
		}
		var body struct {
			Sessions []map[string]json.RawMessage `json:"sessions"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatal(err)
		}
		byName := map[string]map[string]json.RawMessage{}
		for _, s := range body.Sessions {
			var name string
			_ = json.Unmarshal(s["name"], &name)
			byName[name] = s
		}
		return byName
	}

	if got := payload("running")["linked"]["links"]; string(got) != links {
		t.Errorf("running: links = %s, want the Agent's %s", got, links)
	}
	// Stopped: the mirror alone gives the same array back, in the same order.
	stopped := payload("stopped")
	if got := stopped["linked"]["links"]; string(got) != links {
		t.Errorf("stopped: links = %s, want the mirrored %s", got, links)
	}
	if got, ok := stopped["plain"]["links"]; ok {
		t.Errorf("stopped: plain links = %s, want absent", got)
	}
}
