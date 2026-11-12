package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// ADR 0114 decision 8 (P2). What these pin down:
//
//   - the admin face is the tenant's own: another tenant's admin and a plain member are refused
//   - only the known agents, strictly decoded, are accepted, and a refusal stores nothing
//   - a save is audited
//   - members of that tenant read the default through GET /api/me/ui-state, others do not, and a
//     member cannot write it there
//   - deleting the tenant takes the row

func memberDefaultsCall(mgr *manager, method, slug, email, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/api/admin/tenants/"+slug+"/member-defaults", strings.NewReader(body))
	r.SetPathValue("slug", slug)
	r.Header.Set("X-Forwarded-Email", email)
	w := httptest.NewRecorder()
	adm := newAdminAPI(mgr)
	if method == http.MethodGet {
		adm.tenantMemberDefaults(w, r)
	} else {
		adm.setTenantMemberDefaults(w, r)
	}
	return w
}

func memberUIStateCall(mgr *manager, method, tenant, email, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/api/me/ui-state", strings.NewReader(body))
	r.Header.Set("X-Forwarded-Email", email)
	r.Header.Set("X-AF-Tenant", tenant)
	w := httptest.NewRecorder()
	api := newMemberUIStateAPI(mgr)
	if method == http.MethodGet {
		api.withMembership(api.get)(w, r)
	} else {
		api.withMembership(api.patch)(w, r)
	}
	return w
}

func TestTenantMemberDefaultsAdminIsTheTenants(t *testing.T) {
	st := p3Store(t)
	mgr := p3Manager(t, st)
	sub := seedGitOAuthTenant(t, st, "sub", "admin@sub.co.jp")
	seedGitOAuthTenant(t, st, "other", "admin@other.co.jp")
	addBranchRulesMember(t, st, sub.ID, "user@sub.co.jp", "member")

	if w := memberDefaultsCall(mgr, http.MethodGet, "sub", "admin@sub.co.jp", ""); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), `"defaults":{"agent":"","launch":{`) {
		t.Fatalf("empty get: %d %s", w.Code, w.Body.String())
	}
	if w := memberDefaultsCall(mgr, http.MethodPut, "sub", "admin@sub.co.jp", `{"agent":"codex"}`); w.Code != http.StatusOK {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	if w := memberDefaultsCall(mgr, http.MethodGet, "sub", "admin@sub.co.jp", ""); !strings.Contains(w.Body.String(), `"agent":"codex"`) ||
		!strings.Contains(w.Body.String(), `"updated_at"`) {
		t.Fatalf("get after save: %s", w.Body.String())
	}

	for _, who := range []string{"admin@other.co.jp", "user@sub.co.jp"} {
		if w := memberDefaultsCall(mgr, http.MethodGet, "sub", who, ""); w.Code != http.StatusForbidden {
			t.Errorf("%s reads sub: %d", who, w.Code)
		}
		if w := memberDefaultsCall(mgr, http.MethodPut, "sub", who, `{"agent":"claude"}`); w.Code != http.StatusForbidden {
			t.Errorf("%s writes sub: %d", who, w.Code)
		}
	}

	for _, bad := range []string{
		`{"agent":"shell"}`,
		`{"agent":"lcpp"}`,
		`{"agent":"Claude"}`,
		`{"agent":"claude","role":"dev"}`,
		`{"agent":"claude"} {}`,
		`{"agent":"claude"}]`,
		`{}}`,
		`null`,
		`not json`,
		`{"agent":"` + strings.Repeat("x", tenantMemberDefaultsMaxBody) + `"}`,
	} {
		if w := memberDefaultsCall(mgr, http.MethodPut, "sub", "admin@sub.co.jp", bad); w.Code != http.StatusBadRequest && w.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("accepted %.60s: %d %s", bad, w.Code, w.Body.String())
		}
	}
	if w := memberDefaultsCall(mgr, http.MethodGet, "sub", "admin@sub.co.jp", ""); !strings.Contains(w.Body.String(), `"agent":"codex"`) {
		t.Errorf("a refused save changed the default: %s", w.Body.String())
	}
	// Clearing is a save like any other.
	if w := memberDefaultsCall(mgr, http.MethodPut, "sub", "admin@sub.co.jp", `{"agent":""}`); w.Code != http.StatusOK {
		t.Errorf("clear: %d %s", w.Code, w.Body.String())
	}
	var n int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action='tenant.member_defaults'`).Scan(&n); err != nil || n != 2 {
		t.Errorf("audit rows = %d (%v), want one per accepted save", n, err)
	}
}

func TestTenantMemberDefaultsReachTheTenantsMembers(t *testing.T) {
	st := p3Store(t)
	mgr := p3Manager(t, st)
	sub := seedGitOAuthTenant(t, st, "sub", "admin@sub.co.jp")
	other := seedGitOAuthTenant(t, st, "other", "admin@other.co.jp")
	addBranchRulesMember(t, st, sub.ID, "user@sub.co.jp", "member")
	addBranchRulesMember(t, st, other.ID, "user@other.co.jp", "member")

	if w := memberDefaultsCall(mgr, http.MethodPut, "sub", "admin@sub.co.jp", `{"agent":"claude"}`); w.Code != http.StatusOK {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	if w := memberUIStateCall(mgr, http.MethodGet, "sub", "user@sub.co.jp", ""); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), `"defaults":{"agent":"claude"`) {
		t.Errorf("sub member GET = %d %s", w.Code, w.Body.String())
	}
	if w := memberUIStateCall(mgr, http.MethodGet, "other", "user@other.co.jp", ""); strings.TrimSpace(w.Body.String()) != emptyUIState {
		t.Errorf("another tenant's member sees sub's default: %s", w.Body.String())
	}
	// The defaults ride along on a PATCH answer too, and a member cannot write them.
	if w := memberUIStateCall(mgr, http.MethodPatch, "sub", "user@sub.co.jp", `{"onboarding":{"dismissed":true,"track":""}}`); !strings.Contains(w.Body.String(), `"defaults":{"agent":"claude"`) {
		t.Errorf("PATCH answer = %d %s", w.Code, w.Body.String())
	}
	if w := memberUIStateCall(mgr, http.MethodPatch, "sub", "user@sub.co.jp", `{"defaults":{"agent":"codex"}}`); w.Code != http.StatusBadRequest {
		t.Errorf("member wrote defaults: %d %s", w.Code, w.Body.String())
	}
}

func TestTenantMemberDefaultsGoWithTheTenant(t *testing.T) {
	st := p3Store(t)
	ctx := context.Background()
	a, err := st.CreateTenant(ctx, "sales", "Sales")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.PutTenantMemberDefaults(ctx, storeTenantMemberDefaults(a.ID, `{"agent":"claude"}`)); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteTenant(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := st.GetTenantMemberDefaults(ctx, a.ID); err != nil || ok {
		t.Errorf("a deleted tenant left its defaults: ok=%v err=%v", ok, err)
	}
}

func storeTenantMemberDefaults(tenantID, doc string) store.TenantMemberDefaults {
	return store.TenantMemberDefaults{TenantID: tenantID, Defaults: doc, UpdatedAt: store.NowTS()}
}

// ADR 0114 P3: the three groups, their closed validation (no secret can ride in an ops field),
// the member's override and the CP-computed effective value.
func TestTenantMemberDefaultsP3Groups(t *testing.T) {
	st := p3Store(t)
	mgr := p3Manager(t, st)
	sub := seedGitOAuthTenant(t, st, "sub", "admin@sub.co.jp")
	addBranchRulesMember(t, st, sub.ID, "user@sub.co.jp", "member")

	for _, bad := range []string{
		`{"launch":{"model":"has space"}}`,
		`{"launch":{"driver":"tui"}}`,
		`{"launch":{"permission_mode":"yolo"}}`,
		`{"launch":{"budget_usd":-1}}`,
		`{"launch":{"budget_usd":1000000}}`,
		`{"launch":{"agent":"codex"}}`,
		`{"launch":{"colour":"red"}}`,
		`{"repos":["just-a-name"]}`,
		`{"repos":["a/b","A/B"]}`,
		`{"repos":["https://user:tok@github.com/a/b"]}`,
		`{"ops":{"grafana_url":"http://g.example.com"}}`,
		`{"ops":{"grafana_url":"https://u:p@g.example.com"}}`,
		`{"ops":{"grafana_url":"https://g.example.com/?token=abc"}}`,
		`{"ops":{"aws_sso_start_url":"https://x.awsapps.com/start#tok"}}`,
		`{"ops":{"aws_account_id":"12345"}}`,
		`{"ops":{"aws_role_name":"AKIAxxxx secret"}}`,
		`{"ops":{"token":"x"}}`,
	} {
		if w := memberDefaultsCall(mgr, http.MethodPut, "sub", "admin@sub.co.jp", bad); w.Code != http.StatusBadRequest {
			t.Errorf("accepted %s: %d %s", bad, w.Code, w.Body.String())
		}
	}
	// Distinct entries, so only the count check can refuse the 21st; exactly the cap passes.
	repoList := func(n int) string {
		var q []string
		for i := 0; i < n; i++ {
			q = append(q, fmt.Sprintf(`"acme/r%d"`, i))
		}
		return `{"repos":[` + strings.Join(q, ",") + `]}`
	}
	if w := memberDefaultsCall(mgr, http.MethodPut, "sub", "admin@sub.co.jp", repoList(maxTeamRepos+1)); w.Code != http.StatusBadRequest {
		t.Errorf("accepted %d repos: %d", maxTeamRepos+1, w.Code)
	}
	if w := memberDefaultsCall(mgr, http.MethodPut, "sub", "admin@sub.co.jp", repoList(maxTeamRepos)); w.Code != http.StatusOK {
		t.Errorf("refused %d repos: %d %s", maxTeamRepos, w.Code, w.Body.String())
	}
	// Ordinary model ids, including opencode's provider/model#variant, and a normal URL pass.
	for _, ok := range []string{
		`{"launch":{"model":"stub/stub-2#high"}}`,
		`{"ops":{"grafana_url":"https://grafana.acme.example:3000/d/home"}}`,
	} {
		if w := memberDefaultsCall(mgr, http.MethodPut, "sub", "admin@sub.co.jp", ok); w.Code != http.StatusOK {
			t.Errorf("refused %s: %d %s", ok, w.Code, w.Body.String())
		}
	}
	if w := memberDefaultsCall(mgr, http.MethodPut, "sub", "admin@sub.co.jp", `{"ops":{"grafana_url":"https://:443/path"}}`); w.Code != http.StatusBadRequest {
		t.Errorf("accepted a URL with no host: %d", w.Code)
	}

	// Leave the repos list as the test below expects it.
	put := `{"agent":"claude","launch":{"model":"claude-opus-4-7","driver":"managed","permission_mode":"ask","budget_usd":5},` +
		`"repos":["acme/api","acme/web"],` +
		`"ops":{"grafana_url":"https://grafana.acme.example/d/home","aws_sso_start_url":"https://acme.awsapps.com/start","aws_account_id":"123456789012","aws_role_name":"ReadOnly"}}`
	if w := memberDefaultsCall(mgr, http.MethodPut, "sub", "admin@sub.co.jp", put); w.Code != http.StatusOK {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	var detail string
	if err := st.DB().QueryRow(`SELECT detail FROM audit_log WHERE action='tenant.member_defaults' AND detail LIKE 'agent=claude%'`).Scan(&detail); err != nil ||
		!strings.Contains(detail, "agent=claude") || !strings.Contains(detail, "launch.budget_usd=5") ||
		!strings.Contains(detail, "repos=acme/api,acme/web") || !strings.Contains(detail, "ops.aws_account_id=123456789012") {
		t.Errorf("audit detail = %q (%v)", detail, err)
	}

	type answer struct {
		Defaults  tenantMemberDefaultsWire `json:"defaults"`
		Effective effectiveWire            `json:"effective"`
	}
	read := func(method, body string) answer {
		w := memberUIStateCall(mgr, method, "sub", "user@sub.co.jp", body)
		if w.Code != http.StatusOK {
			t.Fatalf("%s %s: %d %s", method, body, w.Code, w.Body.String())
		}
		var a answer
		if err := json.Unmarshal(w.Body.Bytes(), &a); err != nil {
			t.Fatal(err)
		}
		return a
	}
	// No member override: effective is the tenant's own.
	a := read(http.MethodGet, "")
	if l := a.Effective.Launch; l.Agent != "claude" || l.Model != "claude-opus-4-7" || l.Driver != "managed" || l.PermissionMode != "ask" || l.BudgetUSD == nil || *l.BudgetUSD != 5 {
		t.Errorf("effective launch = %+v", l)
	}
	if len(a.Effective.Repos) != 2 || a.Effective.Ops.AWSRoleName != "ReadOnly" {
		t.Errorf("effective = %+v", a.Effective)
	}
	// The member's own choice wins field by field; fields they leave empty keep the tenant's.
	a = read(http.MethodPatch, `{"launch":{"agent":"codex","model":"gpt-5.2","budget_usd":0},"ops":{"aws_role_name":"Admin"}}`)
	l := a.Effective.Launch
	if l.Agent != "codex" || l.Model != "gpt-5.2" || l.Driver != "managed" || l.PermissionMode != "ask" || l.BudgetUSD == nil || *l.BudgetUSD != 0 {
		t.Errorf("effective launch after override = %+v", l)
	}
	if o := a.Effective.Ops; o.AWSRoleName != "Admin" || o.AWSAccountID != "123456789012" {
		t.Errorf("effective ops after override = %+v", o)
	}
	// The tenant layer is untouched by the member's override.
	if a.Defaults.Launch.Model != "claude-opus-4-7" {
		t.Errorf("override leaked into defaults: %+v", a.Defaults)
	}
	// A member's override passes the same validation (a secret cannot be parked there either).
	if w := memberUIStateCall(mgr, http.MethodPatch, "sub", "user@sub.co.jp", `{"ops":{"grafana_url":"https://g.example.com/?token=x"}}`); w.Code != http.StatusBadRequest {
		t.Errorf("member parked a token in ops: %d", w.Code)
	}
	// Clearing the member's override falls back to the tenant.
	a = read(http.MethodPatch, `{"launch":{"agent":"","model":"","driver":"","permission_mode":"","budget_usd":null}}`)
	if a.Effective.Launch.Model != "claude-opus-4-7" || a.Effective.Launch.Agent != "claude" {
		t.Errorf("effective after clearing = %+v", a.Effective.Launch)
	}
}

// An older Console (or script) saves only `agent`: the groups it does not know must survive.
func TestTenantMemberDefaultsPutKeepsOmittedGroups(t *testing.T) {
	st := p3Store(t)
	mgr := p3Manager(t, st)
	seedGitOAuthTenant(t, st, "sub", "admin@sub.co.jp")
	full := `{"agent":"claude","launch":{"model":"m1","budget_usd":3},"repos":["acme/api"],"ops":{"aws_role_name":"ReadOnly"}}`
	if w := memberDefaultsCall(mgr, http.MethodPut, "sub", "admin@sub.co.jp", full); w.Code != http.StatusOK {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	w := memberDefaultsCall(mgr, http.MethodPut, "sub", "admin@sub.co.jp", `{"agent":"codex","launch":null}`)
	b := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(b, `"agent":"codex"`) || !strings.Contains(b, `"model":"m1"`) ||
		!strings.Contains(b, `"acme/api"`) || !strings.Contains(b, `"ReadOnly"`) {
		t.Fatalf("an agent-only save dropped groups: %d %s", w.Code, b)
	}
	// A mis-cased group key is refused, never validated and then discarded as "left out".
	for _, bad := range []string{`{"LAUNCH":{"model":"opus"}}`, `{"agent":"codex","Repos":["acme/x"]}`, `{"OPS":{}}`} {
		if w := memberDefaultsCall(mgr, http.MethodPut, "sub", "admin@sub.co.jp", bad); w.Code != http.StatusBadRequest {
			t.Errorf("accepted %s: %d %s", bad, w.Code, w.Body.String())
		}
	}
	if w := memberDefaultsCall(mgr, http.MethodPut, "sub", "admin@sub.co.jp", `{"agent":"codex","launch":{"model":"opus"}}`); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), `"model":"opus"`) {
		t.Errorf("canonical keys: %d %s", w.Code, w.Body.String())
	}
	// Explicit empties clear.
	w = memberDefaultsCall(mgr, http.MethodPut, "sub", "admin@sub.co.jp", `{"agent":"codex","launch":{},"repos":[],"ops":{}}`)
	b = w.Body.String()
	if w.Code != http.StatusOK || strings.Contains(b, `"m1"`) || strings.Contains(b, `acme/api`) || strings.Contains(b, `ReadOnly`) {
		t.Fatalf("explicit empties did not clear: %d %s", w.Code, b)
	}
}

// A null group in a member PATCH is left alone; {} clears the override.
func TestMemberUIStateNullGroupIsNoOpAndEmptyClears(t *testing.T) {
	st := p3Store(t)
	mgr := p3Manager(t, st)
	sub := seedGitOAuthTenant(t, st, "sub", "admin@sub.co.jp")
	addBranchRulesMember(t, st, sub.ID, "user@sub.co.jp", "member")
	type doc struct {
		Launch *launchDefaultsWire `json:"launch"`
		Ops    *opsSettingsWire    `json:"ops"`
	}
	call := func(method, body string) doc {
		w := memberUIStateCall(mgr, method, "sub", "user@sub.co.jp", body)
		if w.Code != http.StatusOK {
			t.Fatalf("%s %s: %d %s", method, body, w.Code, w.Body.String())
		}
		var d doc
		if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	if d := call(http.MethodPatch, `{"launch":{"model":"m2"},"ops":{"aws_role_name":"R"}}`); d.Launch == nil || d.Ops == nil {
		t.Fatalf("not stored: %+v", d)
	}
	call(http.MethodPatch, `{"launch":null,"ops":null}`)
	if d := call(http.MethodGet, ""); d.Launch == nil || d.Launch.Model != "m2" || d.Ops == nil || d.Ops.AWSRoleName != "R" {
		t.Errorf("null changed the overrides: %+v", d)
	}
	call(http.MethodPatch, `{"launch":{},"ops":{}}`)
	if d := call(http.MethodGet, ""); d.Launch == nil || *d.Launch != (launchDefaultsWire{}) || d.Ops == nil || *d.Ops != (opsSettingsWire{}) {
		t.Errorf("{} did not clear both: %+v", d)
	}
}
