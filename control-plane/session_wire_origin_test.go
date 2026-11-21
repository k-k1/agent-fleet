// session_wire_origin_test.go — origin and originSession survive the stopped-workspace list.
//
// The Console moves scheduled and auto-run runs out of the Sessions list by origin and
// originSession. While the Workspace is stopped the DB mirror is the only source, so both
// keys have to be written by sessionRows and read back by the stopped path.
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

func TestSessionsPayloadKeepsOriginThroughTheMirror(t *testing.T) {
	ctx := context.Background()
	st, err := openMigratedSQLite(filepath.Join(t.TempDir(), "cp.db"))
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
	ws := store.Workspace{ID: "ws-origin", TenantID: tenant.ID, MembershipID: member.ID,
		ContainerName: "owner", Network: "test", DataDir: "/data/owner", AgentPort: "1", AgentToken: "tok",
		State: "running", CreatedAt: store.NowTS()}
	if err := st.CreateWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}

	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sessions":[
			{"name":"child","kind":"claude","createdAt":"2026-09-23T10:00:00+09:00","alive":true,
			 "origin":"session","originSession":"parent"},
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
		if len(byName) != 2 {
			t.Fatalf("%s: sessions = %v, want child and plain", state, body.Sessions)
		}
		return byName
	}

	payload("running") // fills the mirror
	stopped := payload("stopped")
	if stopped["child"]["origin"] != "session" || stopped["child"]["originSession"] != "parent" {
		t.Errorf("stopped: child origin = %v / %v, want session / parent",
			stopped["child"]["origin"], stopped["child"]["originSession"])
	}
	for _, k := range []string{"origin", "originSession"} {
		if v, ok := stopped["plain"][k]; ok {
			t.Errorf("stopped: plain %s = %v, want absent", k, v)
		}
	}
}
