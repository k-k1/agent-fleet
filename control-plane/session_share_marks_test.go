package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// marksFixture builds a share with one owner, one recipient (at the given permission) and
// one stranger, and stubs out the Agent of the owner's workspace. agentMarks is the list
// a GET returns; seen records the writes the Agent actually received.
type marksFixture struct {
	api       sessionShareAPI
	catalogID string

	recipientIdent store.Identity
	recipientView  store.MembershipView
	strangerIdent  store.Identity
	strangerView   store.MembershipView

	agentMarks []any
	seenPOST   map[string]any
	seenDELETE string
}

func newMarksFixture(t *testing.T, permission string) *marksFixture {
	t.Helper()
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
	ownerIdentity, _ := st.UpsertIdentity(ctx, "owner@example.com", "owner", "")
	recipientIdentity, _ := st.UpsertIdentity(ctx, "recipient@example.com", "recipient", "")
	strangerIdentity, _ := st.UpsertIdentity(ctx, "stranger@example.com", "stranger", "")
	owner, _ := st.EnsureMembership(ctx, ownerIdentity.ID, tenant.ID, "member")
	recipient, _ := st.EnsureMembership(ctx, recipientIdentity.ID, tenant.ID, "member")
	_, _ = st.EnsureMembership(ctx, strangerIdentity.ID, tenant.ID, "member")
	recipientViews, _ := st.ListMemberships(ctx, recipientIdentity.ID)
	strangerViews, _ := st.ListMemberships(ctx, strangerIdentity.ID)

	workspace := store.Workspace{ID: "ws-owner", TenantID: tenant.ID, MembershipID: owner.ID,
		ContainerName: "owner", Network: "test", DataDir: "/data/owner", AgentPort: "1", AgentToken: "tok",
		State: "running", CreatedAt: store.NowTS()}
	if err := st.CreateWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}

	f := &marksFixture{
		catalogID:      "catalog-1",
		recipientIdent: recipientIdentity,
		recipientView:  recipientViews[0],
		strangerIdent:  strangerIdentity,
		strangerView:   strangerViews[0],
	}
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/sessions/catalog":
			_ = json.NewEncoder(w).Encode(map[string]any{"sessions": []any{map[string]any{
				"name": "session-1", "kind": "claude", "dir": "/home/dev/repos/private", "repo": "private", "workingCopyId": "wc-1",
			}}})
		case r.URL.Path == "/repos":
			_ = json.NewEncoder(w).Encode(map[string]any{"repos": []any{map[string]any{"workingCopyId": "wc-1"}}})
		case r.URL.Path == "/sessions/session-1/marks" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"marks": f.agentMarks})
		case r.URL.Path == "/sessions/session-1/marks" && r.Method == http.MethodPost:
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &f.seenPOST)
			_ = json.NewEncoder(w).Encode(map[string]any{"mark": f.seenPOST})
		case r.URL.Path == "/sessions/session-1/marks" && r.Method == http.MethodDelete:
			f.seenDELETE = r.URL.RawQuery
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected agent call %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(agent.Close)

	mgr := &manager{store: st, rts: map[string]cachedRT{owner.ID: {
		rt: stubRuntime{endpoint: agent.URL, token: "tok"}, ws: workspace,
	}}}
	f.api = newSessionShareAPI(mgr)
	catalog := store.SharedSessionCatalog{ID: f.catalogID, WorkspaceID: workspace.ID, OwnerMembershipID: owner.ID,
		Name: "session-1", Kind: "claude", WorkingCopyID: "wc-1", LastSeen: store.NowTS()}
	if err := st.ReplaceSharedSessionCatalog(ctx, workspace.ID, owner.ID, []store.SharedSessionCatalog{catalog}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutSessionShare(ctx, store.SessionShare{ID: "share-1", TenantID: tenant.ID, OwnerMembershipID: owner.ID,
		RecipientMembershipID: recipient.ID, ScopeType: "session", ScopeKey: "session-1", Permission: permission,
		CreatedAt: store.NowTS(), UpdatedAt: store.NowTS()}); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *marksFixture) call(method, query string, body string, ident store.Identity, mv store.MembershipView) *httptest.ResponseRecorder {
	url := "/api/shared-sessions/" + f.catalogID + "/marks"
	if query != "" {
		url += "?" + query
	}
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, url, rdr)
	req.SetPathValue("id", f.catalogID)
	rec := httptest.NewRecorder()
	f.api.marks(rec, req, ident, mv)
	return rec
}

func TestSharedMarksRead(t *testing.T) {
	f := newMarksFixture(t, "ro")
	f.agentMarks = []any{map[string]any{
		"id": "mk_1", "turn": "uuid-1", "part": float64(0), "kind": "text",
		"quote": "the sentence", "nth": float64(0), "color": "yellow",
		"author": "recipient@example.com", "created_at": float64(1700000000000),
	}}

	rec := f.call(http.MethodGet, "", "", f.recipientIdent, f.recipientView)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("Cache-Control=%q", got)
	}
	for _, want := range []string{"mk_1", "uuid-1", "the sentence", "recipient@example.com"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("visible field %q removed: %s", want, rec.Body.String())
		}
	}

	// Someone without permission is not even told the share exists.
	if denied := f.call(http.MethodGet, "", "", f.strangerIdent, f.strangerView); denied.Code != http.StatusNotFound {
		t.Fatalf("stranger status=%d body=%s", denied.Code, denied.Body.String())
	}
}

// A mark's quote travels to the recipient so the position can be restored. Relaying a
// mark that sits on a part whose coordinates the share DTO drops — a tool line, say —
// sends that supposedly dropped path out as the quote (docs/log/69 §69.4). The Console
// and the Agent restrict where a mark may be placed as well, but the relay drops them at
// its own exit too, so one side loosening is not enough to leak.
func TestSharedMarksDropNonProseKind(t *testing.T) {
	f := newMarksFixture(t, "ro")
	f.agentMarks = []any{
		map[string]any{"id": "mk_ok", "turn": "u", "part": float64(0), "kind": "text", "quote": "prose", "nth": float64(0), "color": "blue"},
		map[string]any{"id": "mk_bad", "turn": "u", "part": float64(1), "kind": "tool", "quote": "/home/dev/repos/private/secret.ts", "nth": float64(0), "color": "blue"},
	}
	rec := f.call(http.MethodGet, "", "", f.recipientIdent, f.recipientView)
	body := rec.Body.String()
	if !strings.Contains(body, "mk_ok") {
		t.Fatalf("prose mark dropped: %s", body)
	}
	for _, secret := range []string{"mk_bad", "/home/dev/repos/private/secret.ts"} {
		if strings.Contains(body, secret) {
			t.Fatalf("a non-prose mark was relayed (%q): %s", secret, body)
		}
	}
}

// RO can read but not write. Writes are cut on the same RW line as docs/log/59 §2: not
// part of the approval flow, but the same permission.
func TestSharedMarksWriteNeedsRW(t *testing.T) {
	f := newMarksFixture(t, "ro")
	body := `{"id":"mk_2","turn":"uuid-1","part":0,"kind":"text","quote":"q","nth":0,"color":"green"}`
	if rec := f.call(http.MethodPost, "", body, f.recipientIdent, f.recipientView); rec.Code != http.StatusNotFound {
		t.Fatalf("RO POST status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := f.call(http.MethodDelete, "id=mk_2", "", f.recipientIdent, f.recipientView); rec.Code != http.StatusNotFound {
		t.Fatalf("RO DELETE status=%d body=%s", rec.Code, rec.Body.String())
	}
	if f.seenPOST != nil || f.seenDELETE != "" {
		t.Fatalf("an RO write reached the Agent: post=%+v delete=%q", f.seenPOST, f.seenDELETE)
	}
}

// author is never taken from the request: accepting it would let a recipient impersonate
// the owner or another recipient.
func TestSharedMarksStampAuthor(t *testing.T) {
	f := newMarksFixture(t, "rw")
	body := `{"id":"mk_2","turn":"uuid-1","part":0,"kind":"text","quote":"q","nth":0,"color":"green","author":"owner@example.com"}`
	rec := f.call(http.MethodPost, "", body, f.recipientIdent, f.recipientView)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if f.seenPOST["author"] != "recipient@example.com" {
		t.Fatalf("author was not overwritten: %+v", f.seenPOST)
	}
	if !strings.Contains(rec.Body.String(), "recipient@example.com") {
		t.Fatalf("the response carries no author: %s", rec.Body.String())
	}

	// Not even RW may place a mark on a part that carries coordinates.
	bad := `{"id":"mk_3","turn":"uuid-1","part":1,"kind":"tool","quote":"/private/x.ts","nth":0,"color":"green"}`
	if r := f.call(http.MethodPost, "", bad, f.recipientIdent, f.recipientView); r.Code != http.StatusBadRequest {
		t.Fatalf("non-prose POST status=%d body=%s", r.Code, r.Body.String())
	}
}

// Only your own marks can be deleted. The Agent makes that call, but it only works
// because the CP always attaches the author.
func TestSharedMarksDeleteCarriesAuthor(t *testing.T) {
	f := newMarksFixture(t, "rw")
	rec := f.call(http.MethodDelete, "id=mk_9", "", f.recipientIdent, f.recipientView)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(f.seenDELETE, "author=recipient%40example.com") || !strings.Contains(f.seenDELETE, "id=mk_9") {
		t.Fatalf("query passed to the Agent: %q", f.seenDELETE)
	}
}
