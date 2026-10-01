package branchrule

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// tenantCP is a CP whose /internal/branch-rules answers whatever body/status is current.
func tenantCP(t *testing.T) (set func(status int, body string), hits *atomic.Int32) {
	t.Helper()
	var status atomic.Int32
	var body atomic.Value
	hits = &atomic.Int32{}
	status.Store(http.StatusOK)
	body.Store(`{"rules":[]}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/internal/branch-rules" || r.Header.Get("Authorization") != "Bearer afb_test" {
			http.Error(w, "unexpected request", http.StatusTeapot)
			return
		}
		w.WriteHeader(int(status.Load()))
		_, _ = w.Write([]byte(body.Load().(string)))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("AF_CP_BASE_URL", srv.URL+"/")
	t.Setenv("AF_BRANCH_RULES_TOKEN", "afb_test")
	return func(s int, b string) { status.Store(int32(s)); body.Store(b) }, hits
}

func TestFetchTenantKeepsTheLastCopyWhenTheCPFails(t *testing.T) {
	isolateGit(t)
	set, hits := tenantCP(t)
	path := filepath.Join(t.TempDir(), "branch-rules-tenant.json")
	ctx := context.Background()

	if got := ReadTenant(path); got.FetchedAt != 0 || got.Rules == nil || len(got.Rules) != 0 {
		t.Fatalf("no cache yet = %+v", got)
	}

	// One good rule, one with a base git refuses: the bad one is dropped alone.
	set(http.StatusOK, `{"rules":[{"match":"*","name":"{prefix}{key}","base":"develop"},{"match":"*","base":"a..b"}]}`)
	res, err := FetchTenant(ctx, path)
	if err != nil || res.Rules != 1 || res.Dropped != 1 || !res.Changed {
		t.Fatalf("first fetch = %+v, %v", res, err)
	}
	first := ReadTenant(path)
	if len(first.Rules) != 1 || *first.Rules[0].Name != "{prefix}{key}" || first.FetchedAt == 0 {
		t.Fatalf("cache = %+v", first)
	}

	// Same rules again: written (the stamp), not a change.
	if res, err := FetchTenant(ctx, path); err != nil || res.Changed {
		t.Errorf("unchanged fetch = %+v, %v", res, err)
	}

	// Each failure leaves the copy as it was.
	for _, c := range []struct {
		status int
		body   string
	}{
		{http.StatusInternalServerError, `{"error":"boom"}`},
		{http.StatusOK, `<html>proxy error</html>`},
		{http.StatusOK, `{}`},
	} {
		set(c.status, c.body)
		if _, err := FetchTenant(ctx, path); err == nil {
			t.Errorf("%d %s: no error", c.status, c.body)
		}
		if got := ReadTenant(path); len(got.Rules) != 1 || *got.Rules[0].Name != "{prefix}{key}" {
			t.Errorf("%d %s: cache = %+v", c.status, c.body, got)
		}
	}

	// An unreachable CP too.
	t.Setenv("AF_CP_BASE_URL", "http://127.0.0.1:1")
	if _, err := FetchTenant(ctx, path); err == nil {
		t.Error("unreachable CP: no error")
	}
	if got := ReadTenant(path); len(got.Rules) != 1 {
		t.Errorf("unreachable CP: cache = %+v", got)
	}

	// An answer with no rules is a real answer: the tenant removed them.
	set, _ = tenantCP(t)
	set(http.StatusOK, `{"rules":[]}`)
	if res, err := FetchTenant(ctx, path); err != nil || !res.Changed {
		t.Errorf("emptied = %+v, %v", res, err)
	}
	if got := ReadTenant(path); len(got.Rules) != 0 {
		t.Errorf("emptied: cache = %+v", got)
	}
	if hits.Load() == 0 {
		t.Error("the CP was never asked")
	}
}

func TestFetchTenantBridgeOff(t *testing.T) {
	t.Setenv("AF_CP_BASE_URL", "")
	t.Setenv("AF_BRANCH_RULES_TOKEN", "x")
	if _, err := FetchTenant(context.Background(), filepath.Join(t.TempDir(), "x.json")); !errors.Is(err, ErrTenantBridgeOff) {
		t.Errorf("err = %v", err)
	}
}

// A tenant `*` rule may set name (it is the tenant's default template); the user store's
// refusal is the user's alone. The key and ref-name checks are the same.
func TestAcceptTenantChecks(t *testing.T) {
	isolateGit(t)
	kept, dropped := AcceptTenant([]Rule{
		{Match: "*", Name: str("{prefix}{key}")},
		{Match: ""},
		{Match: "github.com/a*/b"},
		{Match: "*", Types: map[string]KindRule{"feat": {}}},
		{Match: "*", Types: map[string]KindRule{"bugfix": {Prefix: str("-x/")}}},
		{Match: "*", Types: map[string]KindRule{"hotfix": {Base: str("@{-1}")}}},
		{Match: "bitbucket.org/acme/*", Types: map[string]KindRule{"bugfix": {Prefix: str("bugfix/"), Base: str("main"), From: []string{"Defect"}}}},
	})
	if dropped != 5 || len(kept) != 2 || kept[1].Match != "bitbucket.org/acme/*" {
		t.Errorf("kept %+v, dropped %d", kept, dropped)
	}
}

// Decision 2 with the tenant as layer 3: repository > user > tenant > built-in, field by field.
func TestTenantLayerPrecedence(t *testing.T) {
	isolateGit(t)
	tenant := TenantLayer(TenantRules{Rules: []Rule{
		{Match: "*", Name: str("{prefix}{key}"), Base: str("develop"),
			Types: map[string]KindRule{"bugfix": {Prefix: str("bugfix/"), From: []string{"Defect"}}}},
		{Match: "bitbucket.org/acme/*", Types: map[string]KindRule{"hotfix": {Base: str("main")}}},
	}})
	id := "bitbucket.org/acme/web"
	item := &Item{Key: "PROJ-7", Title: "Broken login", Type: "Defect"}

	// Tenant over built-in: name, base, a kind's prefix and its from all come from the tenant.
	got := Name([]Layer{UserLayer(nil, ""), tenant, Builtin()}, id, Request{Item: item})
	if got.Name != "bugfix/PROJ-7" || got.Kind != "bugfix" || got.Base != "develop" {
		t.Errorf("tenant over built-in = %+v", got)
	}
	if got.Sources["name"] != "tenant: *" || got.Sources["base"] != "tenant: *" || got.Sources["prefix"] != "tenant: *" {
		t.Errorf("sources = %v", got.Sources)
	}
	// A kind the tenant does not touch keeps the built-in prefix.
	if got := Name([]Layer{tenant, Builtin()}, id, Request{Item: &Item{Key: "PROJ-8"}, Kind: "docs"}); got.Name != "docs/PROJ-8" || got.Sources["prefix"] != "builtin: builtin" {
		t.Errorf("untouched kind = %+v", got)
	}
	// The more specific tenant rule supplies the hotfix base (decision 5: kind base first).
	if got := Name([]Layer{tenant, Builtin()}, id, Request{Item: &Item{Key: "PROJ-9"}, Kind: "hotfix"}); got.Base != "main" || got.Sources["base"] != "tenant: bitbucket.org/acme/*" {
		t.Errorf("hotfix = %+v", got)
	}

	// User over tenant, field by field: the user's template wins the name, the tenant's base stays.
	got = Name([]Layer{UserLayer(nil, "{type}/{num}"), tenant, Builtin()}, id, Request{Item: item})
	if got.Name != "bugfix/7" || got.Base != "develop" || got.Sources["name"] != "user: workItemBranchTemplate" {
		t.Errorf("user over tenant = %+v", got)
	}
	got = Name([]Layer{UserLayer([]Rule{{Match: id, Base: str("release")}}, ""), tenant, Builtin()}, id, Request{Item: item})
	if got.Base != "release" || got.Name != "bugfix/PROJ-7" {
		t.Errorf("user base over tenant base = %+v", got)
	}

	// Repository over tenant: a declared base wins, and the declared kind set closes bugfix.
	repo := Layer{Name: "repository", Repository: true, Rules: []Rule{{
		Source: FilePath, Base: str("trunk"), Declares: []string{"feature"},
		Types: map[string]KindRule{"feature": {Prefix: str("feat/")}},
	}}}
	got = Name([]Layer{repo, UserLayer(nil, ""), tenant, Builtin()}, id, Request{Item: item})
	if got.Base != "trunk" || got.Kind != "feature" || got.Name != "feat/PROJ-7" {
		t.Errorf("repository over tenant = %+v", got)
	}

	// A tenant rule for another repository does not apply.
	if got := Name([]Layer{tenant, Builtin()}, "github.com/acme/web", Request{Item: &Item{Key: "PROJ-9"}, Kind: "hotfix"}); got.Base != "develop" {
		t.Errorf("other repository = %+v", got)
	}
}

// TestRuleChecksTable runs the case table the CP runs too (control-plane
// tenant_branch_rules_test.go), so the two sides refuse the same tenant rules.
func TestRuleChecksTable(t *testing.T) {
	isolateGit(t)
	b, err := os.ReadFile(filepath.Join("testdata", "rule_checks.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		OK   bool `json:"ok"`
		Rule Rule `json:"rule"`
	}
	if err := json.Unmarshal(b, &cases); err != nil || len(cases) < 10 {
		t.Fatalf("table: %d cases, %v", len(cases), err)
	}
	for _, c := range cases {
		kept, _ := AcceptTenant([]Rule{c.Rule})
		if got := len(kept) == 1; got != c.OK {
			t.Errorf("%+v: accepted=%v, want %v", c.Rule, got, c.OK)
		}
	}
}
