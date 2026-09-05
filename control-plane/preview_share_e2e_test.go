// preview_share_e2e_test.go — sharing a preview with a member of the same tenant
// (docs/log/81 §14 / ADR 0062 decisions 14-17). These drive newPreviewHostEnv's real route
// table and take the viewer through as "someone in the same tenant who is not the owner".
//
// Two things are being checked:
//   - that it goes through while sharing is on, from the handshake to the relayed request;
//   - that it closes on the NEXT request once sharing is turned off, even though the cookie
//     is still valid (decision 15). Get that wrong and revoking a share leaves the preview
//     visible for another 12 hours.
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// previewViewer is the second person in the tenant — not the owner.
type previewViewer struct {
	userKey      string
	identityID   string
	membershipID string
}

// addViewer creates a second active member of the same tenant, with a workspace of its
// own. The workspace is created up front so nothing has to be provisioned during the test:
// letting resolveFull reach createWorkspace would tie these cases to whatever the runtime
// happens to do.
func (e *previewHostEnv) addViewer(t *testing.T, userKey string) previewViewer {
	t.Helper()
	ctx := context.Background()
	ident, err := e.mgr.store.UpsertIdentity(ctx, "", userKey, "")
	if err != nil {
		t.Fatalf("viewer identity: %v", err)
	}
	mem, err := e.mgr.store.EnsureMembership(ctx, ident.ID, e.ws.TenantID, "member")
	if err != nil {
		t.Fatalf("viewer membership: %v", err)
	}
	ws := store.Workspace{ID: "ws-" + userKey, TenantID: e.ws.TenantID, MembershipID: mem.ID,
		ContainerName: "c-" + userKey, Network: "n-" + userKey, DataDir: "d-" + userKey,
		AgentPort: "1", AgentToken: "t", State: "running", CreatedAt: store.NowTS()}
	if err := e.mgr.store.CreateWorkspace(ctx, ws); err != nil {
		t.Fatalf("viewer workspace: %v", err)
	}
	return previewViewer{userKey: userKey, identityID: ident.ID, membershipID: mem.ID}
}

// asUser runs fn with the dev-auth identity switched to userKey. Under AUTH=dev,
// resolveIdentity hands back devUser as is, which makes this the shortest way to arrive as
// a different person.
func (e *previewHostEnv) asUser(userKey string, fn func()) {
	prev := e.mgr.devUser
	e.mgr.devUser = userKey
	defer func() { e.mgr.devUser = prev }()
	fn()
}

func (e *previewHostEnv) setPreviewSettings(t *testing.T, mut func(*wsSettings)) {
	t.Helper()
	ctx := context.Background()
	raw, _ := e.mgr.store.GetWorkspaceSettings(ctx, e.ws.ID)
	st := parseWSSettings(raw)
	mut(&st)
	if err := e.mgr.store.SetWorkspaceSettings(ctx, e.ws.ID, toWSSettingsJSON(t, st)); err != nil {
		t.Fatalf("save ws settings: %v", err)
	}
}

// console issues a request against the Console-origin mux; authGate lets it through on dev
// auth.
func (e *previewHostEnv) console(t *testing.T, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

// viewerCookie walks the whole handshake as `viewer` and returns the minted af_pv.
func (e *previewHostEnv) viewerCookie(t *testing.T, v previewViewer, slug string, port int) *http.Cookie {
	t.Helper()
	var back string
	e.asUser(v.userKey, func() {
		rec := e.console(t, "https://af.example.com"+previewHandshakePath+
			"?slug="+slug+"&port="+strconv.Itoa(port)+"&next=/")
		if rec.Code != http.StatusFound {
			t.Fatalf("viewer handshake: code=%d body=%s", rec.Code, rec.Body.String())
		}
		back = rec.Header().Get("Location")
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, back, nil)
	req.Host = previewHostname(slug, port, e.domain)
	req.Header.Set("X-Forwarded-Proto", "https")
	e.host.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("viewer callback: code=%d body=%s", rec.Code, rec.Body.String())
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == previewAuthCookie {
			return c
		}
	}
	t.Fatal("viewer callback minted no af_pv cookie")
	return nil
}

// A workspace that is not shared does not exist as far as anyone else in the tenant is
// concerned. 404, not 401 or 403: no answer that reads as "you might see this if you signed
// in".
func TestPreviewNotSharedIsNotFoundForAColleague(t *testing.T) {
	e := newPreviewHostEnv(t, "http://127.0.0.1:1")
	v := e.addViewer(t, "colleague")
	slug := e.mintSlug(t)

	e.asUser(v.userKey, func() {
		rec := e.console(t, "https://af.example.com"+previewHandshakePath+"?slug="+slug+"&port=3000&next=/")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("colleague handshake without sharing: code=%d, want 404", rec.Code)
		}
	})
}

// With sharing on, a colleague clears the handshake and the request is actually relayed.
func TestPreviewTenantShareLetsAColleagueThrough(t *testing.T) {
	var seen *http.Request
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Clone(r.Context())
		w.WriteHeader(http.StatusOK)
	}))
	defer agent.Close()
	e := newPreviewHostEnv(t, agent.URL)
	v := e.addViewer(t, "colleague")
	slug := e.mintSlug(t)
	e.setPreviewSettings(t, func(st *wsSettings) { st.PreviewTenantShare = true })

	ck := e.viewerCookie(t, v, slug, 3000)
	if rec := e.get(t, slug, 3000, "/", ck); rec.Code != http.StatusOK {
		t.Fatalf("shared preview for a colleague: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if seen == nil {
		t.Fatal("the Agent never saw the colleague's request")
	}
	// The viewer's cookie carries the viewer's own membership, not the owner's.
	cl, ok := newPreviewHostAPI(e.cfg).verifyClaims(ck.Value)
	if !ok {
		t.Fatal("viewer cookie does not verify")
	}
	if cl.MembershipID != v.membershipID {
		t.Errorf("cookie membership=%q, want the VIEWER's %q", cl.MembershipID, v.membershipID)
	}
}

// Revoking the share closes the next request even for a holder of a still-valid cookie
// (decision 15). Bake the permission into the cookie instead and revoking leaves the
// preview visible for 12 more hours.
func TestPreviewShareRevokeClosesTheNextRequest(t *testing.T) {
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer agent.Close()
	e := newPreviewHostEnv(t, agent.URL)
	v := e.addViewer(t, "colleague")
	slug := e.mintSlug(t)
	e.setPreviewSettings(t, func(st *wsSettings) { st.PreviewTenantShare = true })
	ck := e.viewerCookie(t, v, slug, 3000)
	if rec := e.get(t, slug, 3000, "/", ck); rec.Code != http.StatusOK {
		t.Fatalf("precondition: shared preview should serve, got %d", rec.Code)
	}

	e.setPreviewSettings(t, func(st *wsSettings) { st.PreviewTenantShare = false })

	rec := e.get(t, slug, 3000, "/", ck)
	if rec.Code == http.StatusOK {
		t.Fatal("the colleague's cookie still opened the preview after the share was revoked")
	}
	// The owner still gets in: what was revoked is the share, not their own way in.
	owner := e.viewerCookie(t, previewViewer{userKey: e.mgr.devUser, membershipID: e.ws.MembershipID}, slug, 3000)
	if rec := e.get(t, slug, 3000, "/", owner); rec.Code != http.StatusOK {
		t.Fatalf("owner lost access when the share was revoked: code=%d", rec.Code)
	}
}

// Someone removed from the tenant is shut out even with a live cookie: GetMembershipByID
// only returns active rows.
func TestPreviewShareInactiveMemberIsClosedOut(t *testing.T) {
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer agent.Close()
	e := newPreviewHostEnv(t, agent.URL)
	v := e.addViewer(t, "colleague")
	slug := e.mintSlug(t)
	e.setPreviewSettings(t, func(st *wsSettings) { st.PreviewTenantShare = true })
	ck := e.viewerCookie(t, v, slug, 3000)
	if rec := e.get(t, slug, 3000, "/", ck); rec.Code != http.StatusOK {
		t.Fatalf("precondition: shared preview should serve, got %d", rec.Code)
	}

	if err := e.mgr.store.SetMembershipStatus(context.Background(), v.membershipID, "inactive"); err != nil {
		t.Fatal(err)
	}
	if rec := e.get(t, slug, 3000, "/", ck); rec.Code == http.StatusOK {
		t.Fatal("an offboarded member's cookie still opened the preview")
	}
}

// The share setting survives a start (decision 14) — a deliberate difference from public
// mode, which does not.
func TestPreviewTenantShareSurvivesRestartWhilePublicDoesNot(t *testing.T) {
	e := newPreviewHostEnv(t, "http://127.0.0.1:1")
	e.setPreviewSettings(t, func(st *wsSettings) { st.PreviewTenantShare = true; st.PreviewPublic = true })
	e.mintSlug(t)
	raw, _ := e.mgr.store.GetWorkspaceSettings(context.Background(), e.ws.ID)
	st := parseWSSettings(raw)
	if st.PreviewPublic {
		t.Error("public mode survived a start (fail-closed, decision 12)")
	}
	if !st.PreviewTenantShare {
		t.Error("tenant sharing was reset by a start - its use always spans a restart, so this makes it useless (decision 14)")
	}
}

// The stable redirector re-resolves the slug on every request, because the slug rotates on
// every start (decision 17).
func TestPreviewOpenFollowsTheCurrentSlug(t *testing.T) {
	e := newPreviewHostEnv(t, "http://127.0.0.1:1")
	v := e.addViewer(t, "colleague")
	e.setPreviewSettings(t, func(st *wsSettings) { st.PreviewTenantShare = true })
	slug1 := e.mintSlug(t)

	open := "https://af.example.com" + previewOpenPath + "?owner=" + url.QueryEscape("preview-user") + "&port=3000"
	e.asUser(v.userKey, func() {
		rec := e.console(t, open)
		if rec.Code != http.StatusFound {
			t.Fatalf("/preview-open: code=%d body=%s", rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Location"); !strings.Contains(got, "slug="+slug1) {
			t.Fatalf("Location=%q, want the handshake for the current slug %q", got, slug1)
		}
	})

	// A restart rotates the slug, and the same link has to point at the new one.
	slug2 := e.mintSlug(t)
	if slug1 == slug2 {
		t.Fatal("precondition: the slug should have rotated")
	}
	e.asUser(v.userKey, func() {
		rec := e.console(t, open)
		if got := rec.Header().Get("Location"); !strings.Contains(got, "slug="+slug2) {
			t.Fatalf("after a restart Location=%q, want the NEW slug %q", got, slug2)
		}
	})
}

// 404 for someone the preview is not shared with; "not started" for a stopped workspace.
// The second answer may only be distinguished once the caller is established as someone
// allowed to see it (docs/log/81 §14.6).
func TestPreviewOpenRefusals(t *testing.T) {
	e := newPreviewHostEnv(t, "http://127.0.0.1:1")
	v := e.addViewer(t, "colleague")
	e.mintSlug(t)
	open := "https://af.example.com" + previewOpenPath + "?owner=preview-user&port=3000"

	e.asUser(v.userKey, func() {
		if rec := e.console(t, open); rec.Code != http.StatusNotFound {
			t.Fatalf("not shared: code=%d, want 404", rec.Code)
		}
	})
	e.setPreviewSettings(t, func(st *wsSettings) { st.PreviewTenantShare = true })
	e.asUser(v.userKey, func() {
		if rec := e.console(t, "https://af.example.com"+previewOpenPath+"?owner=preview-user&port=9999"); rec.Code != http.StatusNotFound {
			t.Fatalf("port outside the allowlist: code=%d, want 404", rec.Code)
		}
	})
	// Stopping expires the slug. This path must never trigger a start (decision 16).
	if err := e.mgr.store.SetWorkspacePreviewSlug(context.Background(), e.ws.ID, ""); err != nil {
		t.Fatal(err)
	}
	e.asUser(v.userKey, func() {
		if rec := e.console(t, open); rec.Code != http.StatusConflict {
			t.Fatalf("stopped workspace: code=%d, want 409 (not a start)", rec.Code)
		}
	})
}

// The list holds only other people who opted in — never yourself, never anyone who has not
// shared.
func TestSharedPreviewListShowsOnlyOptedInOthers(t *testing.T) {
	e := newPreviewHostEnv(t, "http://127.0.0.1:1")
	v := e.addViewer(t, "colleague")
	slug := e.mintSlug(t)

	read := func() []map[string]any {
		t.Helper()
		var body struct {
			Domain string           `json:"domain"`
			Items  []map[string]any `json:"items"`
		}
		var rec *httptest.ResponseRecorder
		e.asUser(v.userKey, func() { rec = e.console(t, "https://af.example.com/api/preview/shared") })
		if rec.Code != http.StatusOK {
			t.Fatalf("/api/preview/shared: code=%d body=%s", rec.Code, rec.Body.String())
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v (%s)", err, rec.Body.String())
		}
		return body.Items
	}

	if got := read(); len(got) != 0 {
		t.Fatalf("nothing is shared yet, got %v", got)
	}
	e.setPreviewSettings(t, func(st *wsSettings) { st.PreviewTenantShare = true })
	items := read()
	if len(items) != 1 {
		t.Fatalf("want exactly the owner's entry, got %v", items)
	}
	if items[0]["ownerUserKey"] != "preview-user" {
		t.Errorf("ownerUserKey=%v, want preview-user", items[0]["ownerUserKey"])
	}
	if items[0]["running"] != true {
		t.Errorf("running=%v, want true while a slug is issued", items[0]["running"])
	}
	urls, _ := items[0]["urls"].(map[string]any)
	if urls["3000"] != previewURLFor(slug, 3000, e.domain) {
		t.Errorf("urls[3000]=%v, want the current preview URL", urls["3000"])
	}

	// A stop keeps the row but drops the URLs: never show a URL that is not issued.
	if err := e.mgr.store.SetWorkspacePreviewSlug(context.Background(), e.ws.ID, ""); err != nil {
		t.Fatal(err)
	}
	items = read()
	if len(items) != 1 || items[0]["running"] != false {
		t.Fatalf("a stopped workspace should still be listed as not running, got %v", items)
	}
	if urls, _ := items[0]["urls"].(map[string]any); len(urls) != 0 {
		t.Errorf("urls=%v, want none while the workspace is stopped", urls)
	}
}
