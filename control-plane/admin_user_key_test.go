package main

// A client-supplied user_key becomes the member's home directory name, so the admin API
// never mints an identity from a key the server would not have minted itself.
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

// StopWorkspace, CleanHome, SetUserLimit and SetMembershipRole act on an existing member:
// they look the key up and never mint an identity, so they reach every stored key — the
// longer ones disambiguateUserKey mints and any stored before the check — and an unknown
// key is a 404 that leaves nothing behind.
func TestMemberHandlersLookUpTheStoredUserKey(t *testing.T) {
	ctx := context.Background()
	st, mgr, tn := userKeyFixture(t)
	adm := newAdminAPI(mgr)
	super, _ := st.UpsertIdentity(ctx, "boss@acme.co.jp", "boss-acme-co-jp", "super_admin")
	setRole := func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set("X-Forwarded-Email", "boss@acme.co.jp")
		adm.setMembershipRole(w, r, super)
	}
	long := strings.Repeat("a", 40)
	if _, err := st.UpsertIdentity(ctx, long+"1@example.com", long, ""); err != nil {
		t.Fatal(err)
	}
	hashed, err := st.UpsertIdentity(ctx, long+"2@example.com", long, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(hashed.UserKey) <= 40 {
		t.Fatalf("colliding email got key %q, want a disambiguated one past 40 characters", hashed.UserKey)
	}
	legacy, _ := st.UpsertIdentity(ctx, "", "Legacy", "")
	for _, id := range []store.Identity{hashed, legacy} {
		if _, err := st.EnsureMembership(ctx, id.ID, tn.ID, "member"); err != nil {
			t.Fatal(err)
		}
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
		body := func(key string) string { return `{"tenant_slug":"sales","user_key":` + strconv.Quote(key) + `}` }
		if w := callAdmin(h.fn, h.method, h.path, body("")); w.Code != http.StatusBadRequest {
			t.Errorf("%s empty user_key = %d %s, want 400", h.name, w.Code, w.Body.String())
		}
		for _, key := range append(badUserKeys, "nobody-here") {
			w := callAdmin(h.fn, h.method, h.path, body(key))
			if w.Code != http.StatusNotFound || apiErrCode(t, w) != "no_membership" {
				t.Errorf("%s unknown user_key %q = %d %s, want 404 no_membership", h.name, key, w.Code, w.Body.String())
			}
			if _, ok, _ := st.GetIdentityByUserKey(ctx, key); ok {
				t.Errorf("%s: unknown user_key %q left an identity behind", h.name, key)
			}
		}
		for _, key := range []string{hashed.UserKey, "Legacy"} {
			// Stop and clean-home go on to the runtime, which this fixture lacks; past the
			// key is all that is asserted for them.
			w := callAdmin(h.fn, h.method, h.path, body(key))
			if w.Code == http.StatusBadRequest || w.Code == http.StatusNotFound {
				t.Errorf("%s stored user_key %q = %d %s, want it resolved", h.name, key, w.Code, w.Body.String())
			}
		}
	}
	if w := callAdmin(adm.setUserLimit, http.MethodPut, "/api/admin/user-limit",
		`{"tenant_slug":"sales","user_key":`+strconv.Quote(hashed.UserKey)+`,"max_sessions":3}`); w.Code != http.StatusOK {
		t.Errorf("user-limit for a disambiguated key = %d %s, want 200", w.Code, w.Body.String())
	}
}

// AddMembership accepts the server-minted long key anywhere, and a key stored before the
// check only where that person is already a member: a re-invite makes no new home.
func TestAddMembershipAcceptsStoredUserKeys(t *testing.T) {
	ctx := context.Background()
	st, mgr, tn := userKeyFixture(t)
	adm := newAdminAPI(mgr)
	if _, err := st.CreateTenant(ctx, "ops", "Ops"); err != nil {
		t.Fatal(err)
	}
	head, _, _ := st.GetIdentityByUserKey(ctx, "head-acme-co-jp")
	opsT, _, _ := st.GetTenantBySlug(ctx, "ops")
	if _, err := st.EnsureMembership(ctx, head.ID, opsT.ID, "tenant_admin"); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("a", 40)
	_, _ = st.UpsertIdentity(ctx, long+"1@example.com", long, "")
	hashed, _ := st.UpsertIdentity(ctx, long+"2@example.com", long, "")
	legacy, _ := st.UpsertIdentity(ctx, "", "Legacy", "")
	if _, err := st.EnsureMembership(ctx, legacy.ID, tn.ID, "member"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		slug, key string
		status    int
	}{
		{"ops", hashed.UserKey, http.StatusOK},
		{"sales", "Legacy", http.StatusOK},
		{"ops", "Legacy", http.StatusBadRequest},
		// The disambiguated shape on an unsanitized prefix is not a server-minted key.
		{"ops", "A/b-0123abcd", http.StatusBadRequest},
		{"ops", "ab-0123ABCD", http.StatusBadRequest},
	} {
		w := callAdmin(adm.addMembership, http.MethodPost, "/api/admin/memberships",
			`{"tenant_slug":"`+c.slug+`","user_key":`+strconv.Quote(c.key)+`}`)
		if w.Code != c.status {
			t.Errorf("add %q to %s = %d %s, want %d", c.key, c.slug, w.Code, w.Body.String(), c.status)
		}
	}
}

// RemoveMembership was already a lookup; a stored key outside the minted forms stays
// removable through it.
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

// The re-invite exemption must reuse the stored identity: a different address would make
// UpsertIdentity mint "<key>-<hash>", a new unsafe key with a new membership and home.
func TestReinviteOfAnUnsafeKeyReusesTheStoredMembership(t *testing.T) {
	ctx := context.Background()
	st, mgr, tn := userKeyFixture(t)
	adm := newAdminAPI(mgr)
	legacy, err := st.UpsertIdentity(ctx, "legacy@example.com", "../escape", "")
	if err != nil {
		t.Fatal(err)
	}
	mem, err := st.EnsureMembership(ctx, legacy.ID, tn.ID, "member")
	if err != nil {
		t.Fatal(err)
	}
	countRows := func() (idents, mems int) {
		_ = st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM identity`).Scan(&idents)
		_ = st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM membership`).Scan(&mems)
		return
	}
	idents0, mems0 := countRows()
	add := func(body string) *httptest.ResponseRecorder {
		return callAdmin(adm.addMembership, http.MethodPost, "/api/admin/memberships", body)
	}

	if err := st.SetMembershipStatus(ctx, mem.ID, "inactive"); err != nil {
		t.Fatal(err)
	}
	w := add(`{"tenant_slug":"sales","user_key":"../escape","email":"different@example.com"}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("re-invite with another email = %d %s, want 400", w.Code, w.Body.String())
	}
	if i, m := countRows(); i != idents0 || m != mems0 {
		t.Errorf("refused re-invite left rows behind: identities %d→%d, memberships %d→%d", idents0, i, mems0, m)
	}

	for _, body := range []string{
		`{"tenant_slug":"sales","user_key":"../escape"}`,
		`{"tenant_slug":"sales","user_key":"../escape","email":"Legacy@Example.com"}`,
	} {
		if err := st.SetMembershipStatus(ctx, mem.ID, "inactive"); err != nil {
			t.Fatal(err)
		}
		w := add(body)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"user_key":"../escape"`) {
			t.Errorf("re-invite %s = %d %s, want 200 on the stored key", body, w.Code, w.Body.String())
		}
		got, ok, _ := st.GetMembership(ctx, legacy.ID, tn.ID)
		if !ok || got.ID != mem.ID || got.Status != "active" {
			t.Errorf("re-invite %s: membership = %+v, want %s reactivated", body, got, mem.ID)
		}
		if i, m := countRows(); i != idents0 || m != mems0 {
			t.Errorf("re-invite %s added rows: identities %d→%d, memberships %d→%d", body, idents0, i, mems0, m)
		}
	}
}
