package main

// A client-supplied user_key becomes the member's home directory name, so every admin
// handler that can mint an identity from one refuses a key sanitizeUser would change.
// The caller here is a plain tenant_admin: the person the check exists to stop.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

func userKeyFixture(t *testing.T) (*store.SQL, *manager, store.Tenant) {
	t.Helper()
	ctx := context.Background()
	st := p3Store(t)
	mgr := p3Manager(t, st)
	tn, err := st.CreateTenant(ctx, "sales", "Sales")
	if err != nil {
		t.Fatal(err)
	}
	head, _ := st.UpsertIdentity(ctx, "head@acme.co.jp", "head-acme-co-jp", "")
	if _, err := st.EnsureMembership(ctx, head.ID, tn.ID, "tenant_admin"); err != nil {
		t.Fatal(err)
	}
	return st, mgr, tn
}

func callAdmin(h func(http.ResponseWriter, *http.Request), method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("X-Forwarded-Email", "head@acme.co.jp")
	w := httptest.NewRecorder()
	h(w, r)
	return w
}

var badUserKeys = []string{"..", "../escape", "a/b", "Upper", " padded ", "a--b", "-lead"}

func TestAddMembershipRefusesAnUnsanitizedUserKey(t *testing.T) {
	ctx := context.Background()
	st, mgr, _ := userKeyFixture(t)
	adm := newAdminAPI(mgr)
	add := func(body string) *httptest.ResponseRecorder {
		return callAdmin(adm.addMembership, http.MethodPost, "/api/admin/memberships", body)
	}
	for _, key := range badUserKeys {
		w := add(`{"tenant_slug":"sales","user_key":` + strconv.Quote(key) + `}`)
		if w.Code != http.StatusBadRequest || apiErrCode(t, w) != "bad_request" ||
			!strings.Contains(w.Body.String(), "invalid user_key") {
			t.Errorf("add user_key %q = %d %s, want 400 invalid user_key", key, w.Code, w.Body.String())
		}
		if _, ok, _ := st.GetIdentityByUserKey(ctx, key); ok {
			t.Errorf("refused user_key %q still left an identity behind", key)
		}
	}
	for _, c := range []struct {
		body, key string
		status    int
	}{
		{`{"tenant_slug":"sales","user_key":""}`, "", http.StatusBadRequest}, // nothing to derive a key from
		{`{"tenant_slug":"sales","user_key":"alice-2"}`, "alice-2", http.StatusOK},
		// The email path sanitizes on the server, so a mixed-case address stays accepted.
		{`{"tenant_slug":"sales","email":"Bob.Smith@Example.com"}`, "bob-smith-example-com", http.StatusOK},
	} {
		w := add(c.body)
		if w.Code != c.status {
			t.Errorf("add %s = %d %s, want %d", c.body, w.Code, w.Body.String(), c.status)
			continue
		}
		if c.status == http.StatusOK {
			if _, ok, _ := st.GetIdentityByUserKey(ctx, c.key); !ok {
				t.Errorf("add %s: no identity %q", c.body, c.key)
			}
		}
	}
}

// StopWorkspace, CleanHome, SetUserLimit and SetMembershipRole only act on an existing
// member, but resolve the key through UpsertIdentity, which mints the identity on the way.
func TestMemberHandlersRefuseAnUnsanitizedUserKey(t *testing.T) {
	ctx := context.Background()
	st, mgr, _ := userKeyFixture(t)
	adm := newAdminAPI(mgr)
	super, _ := st.UpsertIdentity(ctx, "boss@acme.co.jp", "boss-acme-co-jp", "super_admin")
	setRole := func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set("X-Forwarded-Email", "boss@acme.co.jp")
		adm.setMembershipRole(w, r, super)
	}
	for _, h := range []struct {
		name, method, path string
		fn                 func(http.ResponseWriter, *http.Request)
	}{
		{"stop-workspace", http.MethodPost, "/api/admin/stop-workspace", adm.stopWorkspace},
		{"clean-home", http.MethodPost, "/api/admin/clean-home", adm.cleanHome},
		{"user-limit", http.MethodPut, "/api/admin/user-limit", adm.setUserLimit},
		{"membership-role", http.MethodPut, "/api/admin/membership-role", setRole},
	} {
		for _, key := range append(badUserKeys, "") {
			w := callAdmin(h.fn, h.method, h.path, `{"tenant_slug":"sales","user_key":`+strconv.Quote(key)+`}`)
			if w.Code != http.StatusBadRequest || apiErrCode(t, w) != "bad_request" {
				t.Errorf("%s user_key %q = %d %s, want 400 bad_request", h.name, key, w.Code, w.Body.String())
			}
			if _, ok, _ := st.GetIdentityByUserKey(ctx, key); ok {
				t.Errorf("%s: refused user_key %q still left an identity behind", h.name, key)
			}
		}
		// A well-formed key gets past the check to the membership lookup.
		w := callAdmin(h.fn, h.method, h.path, `{"tenant_slug":"sales","user_key":"nobody-here"}`)
		if w.Code != http.StatusNotFound || apiErrCode(t, w) != "no_membership" {
			t.Errorf("%s valid non-member = %d %s, want 404 no_membership", h.name, w.Code, w.Body.String())
		}
	}

	// A real member with a valid key still goes through.
	carol, _ := st.UpsertIdentity(ctx, "carol@acme.co.jp", "carol-acme-co-jp", "")
	tn, _, _ := st.GetTenantBySlug(ctx, "sales")
	if _, err := st.EnsureMembership(ctx, carol.ID, tn.ID, "member"); err != nil {
		t.Fatal(err)
	}
	if w := callAdmin(adm.setUserLimit, http.MethodPut, "/api/admin/user-limit",
		`{"tenant_slug":"sales","user_key":"carol-acme-co-jp","max_sessions":3}`); w.Code != http.StatusOK {
		t.Errorf("user-limit for a valid member = %d %s", w.Code, w.Body.String())
	}
}

// A key stored before the check (only this bug could have stored one) stays removable:
// RemoveMembership looks the identity up and never mints one, so it is not checked.
func TestRemoveMembershipStillReachesALegacyUnsanitizedKey(t *testing.T) {
	ctx := context.Background()
	st, mgr, tn := userKeyFixture(t)
	legacy, err := st.UpsertIdentity(ctx, "", "Legacy", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnsureMembership(ctx, legacy.ID, tn.ID, "member"); err != nil {
		t.Fatal(err)
	}
	w := callAdmin(newAdminAPI(mgr).removeMembership, http.MethodDelete, "/api/admin/memberships",
		`{"tenant_slug":"sales","user_key":"Legacy"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("remove legacy key = %d %s, want 200", w.Code, w.Body.String())
	}
}
