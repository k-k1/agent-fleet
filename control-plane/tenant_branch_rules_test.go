package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// ADR 0103 decision 10. What these pin down:
//
//   - the admin face is the tenant's own: another tenant's admin and a plain member are refused
//   - the CP refuses exactly the rules the Agent would (the shared case table)
//   - the bridge answers the token's tenant, always with a list, and refuses a bad token
//   - the token reaches the container

func branchRulesCall(mgr *manager, method, slug, email, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/api/admin/tenants/"+slug+"/branch-rules", strings.NewReader(body))
	r.SetPathValue("slug", slug)
	r.Header.Set("X-Forwarded-Email", email)
	w := httptest.NewRecorder()
	adm := newAdminAPI(mgr)
	if method == http.MethodGet {
		adm.tenantBranchRules(w, r)
	} else {
		adm.setTenantBranchRules(w, r)
	}
	return w
}

func branchRulesBridgeCall(mgr *manager, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/internal/branch-rules", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	branchRulesBridgeAPI{mgr}.list(w, r)
	return w
}

func addBranchRulesMember(t *testing.T, st *store.SQL, tenantID, email, role string) store.MembershipView {
	t.Helper()
	ctx := context.Background()
	id, err := st.UpsertIdentity(ctx, email, sanitizeUser(email), "")
	if err != nil {
		t.Fatal(err)
	}
	m, err := st.EnsureMembership(ctx, id.ID, tenantID, role)
	if err != nil {
		t.Fatal(err)
	}
	mv, ok, err := st.GetMembershipByID(ctx, m.ID)
	if err != nil || !ok {
		t.Fatalf("membership: %v %v", ok, err)
	}
	return mv
}

func TestTenantBranchRulesAdminIsTheTenants(t *testing.T) {
	st := p3Store(t)
	mgr := p3Manager(t, st)
	sub := seedGitOAuthTenant(t, st, "sub", "admin@sub.co.jp")
	seedGitOAuthTenant(t, st, "other", "admin@other.co.jp")
	addBranchRulesMember(t, st, sub.ID, "user@sub.co.jp", "member")

	if w := branchRulesCall(mgr, http.MethodGet, "sub", "admin@sub.co.jp", ""); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), `"rules":[]`) {
		t.Fatalf("empty get: %d %s", w.Code, w.Body.String())
	}
	body := `{"rules":[{"match":" * ","name":"{prefix}{key}","base":"develop"},` +
		`{"match":"bitbucket.org/acme/*","types":{"bugfix":{"prefix":"bugfix/","from":["Defect"]}}}]}`
	w := branchRulesCall(mgr, http.MethodPut, "sub", "admin@sub.co.jp", body)
	if w.Code != http.StatusOK {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	var got tenantBranchRulesWire
	w = branchRulesCall(mgr, http.MethodGet, "sub", "admin@sub.co.jp", "")
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	// A tenant `*` rule may set name (the user store refuses that; the tenant's is its default
	// template), and match is stored trimmed, as the Agent compares it.
	if len(got.Rules) != 2 || got.Rules[0].Match != "*" || *got.Rules[0].Name != "{prefix}{key}" ||
		got.Rules[1].Types["bugfix"].From[0] != "Defect" || got.UpdatedAt == "" {
		t.Errorf("get after save = %+v", got)
	}

	for _, c := range []struct{ who, slug string }{
		{"admin@other.co.jp", "sub"},
		{"user@sub.co.jp", "sub"},
	} {
		if w := branchRulesCall(mgr, http.MethodGet, c.slug, c.who, ""); w.Code != http.StatusForbidden {
			t.Errorf("%s reads %s: %d", c.who, c.slug, w.Code)
		}
		if w := branchRulesCall(mgr, http.MethodPut, c.slug, c.who, `{"rules":[]}`); w.Code != http.StatusForbidden {
			t.Errorf("%s writes %s: %d", c.who, c.slug, w.Code)
		}
	}
	if w := branchRulesCall(mgr, http.MethodGet, "other", "admin@other.co.jp", ""); !strings.Contains(w.Body.String(), `"rules":[]`) {
		t.Errorf("another tenant sees sub's rules: %s", w.Body.String())
	}

	for _, bad := range []string{
		`{"rules":[{"match":"*","base":"a..b"}]}`,
		`{"rules":[{"match":"*","types":{"bugfix":{"prefixes":"fix/"}}}]}`,
		`{"rules":[{"match":"*","kinds":{}}]}`,
		`{"rules":[{"match":"*","types":{"bugfix":{"from":[""]}}}]}`,
		`{"rules":[{"match":"*","name":"` + strings.Repeat("x", 300) + `"}]}`,
		`{"rules":[` + strings.TrimSuffix(strings.Repeat(`{"match":"*"},`, 101), ",") + `]}`,
		`not json`,
	} {
		if w := branchRulesCall(mgr, http.MethodPut, "sub", "admin@sub.co.jp", bad); w.Code != http.StatusBadRequest {
			t.Errorf("accepted %.80s: %d %s", bad, w.Code, w.Body.String())
		}
	}
	// A refused save leaves the stored list alone.
	w = branchRulesCall(mgr, http.MethodGet, "sub", "admin@sub.co.jp", "")
	if !strings.Contains(w.Body.String(), "bitbucket.org/acme/*") {
		t.Errorf("refused save changed the list: %s", w.Body.String())
	}
	var n int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action='tenant.branch_rules'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("audit rows = %d (%v)", n, err)
	}
}

// TestTenantBranchRulesChecksMatchTheAgent runs the Agent's case table: a rule the CP accepts
// and the Agent drops would vanish from every member without anyone being told.
func TestTenantBranchRulesChecksMatchTheAgent(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	b, err := os.ReadFile(filepath.Join("..", "workspace", "agent", "internal", "branchrule", "testdata", "rule_checks.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		OK   bool           `json:"ok"`
		Rule branchRuleWire `json:"rule"`
	}
	if err := json.Unmarshal(b, &cases); err != nil || len(cases) < 10 {
		t.Fatalf("table: %d cases, %v", len(cases), err)
	}
	for _, c := range cases {
		if got := validateTenantBranchRules([]branchRuleWire{c.Rule}) == nil; got != c.OK {
			t.Errorf("%+v: accepted=%v, want %v", c.Rule, got, c.OK)
		}
	}
}

func TestBranchRulesBridgeServesTheTokensTenant(t *testing.T) {
	ctx := context.Background()
	st := p3Store(t)
	mgr := p3Manager(t, st)
	sub := seedGitOAuthTenant(t, st, "sub", "admin@sub.co.jp")
	other := seedGitOAuthTenant(t, st, "other", "admin@other.co.jp")
	mine := addBranchRulesMember(t, st, sub.ID, "user@sub.co.jp", "member")
	theirs := addBranchRulesMember(t, st, other.ID, "user@other.co.jp", "member")
	if w := branchRulesCall(mgr, http.MethodPut, "sub", "admin@sub.co.jp", `{"rules":[{"match":"*","base":"develop"}]}`); w.Code != http.StatusOK {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	key := branchRulesSignKey(mgr.tokenSignMaster())

	w := branchRulesBridgeCall(mgr, mintBranchRulesToken(key, mine.MembershipID))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"base":"develop"`) {
		t.Fatalf("member pull: %d %s", w.Code, w.Body.String())
	}
	// Another tenant's member gets its own tenant's (empty) list, as a list.
	w = branchRulesBridgeCall(mgr, mintBranchRulesToken(key, theirs.MembershipID))
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != `{"rules":[]}` {
		t.Fatalf("other tenant pull: %d %s", w.Code, w.Body.String())
	}
	for name, tok := range map[string]string{
		"empty":              "",
		"forged tag":         "afb_" + strings.Split(mintBranchRulesToken(key, mine.MembershipID), "_")[1][:4] + ".x",
		"other bridge's key": mintAWSProfilesToken(awsProfilesSignKey(mgr.tokenSignMaster()), mine.MembershipID),
		"wrong sign key":     mintBranchRulesToken(awsProfilesSignKey(mgr.tokenSignMaster()), mine.MembershipID),
	} {
		if w := branchRulesBridgeCall(mgr, tok); w.Code != http.StatusUnauthorized {
			t.Errorf("%s: %d", name, w.Code)
		}
	}
	if err := st.SetMembershipStatus(ctx, mine.MembershipID, "inactive"); err != nil {
		t.Fatal(err)
	}
	if w := branchRulesBridgeCall(mgr, mintBranchRulesToken(key, mine.MembershipID)); w.Code != http.StatusUnauthorized {
		t.Errorf("deactivated membership: %d", w.Code)
	}
}

func TestWorkspaceEnvCarriesTheBranchRulesBridgeToken(t *testing.T) {
	_, mgr, mv := bridgeEnv(t)
	mgr.dataRoot = t.TempDir()
	mgr.publicBaseURL = "https://af.example"
	ws := store.Workspace{ID: "ws1", TenantID: mv.TenantID, MembershipID: mv.MembershipID}
	var token string
	for _, kv := range mgr.workspaceExtraEnv(context.Background(), ws) {
		if v, ok := strings.CutPrefix(kv, "AF_BRANCH_RULES_TOKEN="); ok {
			token = v
		}
	}
	if mid, ok := verifyBranchRulesToken(branchRulesSignKey(mgr.tokenSignMaster()), token); !ok || mid != mv.MembershipID {
		t.Fatalf("injected token %q does not verify to this membership: (%q,%v)", token, mid, ok)
	}
}
