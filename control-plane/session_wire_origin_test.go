// session_wire_origin_test.go — origin and originSession survive the stopped-workspace list.
//
// The Console classifies runs (auto-run views vs. the Sessions list) by origin and
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
			{"name":"plain","kind":"claude","createdAt":"2026-09-23T08:00:00+09:00","alive":false},
			{"name":"nul","kind":"claude","createdAt":"2026-09-23T07:00:00+09:00","alive":false,
			 "origin":"qu\u0000eue","originSession":"queue-\u0000root"},
			{"name":"mixed","kind":"claude","createdAt":"2026-09-23T06:00:00+09:00","alive":false,
			 "origin":"session","originSession":"bad\u0000"}
		]}`))
	}))
	t.Cleanup(agent.Close)

	// Stopped is a fresh manager over the same DB and an Agent that does not answer, so only the
	// mirror can supply the values.
	payload := func(state string) map[string]map[string]any {
		t.Helper()
		endpoint := agent.URL
		if state == "stopped" {
			endpoint = "http://127.0.0.1:1"
		}
		api := workspaceAPI{memberAuth: memberAuth{mgr: &manager{store: st}}}
		res := &resolved{rt: stubRuntime{endpoint: endpoint, token: "tok", state: state}, ws: ws}
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
			t.Fatalf("%s: sessions = %v, want child, plain, nul and mixed", state, body.Sessions)
		}
		return byName
	}

	payload("running") // fills the mirror
	stopped := payload("stopped")
	if stopped["child"]["origin"] != "session" || stopped["child"]["originSession"] != "parent" {
		t.Errorf("stopped: child origin = %v / %v, want session / parent",
			stopped["child"]["origin"], stopped["child"]["originSession"])
	}
	if stopped["child"]["alive"] == true {
		t.Errorf("stopped: child alive = true, want the mirror's stopped row")
	}
	// A NUL-bearing field is dropped whole: stripping the byte would forge "queue" or "queue-root".
	for _, c := range [][2]string{{"nul", "origin"}, {"nul", "originSession"}, {"mixed", "originSession"}} {
		if v, ok := stopped[c[0]][c[1]]; ok {
			t.Errorf("stopped: %s %s = %v, want absent", c[0], c[1], v)
		}
	}
	if stopped["mixed"]["origin"] != "session" {
		t.Errorf("stopped: mixed origin = %v, want the healthy field kept", stopped["mixed"]["origin"])
	}
	for _, k := range []string{"origin", "originSession"} {
		if v, ok := stopped["plain"][k]; ok {
			t.Errorf("stopped: plain %s = %v, want absent", k, v)
		}
	}
}
