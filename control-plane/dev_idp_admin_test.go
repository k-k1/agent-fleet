package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// setDevIdPDeployment flips the deployment switch the way the admin API does: store, then
// invalidate. Tests that need the other replica's view write the setting directly instead.
func setDevIdPDeployment(t *testing.T, m *manager, on bool) {
	t.Helper()
	v := "off"
	if on {
		v = "on"
	}
	if err := m.store.SetSetting(context.Background(), devIdPSettingKey, v); err != nil {
		t.Fatal(err)
	}
	m.devIdPSw.invalidate()
}

func devIdPAdminPut(t *testing.T, a devIdPAdminAPI, body string) (*httptest.ResponseRecorder, devIdPAdminStatus) {
	t.Helper()
	rec := httptest.NewRecorder()
	a.put(rec, httptest.NewRequest(http.MethodPut, "/api/admin/dev-idp", strings.NewReader(body)), store.Identity{ID: "u1"})
	var s devIdPAdminStatus
	_ = json.Unmarshal(rec.Body.Bytes(), &s)
	return rec, s
}

func discoveryCode(d *devIdPTestEnv, t *testing.T) int {
	t.Helper()
	return d.console(t, "https://af.example.com"+devIdPDiscoveryPath).Code
}

// A deployment with PUBLIC_BASE_URL, no AF_DEV_IDP and no stored row serves the IdP: the
// whole point of the change. newDevIdPTestEnv sets neither.
func TestDevIdPIsServedOnAFreshDeployment(t *testing.T) {
	t.Parallel()
	d := newDevIdPTestEnv(t)
	if v, _ := d.mgr.store.GetSetting(context.Background(), devIdPSettingKey); v != "" {
		t.Fatalf("fixture stored a setting %q; the test proves nothing", v)
	}
	if d.mgr.devIdPEnvOff {
		t.Fatal("fixture locked the IdP off")
	}
	if got := discoveryCode(d, t); got != http.StatusOK {
		t.Fatalf("fresh deployment discovery = %d, want 200", got)
	}
	a := devIdPAdminAPI{memberAuth{d.mgr}}
	rec := httptest.NewRecorder()
	a.get(rec, httptest.NewRequest(http.MethodGet, "/api/admin/dev-idp", nil), store.Identity{ID: "u1"})
	var s devIdPAdminStatus
	_ = json.Unmarshal(rec.Body.Bytes(), &s)
	if !s.Enabled || !s.Setting || s.Locked || s.MissingBaseURL || s.Clients != 1 || s.Issuer != "https://af.example.com/dev-idp" {
		t.Errorf("status = %+v", s)
	}
}

// Off then on, with no restart: every reader follows, and the workspace keeps its client id
// and secret throughout.
func TestDevIdPToggleIsFollowedEverywhereAndKeepsTheOptIns(t *testing.T) {
	t.Parallel()
	d := newDevIdPTestEnv(t)
	a := devIdPAdminAPI{memberAuth{d.mgr}}
	ctx := context.Background()
	// Warm the cache while on, so a stale answer would show.
	if discoveryCode(d, t) != http.StatusOK {
		t.Fatal("not serving at the start")
	}

	rec, s := devIdPAdminPut(t, a, `{"enabled":false}`)
	if rec.Code != http.StatusOK || s.Enabled || s.Setting {
		t.Fatalf("PUT off = %d %+v", rec.Code, s)
	}
	if s.Clients != 1 {
		t.Errorf("turning it off dropped the opt-in count: %+v", s)
	}
	for _, p := range []string{devIdPDiscoveryPath, devIdPJWKSPath, devIdPAuthorizePath, devIdPTokenPath, devIdPUserinfoPath} {
		if got := d.console(t, "https://af.example.com"+p).Code; got != http.StatusNotFound {
			t.Errorf("%s right after PUT off = %d, want 404", p, got)
		}
	}
	if env := d.mgr.devIdPEnv(ctx, d.ws); len(env) != 0 {
		t.Errorf("env still injected while off: %v", env)
	}
	// ws-settings reports it unavailable, and the removed per-workspace field is ignored.
	wsGet := httptest.NewRecorder()
	d.mux.ServeHTTP(wsGet, httptest.NewRequest(http.MethodGet, "https://af.example.com/api/env/ws-settings", nil))
	if !strings.Contains(wsGet.Body.String(), `"devIdpAvailable":false`) {
		t.Errorf("ws-settings while off: %s", wsGet.Body)
	}
	ign := httptest.NewRecorder()
	d.mux.ServeHTTP(ign, httptest.NewRequest(http.MethodPut, "https://af.example.com/api/env/ws-settings",
		strings.NewReader(`{"devIdp":true}`)))
	if ign.Code != http.StatusOK {
		t.Errorf("PUT devIdp while the deployment is off = %d, want 200 (ignored)", ign.Code)
	}

	// Back on: the registration that existed before is the one in force.
	_, s = devIdPAdminPut(t, a, `{"enabled":true}`)
	if !s.Enabled {
		t.Fatalf("PUT on: %+v", s)
	}
	if got := discoveryCode(d, t); got != http.StatusOK {
		t.Errorf("discovery after PUT on = %d", got)
	}
}

// The off-then-on cycle must not rotate or delete a registration. A separate test from the
// above, with the workspace opted in throughout.
func TestDevIdPSwitchCycleKeepsClientIDAndSecret(t *testing.T) {
	t.Parallel()
	d := newDevIdPTestEnv(t)
	a := devIdPAdminAPI{memberAuth{d.mgr}}
	id0, secret0 := d.registration(t)
	devIdPAdminPut(t, a, `{"enabled":false}`)
	devIdPAdminPut(t, a, `{"enabled":true}`)
	id1, secret1 := d.registration(t)
	if id0 != id1 || secret0 != secret1 {
		t.Fatalf("registration rotated across the switch: %s/%s -> %s/%s", id0, secret0, id1, secret1)
	}
	if n, _ := d.mgr.store.CountDevIdPClients(context.Background()); n != 1 {
		t.Errorf("registrations = %d, want 1", n)
	}
}

// Another CP process flips the switch and cannot invalidate this one's cache. The window is
// devIdPSwitchTTL and not a tick longer, token endpoint included.
func TestDevIdPCacheWindowIsBoundedForARemoteOff(t *testing.T) {
	t.Parallel()
	d := newDevIdPTestEnv(t)
	clock := time.Now()
	d.mgr.devIdPSw.now = func() time.Time { return clock }
	d.mgr.devIdPSw.invalidate()
	if discoveryCode(d, t) != http.StatusOK { // fills the cache at `clock`
		t.Fatal("not serving")
	}
	if err := d.mgr.store.SetSetting(context.Background(), devIdPSettingKey, "off"); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(devIdPSwitchTTL - time.Millisecond)
	if discoveryCode(d, t) != http.StatusOK {
		t.Fatal("cache did not hold inside the window; the cache is not doing anything")
	}
	clock = clock.Add(2 * time.Millisecond)
	if got := discoveryCode(d, t); got != http.StatusNotFound {
		t.Errorf("discovery past the window = %d, want 404", got)
	}
	if got := d.exchange("whatever", "http://localhost:3000/cb", "").Code; got != http.StatusNotFound {
		t.Errorf("token endpoint past the window = %d, want 404", got)
	}
	// And the reverse: an "on" written elsewhere is picked up past the window too.
	if err := d.mgr.store.SetSetting(context.Background(), devIdPSettingKey, "on"); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(devIdPSwitchTTL + time.Millisecond)
	if got := discoveryCode(d, t); got != http.StatusOK {
		t.Errorf("discovery after a remote on = %d, want 200", got)
	}
}

// idpStoreHook injects faults and pauses into the calls the switch and the token endpoint make.
type idpStoreHook struct {
	store.Store
	getSetting    func(ctx context.Context, key string) (string, error)
	insertAudit   func(ctx context.Context, a store.AuditLog) error
	getClientByID func(ctx context.Context, id string) (store.DevIdPClient, bool, error)
}

func (h *idpStoreHook) GetSetting(ctx context.Context, key string) (string, error) {
	if h.getSetting != nil {
		return h.getSetting(ctx, key)
	}
	return h.Store.GetSetting(ctx, key)
}

func (h *idpStoreHook) InsertAudit(ctx context.Context, a store.AuditLog) error {
	if h.insertAudit != nil {
		return h.insertAudit(ctx, a)
	}
	return h.Store.InsertAudit(ctx, a)
}

func (h *idpStoreHook) GetDevIdPClientByID(ctx context.Context, id string) (store.DevIdPClient, bool, error) {
	if h.getClientByID != nil {
		return h.getClientByID(ctx, id)
	}
	return h.Store.GetDevIdPClientByID(ctx, id)
}

func hookStore(d *devIdPTestEnv) (*idpStoreHook, store.Store) {
	real := d.mgr.store
	h := &idpStoreHook{Store: real}
	d.mgr.store = h
	return h, real
}

// A read that began before a local PUT returns its old "on" afterwards. It must neither be
// handed to the caller nor written back over the invalidation.
func TestDevIdPInvalidateBeatsAnInFlightRead(t *testing.T) {
	t.Parallel()
	d := newDevIdPTestEnv(t)
	h, real := hookStore(d)
	d.mgr.devIdPSw.invalidate()
	read, release := make(chan struct{}), make(chan struct{})
	var paused atomic.Bool
	h.getSetting = func(ctx context.Context, key string) (string, error) {
		v, err := real.GetSetting(ctx, key)     // the old value
		if paused.CompareAndSwap(false, true) { // only the first read pauses
			close(read)
			<-release
		}
		return v, err
	}
	res := make(chan bool, 1)
	go func() { on, _ := d.mgr.devIdPStored(context.Background()); res <- on }()
	<-read
	if rec, s := devIdPAdminPut(t, devIdPAdminAPI{memberAuth{d.mgr}}, `{"enabled":false}`); rec.Code != http.StatusOK || s.Enabled {
		t.Fatalf("PUT off = %d %+v", rec.Code, s)
	}
	close(release)
	if on := <-res; on {
		t.Error("the in-flight read returned its stale on")
	}
	if got := discoveryCode(d, t); got != http.StatusNotFound {
		t.Errorf("discovery after the PUT = %d, want 404 (the stale read re-filled the cache)", got)
	}
}

// A read that returns late with an old "on" (another replica flipped meanwhile) is not
// believed for a fresh window: the window runs from when the read started.
func TestDevIdPSlowReadDoesNotExtendAStaleOn(t *testing.T) {
	t.Parallel()
	d := newDevIdPTestEnv(t)
	h, real := hookStore(d)
	clock := time.Now()
	d.mgr.devIdPSw.now = func() time.Time { return clock }
	d.mgr.devIdPSw.invalidate()
	first := true
	h.getSetting = func(ctx context.Context, key string) (string, error) {
		v, err := real.GetSetting(ctx, key)
		if first {
			first = false
			_ = real.SetSetting(ctx, key, "off") // flipped elsewhere while this read was slow
			clock = clock.Add(devIdPSwitchTTL + time.Second)
		}
		return v, err
	}
	if got := discoveryCode(d, t); got != http.StatusNotFound {
		t.Errorf("discovery after a slow stale read = %d, want 404", got)
	}
	clock = clock.Add(devIdPSwitchTTL - time.Millisecond)
	if got := discoveryCode(d, t); got != http.StatusNotFound {
		t.Errorf("a stale on was cached: %d", got)
	}
}

// A switch that fails to read answers off at every gate, never on a guess.
func TestDevIdPFailsClosedWhenTheSwitchCannotBeRead(t *testing.T) {
	t.Parallel()
	d := newDevIdPTestEnv(t)
	h, _ := hookStore(d)
	d.mgr.devIdPSw.invalidate()
	h.getSetting = func(context.Context, string) (string, error) { return "", errors.New("db down") }
	for _, p := range []string{devIdPDiscoveryPath, devIdPJWKSPath, devIdPAuthorizePath, devIdPTokenPath, devIdPUserinfoPath} {
		if got := d.console(t, "https://af.example.com"+p).Code; got != http.StatusNotFound {
			t.Errorf("%s with an unreadable switch = %d, want 404", p, got)
		}
	}
	if iss := d.mgr.devIdPIssuer(context.Background()); iss != "" {
		t.Errorf("issuer = %q", iss)
	}
}

// The deployment goes off after the gate let a valid code in: no token may come out.
func TestDevIdPTokenIsRefusedWhenTheSwitchFlipsMidRequest(t *testing.T) {
	t.Parallel()
	d := newDevIdPTestEnv(t)
	redirect := d.ownHost(3000) + "/api/auth/callback/google"
	verifier, challenge := pkcePair()
	code := codeFrom(t, d.authorizeAs(t, "preview-user", d.authQuery(redirect, challenge)))
	h, real := hookStore(d)
	h.getClientByID = func(ctx context.Context, id string) (store.DevIdPClient, bool, error) {
		c, ok, err := real.GetDevIdPClientByID(ctx, id)
		setDevIdPDeployment(t, d.mgr, false) // flipped between the gate and the signing
		return c, ok, err
	}
	rec := d.exchange(code, redirect, verifier)
	if rec.Code == http.StatusOK || strings.Contains(rec.Body.String(), "access_token") {
		t.Fatalf("token issued after the switch went off: %d %s", rec.Code, rec.Body)
	}
}

// Audit comes first: when it cannot be written nothing changes and the answer is 503.
func TestDevIdPAdminPutIsRefusedWhenTheAuditCannotBeWritten(t *testing.T) {
	t.Parallel()
	d := newDevIdPTestEnv(t)
	h, real := hookStore(d)
	h.insertAudit = func(context.Context, store.AuditLog) error { return errors.New("audit down") }
	rec, _ := devIdPAdminPut(t, devIdPAdminAPI{memberAuth{d.mgr}}, `{"enabled":false}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("PUT with the audit down = %d, want 503", rec.Code)
	}
	if v, _ := real.GetSetting(context.Background(), devIdPSettingKey); v != "" {
		t.Errorf("the setting was written without an audit row: %q", v)
	}
	if got := discoveryCode(d, t); got != http.StatusOK {
		t.Errorf("discovery = %d after a refused PUT", got)
	}
}

// AF_DEV_IDP=0 wins over a stored "on", the API says so, and a PUT cannot change it.
func TestDevIdPEnvLockBeatsTheStoredSetting(t *testing.T) {
	t.Parallel()
	d := newDevIdPTestEnv(t)
	setDevIdPDeployment(t, d.mgr, true)
	d.mgr.devIdPEnvOff = true
	if got := discoveryCode(d, t); got != http.StatusNotFound {
		t.Fatalf("locked deployment serves discovery: %d", got)
	}
	a := devIdPAdminAPI{memberAuth{d.mgr}}
	rec, _ := devIdPAdminPut(t, a, `{"enabled":true}`)
	if rec.Code != http.StatusConflict {
		t.Errorf("PUT while locked = %d, want 409", rec.Code)
	}
	get := httptest.NewRecorder()
	a.get(get, httptest.NewRequest(http.MethodGet, "/api/admin/dev-idp", nil), store.Identity{ID: "u1"})
	var s devIdPAdminStatus
	_ = json.Unmarshal(get.Body.Bytes(), &s)
	if s.Enabled || !s.Locked || !s.Setting {
		t.Errorf("locked status = %+v (setting should still read on)", s)
	}
}

func TestDevIdPStatusNamesAMissingBaseURL(t *testing.T) {
	t.Parallel()
	d := newDevIdPTestEnv(t)
	d.mgr.publicBaseURL = ""
	a := devIdPAdminAPI{memberAuth{d.mgr}}
	rec, s := devIdPAdminPut(t, a, `{"enabled":true}`)
	if rec.Code != http.StatusOK || s.Enabled || !s.MissingBaseURL || !s.Setting || s.Issuer != "" {
		t.Errorf("without PUBLIC_BASE_URL: %d %+v", rec.Code, s)
	}
}

func TestDevIdPAdminPutRejectsAnEmptyBody(t *testing.T) {
	t.Parallel()
	d := newDevIdPTestEnv(t)
	a := devIdPAdminAPI{memberAuth{d.mgr}}
	for _, body := range []string{``, `{}`, `{"enabled":"yes"}`} {
		if rec, _ := devIdPAdminPut(t, a, body); rec.Code != http.StatusBadRequest {
			t.Errorf("PUT %q = %d, want 400", body, rec.Code)
		}
	}
	if v, _ := d.mgr.store.GetSetting(context.Background(), devIdPSettingKey); v != "" {
		t.Errorf("a refused body stored %q", v)
	}
}

// Through the route table: only a super admin reads or writes; a tenant admin and a member
// get 403 and the stored value stays put. Every accepted PUT leaves an audit row.
func TestDevIdPAdminRoutesAreSuperAdminOnly(t *testing.T) {
	t.Parallel()
	st, mgr, _, _, _ := networkFixture(t)
	ctx := context.Background()
	if _, err := st.UpsertIdentity(ctx, "root@acme.co.jp", "root-acme-co-jp", "super_admin"); err != nil {
		t.Fatal(err)
	}
	mgr.publicBaseURL = "https://af.example.com"
	mux := http.NewServeMux()
	registerDevIdPAdminRoutes(mux, config{mgr: mgr})
	call := func(method, email, body string) (int, string) {
		r := httptest.NewRequest(method, "/api/admin/dev-idp", strings.NewReader(body))
		r.Header.Set("X-Forwarded-Email", email)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, r)
		return rec.Code, rec.Body.String()
	}
	for _, email := range []string{"yamada@acme.co.jp", "boss@acme.co.jp"} {
		if code, _ := call("GET", email, ""); code != http.StatusForbidden {
			t.Errorf("GET by %s = %d, want 403", email, code)
		}
		if code, _ := call("PUT", email, `{"enabled":false}`); code != http.StatusForbidden {
			t.Errorf("PUT by %s = %d, want 403", email, code)
		}
	}
	if v, _ := st.GetSetting(ctx, devIdPSettingKey); v != "" {
		t.Fatalf("a refused caller stored %q", v)
	}
	if code, _ := call("PUT", "root@acme.co.jp", `{"enabled":false}`); code != http.StatusOK {
		t.Fatalf("PUT by the super admin = %d - the refusals above prove nothing", code)
	}
	if v, _ := st.GetSetting(ctx, devIdPSettingKey); v != "off" {
		t.Errorf("stored = %q, want off", v)
	}
	if code, body := call("GET", "root@acme.co.jp", ""); code != http.StatusOK || !strings.Contains(body, `"enabled":false`) {
		t.Errorf("GET by the super admin = %d %s", code, body)
	}
	rows, err := st.ListAuditByTenant(ctx, "", 20)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range rows {
		if r.Action == "dev_idp.set" && r.Target == "off" && r.ActorID != "" {
			found = true
		}
	}
	if !found {
		t.Errorf("no dev_idp.set audit row among %+v", rows)
	}
}

// A deployment with no master key (a dev setup) starts workspaces from the memoized runtime. The switch must still be read at every start, in both directions.
func TestStartFollowsTheDevIdPSwitchFromAMemoWithoutKeys(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := &envRecordingFactory{}
	st, mgr, victim, tn := destroyFixture(t, f)
	mgr.publicBaseURL = "https://af.example.com"
	mgr.previewDomain = "pv.example.com" // the IdP is offered only with preview subdomains
	memID := membershipIDOf(t, st, victim, tn)
	ws, _, _ := st.GetWorkspaceByMembership(ctx, memID)
	mv, _, _ := st.GetMembershipByID(ctx, memID)
	api := newWorkspaceAPI(mgr, false)
	started := func() string {
		t.Helper()
		res, aerr := mgr.buildResolved(ctx, victim, mv)
		if aerr != nil {
			t.Fatal(aerr)
		}
		if aerr := api.ensureWorkspaceStarted(ctx, res); aerr != nil {
			t.Fatalf("start: %v", aerr)
		}
		_ = st.SetWorkspaceState(ctx, ws.ID, "stopped")
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.started[len(f.started)-1]
	}
	setDevIdPDeployment(t, mgr, false)
	if got := started(); strings.Contains(got, "AF_DEV_IDP_ISSUER") { // also memoizes the runtime
		t.Fatalf("env injected while the switch is off: %q", got)
	}
	setDevIdPDeployment(t, mgr, true)
	if got := started(); !strings.Contains(got, "AF_DEV_IDP_ISSUER=https://af.example.com/dev-idp") {
		t.Fatalf("switch turned on but the start from the memo has no IdP env: %q", got)
	}
	setDevIdPDeployment(t, mgr, false)
	if got := started(); strings.Contains(got, "AF_DEV_IDP_") {
		t.Fatalf("switch turned off but the start still injects the IdP env: %q", got)
	}
}

// The IdP is offered only with preview subdomains. Without a preview domain nothing is served
// or injected, ws-settings says unavailable, rotate is the 409 no_preview_domain, and the admin
// status says why; with one, everything works.
func TestDevIdPNeedsAPreviewDomain(t *testing.T) {
	t.Parallel()
	d := newDevIdPTestEnv(t)
	a := devIdPAdminAPI{memberAuth{d.mgr}}
	wsGet := func() map[string]any {
		rec := httptest.NewRecorder()
		d.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "https://af.example.com/api/env/ws-settings", nil))
		return idpJSON(t, rec)
	}
	adminGet := func() devIdPAdminStatus {
		rec := httptest.NewRecorder()
		a.get(rec, httptest.NewRequest(http.MethodGet, "/api/admin/dev-idp", nil), store.Identity{ID: "u1"})
		var s devIdPAdminStatus
		_ = json.Unmarshal(rec.Body.Bytes(), &s)
		return s
	}
	if s := adminGet(); s.MissingPreviewDomain || !s.Enabled {
		t.Errorf("admin status with a preview domain = %+v", s)
	}
	if b := wsGet(); b["devIdpAvailable"] != true || b["devIdpClientId"] == nil {
		t.Fatalf("with a preview domain: %v", b)
	}

	domain := d.mgr.previewDomain
	d.mgr.previewDomain = ""
	if s := adminGet(); !s.MissingPreviewDomain || s.MissingBaseURL || s.Enabled || !s.Setting {
		t.Errorf("admin status without a preview domain = %+v", s)
	}
	if b := wsGet(); b["devIdpAvailable"] != false {
		t.Errorf("without a preview domain: %v", b)
	}
	if _, _, ok := envCreds(d); ok {
		t.Error("env injected without a preview domain")
	}
	for _, p := range []string{devIdPDiscoveryPath, devIdPJWKSPath, devIdPAuthorizePath, devIdPTokenPath, devIdPUserinfoPath} {
		if got := d.console(t, "https://af.example.com"+p).Code; got != http.StatusNotFound {
			t.Errorf("%s without a preview domain = %d, want 404", p, got)
		}
	}
	rec := httptest.NewRecorder()
	d.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "https://af.example.com/api/env/ws-settings/dev-idp/rotate", nil))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"no_preview_domain"`) {
		t.Errorf("rotate without a preview domain = %d %s, want 409 no_preview_domain", rec.Code, rec.Body)
	}

	d.mgr.previewDomain = domain
	if _, _, ok := envCreds(d); !ok {
		t.Error("no env once the preview domain is back")
	}
}
