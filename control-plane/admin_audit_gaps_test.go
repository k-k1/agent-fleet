package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Issue #3500: admin writes that left no audit row, and PAT revocations that were invisible to
// the tenant that issued the token.

func auditRowsOf(t *testing.T, st store.Store, tenantID, action string) []store.AuditLog {
	t.Helper()
	logs, err := st.ListAuditByTenant(context.Background(), tenantID, 200)
	if err != nil {
		t.Fatal(err)
	}
	var out []store.AuditLog
	for _, l := range logs {
		if l.Action == action {
			out = append(out, l)
		}
	}
	return out
}

// Each admin write below must leave at least one row, under the tenant it concerns, when it
// succeeds. The static test further down is what forces a new route onto a list.
func TestAdminWritesLeaveAnAuditRow(t *testing.T) {
	ctx := context.Background()
	boss := func(st *store.SQL) store.Identity {
		id, _, _ := st.GetIdentityByUserKey(ctx, "boss-acme-co-jp")
		return id
	}
	cases := []struct {
		name   string
		action string
		detail string // substring of the outcome row's detail
		// run performs the call and returns the tenant the row belongs under.
		run func(t *testing.T, st *store.SQL, mgr *manager, tn store.Tenant) (*httptest.ResponseRecorder, string)
	}{
		{"set role", "membership.role", "from=member to=tenant_admin", func(t *testing.T, st *store.SQL, mgr *manager, tn store.Tenant) (*httptest.ResponseRecorder, string) {
			w := httptest.NewRecorder()
			newAdminAPI(mgr).setMembershipRole(w, adminRequest(http.MethodPut, "/api/admin/membership-role",
				`{"tenant_slug":"sales","user_key":"leaver-acme-co-jp","role":"tenant_admin"}`), boss(st))
			return w, tn.ID
		}},
		{"create tenant", "tenant.create", "name=Ops", func(t *testing.T, st *store.SQL, mgr *manager, tn store.Tenant) (*httptest.ResponseRecorder, string) {
			w := httptest.NewRecorder()
			newAdminAPI(mgr).createTenant(w, adminRequest(http.MethodPost, "/api/admin/tenants", `{"slug":"ops","name":"Ops"}`), boss(st))
			created, _, _ := st.GetTenantBySlug(ctx, "ops")
			return w, created.ID
		}},
		{"set user limit", "member.limits", "max_sessions=3", func(t *testing.T, st *store.SQL, mgr *manager, tn store.Tenant) (*httptest.ResponseRecorder, string) {
			w := httptest.NewRecorder()
			newAdminAPI(mgr).setUserLimit(w, adminRequest(http.MethodPut, "/api/admin/user-limits",
				`{"tenant_slug":"sales","user_key":"leaver-acme-co-jp","max_sessions":3,"disk_gb":10}`))
			return w, tn.ID
		}},
		{"stop workspace", "workspace.stop", "", func(t *testing.T, st *store.SQL, mgr *manager, tn store.Tenant) (*httptest.ResponseRecorder, string) {
			w := httptest.NewRecorder()
			newAdminAPI(mgr).stopWorkspace(w, adminRequest(http.MethodPost, "/api/admin/stop-workspace",
				`{"tenant_slug":"sales","user_key":"leaver-acme-co-jp"}`))
			return w, tn.ID
		}},
		{"set slot class", "tenant.slot_class", "from= to=", func(t *testing.T, st *store.SQL, mgr *manager, tn store.Tenant) (*httptest.ResponseRecorder, string) {
			w := httptest.NewRecorder()
			r := adminRequest(http.MethodPut, "/api/admin/tenants/sales/slot-class", `{"slot_class":""}`)
			r.SetPathValue("slug", "sales")
			newAdminAPI(mgr).setTenantSlotClass(w, r)
			return w, tn.ID
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st, mgr, _, tn := destroyFixture(t, fixedRuntimeFactory{&reachableHomeRuntime{unreachableHomeRuntime: unreachableHomeRuntime{rec: &wipeRecorder{}, state: "stopped"}}})
			w, tenantID := c.run(t, st, mgr, tn)
			if w.Code != http.StatusOK {
				t.Fatalf("%s = %d %s, want 200", c.name, w.Code, w.Body.String())
			}
			rows := auditRowsOf(t, st, tenantID, c.action)
			if len(rows) == 0 {
				t.Fatalf("%s left no %q row under the tenant", c.name, c.action)
			}
			if c.detail != "" && !strings.Contains(rows[0].Detail, c.detail) {
				t.Errorf("detail = %q, want it to contain %q", rows[0].Detail, c.detail)
			}
			if rows[0].ActorID == "" {
				t.Errorf("row has no actor: %+v", rows[0])
			}
		})
	}
}

// A role change grants or removes privilege, so a request that cannot be recorded is refused
// and nothing changes.
func TestSetMembershipRoleRefusedWhenTheAuditRecordCannotBeWritten(t *testing.T) {
	ctx := context.Background()
	st, mgr, victim, tn := destroyFixture(t, fixedRuntimeFactory{&reachableHomeRuntime{unreachableHomeRuntime: unreachableHomeRuntime{rec: &wipeRecorder{}, state: "stopped"}}})
	boss, _, _ := st.GetIdentityByUserKey(ctx, "boss-acme-co-jp")
	mgr.store = auditFailingStore{st, failEveryAudit}
	w := httptest.NewRecorder()
	newAdminAPI(mgr).setMembershipRole(w, adminRequest(http.MethodPut, "/api/admin/membership-role",
		`{"tenant_slug":"sales","user_key":"leaver-acme-co-jp","role":"tenant_admin"}`), boss)
	wantAuditUnavailable(t, "role change", w)
	mem, _, _ := st.GetMembership(ctx, victim.ID, tn.ID)
	if mem.Role != "member" {
		t.Errorf("role = %q after the refusal, want member", mem.Role)
	}
}

// pat.revoke must sit under the membership's tenant (where pat.create put it), or the tenant's
// administrators see the issuance and never the revocation.
func TestPATRevokeIsVisibleToTheIssuingTenant(t *testing.T) {
	e := newMCPOAuthEnv(t)
	ctx := context.Background()
	_, h := newPATToken()
	if err := e.mgr.store.CreatePAT(ctx, store.PAT{ID: "p1", IdentityID: e.ident.ID, MembershipID: e.membershipID(),
		Scope: "read", Name: "p1", CreatedAt: store.NowTS()}, h); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodDelete, "/api/pat/p1", nil)
	r.SetPathValue("id", "p1")
	w := httptest.NewRecorder()
	newPATAPI(e.mgr).revoke(w, r, e.ident)
	if w.Code != http.StatusOK {
		t.Fatalf("revoke = %d %s", w.Code, w.Body.String())
	}
	if rows := auditRowsOf(t, e.mgr.store, e.tenA.ID, "pat.revoke"); len(rows) != 1 || rows[0].Target != "p1" {
		t.Errorf("pat.revoke rows under the tenant = %+v, want one for p1", rows)
	}
}

// The automatic revocation writes one system row per credential that was live, under that
// credential's tenant; a credential already revoked adds nothing.
func TestRevokeBearerSetAuditsEachRevokedCredential(t *testing.T) {
	e := newMCPOAuthEnv(t)
	ctx := context.Background()
	for _, id := range []string{"p-live", "p-dead"} {
		_, h := newPATToken()
		if err := e.mgr.store.CreatePAT(ctx, store.PAT{ID: id, IdentityID: e.ident.ID, MembershipID: e.membershipID(),
			Scope: "read", Name: id, CreatedAt: store.NowTS()}, h); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().Unix()
	if err := e.mgr.store.CreateMCPOAuthGrant(ctx, store.MCPOAuthGrant{ID: "g1", ClientID: "c", IdentityID: e.ident.ID,
		MembershipID: e.membershipID(), TenantID: e.tenA.ID, Scope: "read", Resource: "r", CreatedAt: now,
		IdleExpiresAt: now + 100, AbsExpiresAt: now + 100}); err != nil {
		t.Fatal(err)
	}
	set, err := e.mgr.store.LiveBearersOfIdentity(ctx, e.ident.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.mgr.store.RevokePAT(ctx, "p-dead", e.ident.ID); err != nil { // revoked between snapshot and sweep
		t.Fatal(err)
	}
	if err := e.mgr.store.RevokeBearerSet(ctx, e.ident.ID, "no longer allowed to sign in", set); err != nil {
		t.Fatal(err)
	}
	pats := auditRowsOf(t, e.mgr.store, e.tenA.ID, "pat.revoke")
	grants := auditRowsOf(t, e.mgr.store, e.tenA.ID, "mcp.oauth.grant")
	if len(pats) != 1 || pats[0].Target != "p-live" || len(grants) != 1 || grants[0].Target != "g1" {
		t.Fatalf("rows: pat=%+v grant=%+v, want one each for p-live and g1", pats, grants)
	}
	for _, l := range append(pats, grants...) {
		if l.ActorKind != "system" || !strings.Contains(l.Detail, "no longer allowed to sign in") {
			t.Errorf("row %+v: want actor kind system and the reason as detail", l)
		}
	}
}

// adminWriteRouteAudit says, for every POST / PUT / PATCH / DELETE route registered under
// /api/admin/, which audit record its handler leaves. marker is a fragment of a string literal
// that must appear in the handler's code (the handler and what it calls, found through the AST),
// and the handler must reach an audit call; a comment or an unrelated helper cannot satisfy it.
// exempt is the reason a route legitimately writes no row. TestAdminWriteRoutesAreClassified
// fails for a route that is registered and missing here (a new handler cannot skip the
// question) and for an entry whose route is gone.
type adminWriteRouteAudit struct {
	marker string
	exempt string
}

const (
	exReadOnly = "a POST that only reads or searches; it changes nothing"
)

var adminWriteRouteAudits = map[string]adminWriteRouteAudit{
	"POST /api/admin/tenants":                                     {marker: `tenant.create`},
	"DELETE /api/admin/tenants/{slug}":                            {marker: `tenant.delete`},
	"DELETE /api/admin/tenants/{slug}/members/{key}":              {marker: `membership.delete`},
	"POST /api/admin/memberships":                                 {marker: `membership.add`},
	"DELETE /api/admin/memberships":                               {marker: `membership.remove`},
	"DELETE /api/admin/workspaces":                                {marker: `workspace.destroy`},
	"POST /api/admin/stop-workspace":                              {marker: `workspace.stop`},
	"POST /api/admin/clean-home":                                  {marker: `workspace.clean_home`},
	"POST /api/admin/rotate-git-token":                            {marker: `membership.rotate_git_token`},
	"POST /api/admin/rotate-bridge-tokens":                        {marker: `membership.rotate_bridge_tokens`},
	"DELETE /api/admin/tenants/{slug}/members/{key}/home-backups": {marker: `workspace.delete_backups`},
	"PUT /api/admin/tenants/{slug}/limits":                        {marker: `tenant.limits`},
	"PUT /api/admin/tenants/{slug}/login":                         {marker: `tenant.login_rules`},
	"PUT /api/admin/tenants/{slug}/network":                       {marker: `tenant.network_rules`},
	"PUT /api/admin/tenants/{slug}/mcp-oauth":                     {marker: `tenant.mcp_oauth`},
	"PUT /api/admin/tenants/{slug}/external-memory":               {marker: `tenant.external_memory`},
	"PUT /api/admin/tenants/{slug}/slot-class":                    {marker: `tenant.slot_class`},
	"PUT /api/admin/tenants/{slug}/engine-access":                 {marker: `tenant.engine_access`},
	"PUT /api/admin/tenants/{slug}/engine-access/members":         {marker: `member.engine_access`},
	"PUT /api/admin/tenants/{slug}/branch-rules":                  {marker: `tenant.branch_rules`},
	"PUT /api/admin/tenants/{slug}/member-defaults":               {marker: `tenant.member_defaults`},
	"PUT /api/admin/tenants/{slug}/limit-board":                   {marker: `limit_board.set`},
	"POST /api/admin/tenants/{slug}/idp":                          {marker: `tenant_idp.`},
	"PUT /api/admin/tenants/{slug}/idp/{id}":                      {marker: `tenant_idp.`},
	"DELETE /api/admin/tenants/{slug}/idp/{id}":                   {marker: `tenant_idp.delete`},
	"POST /api/admin/tenants/{slug}/idp/{id}/status":              {marker: `tenant_idp.`},
	"PUT /api/admin/tenants/{slug}/git-oauth/{provider}":          {marker: `tenant.git_oauth_save`},
	"DELETE /api/admin/tenants/{slug}/git-oauth/{provider}":       {marker: `tenant.git_oauth_delete`},
	"PUT /api/admin/user-limits":                                  {marker: `member.limits`},
	"PUT /api/admin/membership-role":                              {marker: `membership.role`},
	"DELETE /api/admin/ec2-pool/slots/{id}":                       {marker: `pool.slot_terminate`},
	"PUT /api/admin/ec2-pool/slots/{id}/replace":                  {marker: `pool.slot_replace_reserve`},
	"DELETE /api/admin/ec2-pool/slots/{id}/replace":               {marker: `pool.slot_replace_reserve`},
	"POST /api/admin/ec2-pool/slots/{id}/force-stop":              {marker: `pool.slot_force_stop`},
	"POST /api/admin/ec2-pool/reserve-outdated":                   {marker: `pool.slot_replace_reserve`},
	"POST /api/admin/egress/allowlist":                            {marker: `egress.allow.add`},
	"POST /api/admin/egress/allowlist/{id}/state":                 {marker: `egress.allow.`},
	"PUT /api/admin/egress/mode":                                  {marker: `egress.mode`},
	"POST /api/admin/mcp-servers":                                 {marker: `mcp.upsert`},
	"PUT /api/admin/mcp-servers/{id}":                             {marker: `mcp.upsert`},
	"DELETE /api/admin/mcp-servers/{id}":                          {marker: `mcp.delete`},
	"PUT /api/admin/dev-idp":                                      {marker: `dev_idp.set`},
	"PUT /api/admin/brand":                                        {marker: `brand.set`},
	"DELETE /api/admin/brand":                                     {marker: `brand.reset`},
	"PUT /api/admin/tts":                                          {marker: `tts.engine`},
	"PUT /api/admin/tts/dict":                                     {marker: `tts.dict`},
	"PUT /api/admin/engines/{key}":                                {marker: `engine.`},
	"PUT /api/admin/engines/{key}/idle":                           {marker: `.idle`},
	"PUT /api/admin/engines/{key}/class":                          {marker: `.class`},
	"PUT /api/admin/engines/{key}/spot":                           {marker: `.spot`},
	"PUT /api/admin/engines/{key}/negative":                       {marker: `.negative`},
	"POST /api/admin/engines/{key}/replace-box":                   {marker: `.replace_box`},
	"PUT /api/admin/engines/{key}/models/{id}":                    {marker: `.model`},
	"POST /api/admin/engines/{key}/models":                        {marker: `.model`},
	"DELETE /api/admin/engines/{key}/models/{id}":                 {marker: `.model`},
	"POST /api/admin/engines/{key}/models/{id}/meta":              {marker: `meta `},
	"POST /api/admin/engines/{key}/objects/register":              {marker: `from the bucket`},
	"DELETE /api/admin/engines/{key}/objects":                     {marker: `.object`},
	"POST /api/admin/engines/{key}/ingest":                        {marker: `.ingest`},
	"DELETE /api/admin/engines/{key}/ingest/{id}":                 {marker: `.ingest`},
	"PUT /api/admin/engines/hf-token":                             {marker: `engine.hf_token`},
	"DELETE /api/admin/engines/hf-token":                          {marker: `engine.hf_token`},
	"PUT /api/admin/engines/civitai-token":                        {marker: `engine.civitai_token`},
	"DELETE /api/admin/engines/civitai-token":                     {marker: `engine.civitai_token`},
	"PUT /api/admin/engines/comfy-lan":                            {marker: `engine.comfy_lan`},
	"DELETE /api/admin/engines/comfy-lan":                         {marker: `engine.comfy_lan`},
	"PUT /api/admin/engines/civitai-red":                          {marker: `engine.civitai_red`},
	"POST /api/admin/engines/issue-token":                         {marker: `engine.issue_token`},
	"POST /api/admin/engines/{key}/discover":                      {exempt: exReadOnly + " (asks an external ComfyUI what files it has)"},
	"POST /api/admin/engines/{key}/ingest/resolve":                {exempt: exReadOnly},
	"POST /api/admin/engines/{key}/ingest/files":                  {exempt: exReadOnly},
	"POST /api/admin/engines/{key}/ingest/search":                 {exempt: exReadOnly},
	"POST /api/admin/engines/{key}/ingest/versions":               {exempt: exReadOnly},
	"POST /api/admin/engines/search":                              {exempt: exReadOnly},
	"POST /api/admin/engines/{key}/models/{id}/complete":          {exempt: "KNOWN GAP: moves objects and declares catalogue rows without a record (engine_complete.go); tracked in a follow-up issue"},
}

// roleRaceStore lets another change land between the handler's read of the membership and its
// update, the way a concurrent request would.
type roleRaceStore struct {
	store.Store
	inner *store.SQL
}

func (s roleRaceStore) GetMembership(ctx context.Context, identityID, tenantID string) (store.Membership, bool, error) {
	m, ok, err := s.inner.GetMembership(ctx, identityID, tenantID)
	if ok {
		_ = s.inner.SetMembershipRole(ctx, m.ID, "tenant_admin")
	}
	return m, ok, err
}

// "from" is the role the update overwrote, not the one read earlier: with another promotion in
// between, member -> member would otherwise be recorded for a tenant_admin -> member change.
func TestSetMembershipRoleRecordsTheRoleItActuallyOverwrote(t *testing.T) {
	ctx := context.Background()
	st, mgr, _, tn := destroyFixture(t, fixedRuntimeFactory{&reachableHomeRuntime{unreachableHomeRuntime: unreachableHomeRuntime{rec: &wipeRecorder{}, state: "stopped"}}})
	boss, _, _ := st.GetIdentityByUserKey(ctx, "boss-acme-co-jp")
	mgr.store = roleRaceStore{st, st}
	w := httptest.NewRecorder()
	newAdminAPI(mgr).setMembershipRole(w, adminRequest(http.MethodPut, "/api/admin/membership-role",
		`{"tenant_slug":"sales","user_key":"leaver-acme-co-jp","role":"member"}`), boss)
	if w.Code != http.StatusOK {
		t.Fatalf("role change = %d %s", w.Code, w.Body.String())
	}
	rows := auditRowsOf(t, st, tn.ID, "membership.role")
	if len(rows) != 1 || rows[0].Detail != "from=tenant_admin to=member" {
		t.Errorf("membership.role rows = %+v, want one with from=tenant_admin to=member", rows)
	}
}
