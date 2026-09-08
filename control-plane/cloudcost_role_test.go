package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	cetypes "github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// The shared bucket by af-role (ADR 0048 decision 15). Everything here defends one claim:
// this is the same invoice grouped a second way, so it must cover exactly the rows the
// per-member pass calls shared — no more (double counting) and no less (a total that does
// not add up next to the one above it).

func ceRoleGroup(roleKey, service, unblended, amortized string) cetypes.Group {
	return cetypes.Group{
		Keys: []string{roleKey, service},
		Metrics: map[string]cetypes.MetricValue{
			"UnblendedCost": {Amount: aws.String(unblended), Unit: aws.String("USD")},
			"AmortizedCost": {Amount: aws.String(amortized), Unit: aws.String("USD")},
		},
	}
}

// An EMPTY role is the platform residual — NAT, ALB, RDS, DNS, tax — and it is the biggest
// row on the reference deployment. costRowFrom's sibling drops nothing here on purpose:
// without it the card cannot say what is left after the engines, which is the comparison a
// reader is actually making.
func TestCostRoleRowFromKeepsTheEmptyRole(t *testing.T) {
	got, ok := costRoleRowFrom("2026-09-07", false, ceRoleGroup("af-role$engine-llm", "Amazon EC2", "4.72", "4.72"))
	if !ok || got.Role != "engine-llm" || got.Unblended != 4_720_000 || got.Service != "Amazon EC2" {
		t.Fatalf("engine row = %+v ok=%v", got, ok)
	}
	got, ok = costRoleRowFrom("2026-09-07", true, ceRoleGroup("af-role$", "Amazon Route 53", "2.00", "2.00"))
	if !ok {
		t.Fatal("the untagged residual was dropped — the card cannot name what is left without it")
	}
	if got.Role != "" || got.Unblended != 2_000_000 || !got.Estimated {
		t.Fatalf("residual row = %+v", got)
	}
	// A key that is not the one we asked for is refused rather than guessed at, exactly as
	// in costRowFrom: a mis-parse would attribute the whole bill to one fictional role.
	if _, ok := costRoleRowFrom("2026-09-07", false, ceRoleGroup("af-pool$af-ecs", "Amazon EC2", "1.00", "1.00")); ok {
		t.Error("a group keyed by another tag was accepted")
	}
	// Zero rows are skipped: Cost Explorer returns a line for every service it knows.
	if _, ok := costRoleRowFrom("2026-09-07", false, ceRoleGroup("af-role$slot", "AWS Lambda", "0", "0")); ok {
		t.Error("a zero row was stored")
	}
}

// The filter is what keeps the two cuts from overlapping. A slot a member is holding
// carries BOTH af-membership and af-role=slot: without ABSENT it would appear in that
// member's attributed total and again under role `slot`, and the shared card would add up
// to more than the shared bucket.
func TestFetchByRoleAsksOnlyForTheSharedBucket(t *testing.T) {
	ce := &fakeCE{pages: []costexplorer.GetCostAndUsageOutput{{
		ResultsByTime: []cetypes.ResultByTime{{
			TimePeriod: &cetypes.DateInterval{Start: aws.String("2026-08-17")},
			Groups: []cetypes.Group{
				ceRoleGroup("af-role$engine-llm", "Amazon EC2", "4.72", "4.72"),
				ceRoleGroup("af-role$", "Amazon Route 53", "2.00", "2.00"),
			},
		}},
	}}}
	_, _, p := costPollerFixture(t, ce)
	rows, days, err := p.fetchByRole(context.Background())
	if err != nil {
		t.Fatalf("fetchByRole: %v", err)
	}
	if len(rows) != 2 || len(days) != 1 {
		t.Fatalf("rows=%d days=%v", len(rows), days)
	}
	if len(ce.calls) != 1 {
		t.Fatalf("Cost Explorer called %d times — each one is $0.01", len(ce.calls))
	}
	in := ce.calls[0]
	if in.Filter == nil || in.Filter.Tags == nil {
		t.Fatalf("no filter: every claimed slot would be counted twice (%+v)", in.Filter)
	}
	if aws.ToString(in.Filter.Tags.Key) != runtime.EC2TagMembership {
		t.Errorf("filter key = %q, want %q", aws.ToString(in.Filter.Tags.Key), runtime.EC2TagMembership)
	}
	if len(in.Filter.Tags.MatchOptions) != 1 || in.Filter.Tags.MatchOptions[0] != cetypes.MatchOptionAbsent {
		t.Errorf("match options = %v, want ABSENT", in.Filter.Tags.MatchOptions)
	}
	if aws.ToString(in.GroupBy[0].Key) != runtime.EC2TagRole || in.GroupBy[0].Type != cetypes.GroupDefinitionTypeTag {
		t.Errorf("first axis = %+v, want the af-role tag", in.GroupBy[0])
	}
	if aws.ToString(in.GroupBy[1].Key) != "SERVICE" {
		t.Errorf("second axis = %+v, want SERVICE", in.GroupBy[1])
	}
}

// With reserved memberships present the filter has to widen to include them, because
// foldSystemMemberships moves their spend into shared at ingest. Cost Explorer rejects a
// Tags filter with an empty Values list, so the Or branch appears only when there is
// something to put in it — the empty case is the one that would break every request.
func TestSharedBucketFilterAddsTheReservedMembershipsOnlyWhenThereAreSome(t *testing.T) {
	only := sharedBucketFilter(nil)
	if only.Or != nil || only.Tags == nil {
		t.Fatalf("with no reserved memberships the filter must be the bare ABSENT term, got %+v", only)
	}
	both := sharedBucketFilter(map[string]bool{"M-seed": true, "M-probe": true})
	if len(both.Or) != 2 {
		t.Fatalf("Or = %+v, want ABSENT plus the reserved ids", both.Or)
	}
	if both.Or[0].Tags == nil || both.Or[0].Tags.MatchOptions[0] != cetypes.MatchOptionAbsent {
		t.Errorf("first branch = %+v, want ABSENT", both.Or[0].Tags)
	}
	ids := both.Or[1].Tags.Values
	// Sorted, not map order: an unstable request body makes this test flake and makes two
	// identical polls look like different questions.
	if len(ids) != 2 || ids[0] != "M-probe" || ids[1] != "M-seed" {
		t.Errorf("reserved ids = %v, want them sorted", ids)
	}
	if both.Or[1].Tags.MatchOptions[0] != cetypes.MatchOptionEquals {
		t.Errorf("second branch = %+v, want EQUALS", both.Or[1].Tags)
	}
}

// The two levels the card draws have to agree, so both are computed from the same rows in
// one place. The grouping itself is what makes the answer readable: engine-llm and
// engine-models are one question, and `slot` in the shared bucket is a different one.
func TestSharedRoleBreakdownGroupsAndSorts(t *testing.T) {
	rows := []store.CloudCostRoleRow{
		{Day: "2026-09-07", Role: "engine-llm", Service: "Amazon EC2", Unblended: 4_720_000},
		{Day: "2026-09-07", Role: "engine-llm", Service: "Amazon ECS", Unblended: 370_000},
		{Day: "2026-09-07", Role: "engine-models", Service: "Amazon S3", Unblended: 60_000},
		{Day: "2026-09-07", Role: "tts-engine", Service: "Amazon ECS", Unblended: 160_000},
		{Day: "2026-09-07", Role: "slot", Service: "Amazon EC2", Unblended: 120_000},
		{Day: "2026-09-07", Role: "", Service: "Amazon Route 53", Unblended: 2_000_000},
	}
	roles, groups := sharedRoleBreakdown(rows)
	if len(roles) != 5 {
		t.Fatalf("roles = %+v, want one per distinct af-role value", roles)
	}
	// engine-llm's two services are one role: 4.72 + 0.37.
	if roles[0].Role != "engine-llm" || roles[0].Unblended != 5_090_000 {
		t.Errorf("largest role = %+v, want engine-llm at 5.09 (its services summed)", roles[0])
	}
	if roles[0].Group != runtime.CostRoleGroupEngine {
		t.Errorf("engine-llm landed in group %q", roles[0].Group)
	}
	want := map[string]int64{
		runtime.CostRoleGroupEngine:   5_150_000, // llm + models
		runtime.CostRoleGroupTTS:      160_000,
		runtime.CostRoleGroupPool:     120_000,
		runtime.CostRoleGroupPlatform: 2_000_000,
	}
	got := map[string]int64{}
	for _, g := range groups {
		got[g.Group] = g.Unblended
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("group %s = %d, want %d", k, got[k], v)
		}
	}
	if len(groups) != len(want) {
		t.Errorf("groups = %+v, want exactly %d", groups, len(want))
	}
	if groups[0].Group != runtime.CostRoleGroupEngine {
		t.Errorf("groups are not sorted by amount: %+v", groups)
	}
}

// The by-role cut is an EXTRA breakdown, not the money everyone is charged from. If its
// request fails the per-member pass must already be stored — the reverse would let a
// secondary chart take down the invoice.
func TestPollOnceStoresTheMemberPassEvenWhenTheRolePassFails(t *testing.T) {
	ce := &fakeCE{pages: []costexplorer.GetCostAndUsageOutput{{
		ResultsByTime: []cetypes.ResultByTime{{
			TimePeriod: &cetypes.DateInterval{Start: aws.String("2026-08-17")},
			Groups:     []cetypes.Group{ceGroup("af-membership$M-1", "Amazon EC2", "2.00", "2.00")},
		}},
	}}}
	st, _, p := costPollerFixture(t, ce)
	// The by-role request is the second call of the pass and there is no page left for it,
	// which is how the fake reports a failed request.
	p.pollOnce(context.Background())
	rows, err := st.ListCloudCost(context.Background(), "", "", "2026-08-01", "2026-08-31")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("the member pass was lost because the by-role pass failed: %+v", rows)
	}
	if p.lastError() != "" {
		t.Errorf("a by-role failure was reported as the cost view being broken: %q", p.lastError())
	}
	roleRows, err := st.ListCloudCostByRole(context.Background(), "2026-08-01", "2026-08-31")
	if err != nil {
		t.Fatalf("list by role: %v", err)
	}
	if len(roleRows) != 0 {
		t.Errorf("by-role rows appeared out of a failed request: %+v", roleRows)
	}
}

// The breakdown is the deployment's own infrastructure bill, so it is under the same
// super_admin gate as the service breakdown next to it (ADR 0048 decision 4). A
// tenant_admin scoping to their own tenant must not receive it.
func TestSharedRolesAreSuperAdminOnly(t *testing.T) {
	st, mgr, _, _ := cloudCostFixture(t)
	if err := st.PutCloudCostByRole(context.Background(), []string{"2026-08-17"}, []store.CloudCostRoleRow{
		{Day: "2026-08-17", Role: "engine-llm", Service: "Amazon EC2", Unblended: 4_720_000, Currency: "USD"},
		{Day: "2026-08-17", Role: "", Service: "Amazon Route 53", Unblended: 2_000_000, Currency: "USD"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// The tenant_admin path: same request the service breakdown is withheld on.
	r := httptest.NewRequest(http.MethodGet, "/api/admin/cloud-cost?tenant=sales&from=2026-08-01&to=2026-08-31", nil)
	r.Header.Set("X-Forwarded-Email", "boss@acme.co.jp")
	w := httptest.NewRecorder()
	newAdminAPI(mgr).cloudCost(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := got["shared_roles"]; ok {
		t.Error("shared_roles leaked to a tenant_admin — it is the deployment's own bill")
	}
	if _, ok := got["shared_groups"]; ok {
		t.Error("shared_groups leaked to a tenant_admin")
	}

	// The positive control. Without it "absent" above proves nothing: a typo in the
	// response key, or rows that never reached the store, would pass the assertions
	// while the breakdown was broken for everybody.
	if _, err := st.UpsertIdentity(context.Background(), "root@acme.co.jp", "root-acme-co-jp", "super_admin"); err != nil {
		t.Fatalf("super admin: %v", err)
	}
	r = httptest.NewRequest(http.MethodGet, "/api/admin/cloud-cost?from=2026-08-01&to=2026-08-31", nil)
	r.Header.Set("X-Forwarded-Email", "root@acme.co.jp")
	w = httptest.NewRecorder()
	newAdminAPI(mgr).cloudCost(w, r)
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	groups, _ := got["shared_groups"].([]any)
	if len(groups) != 2 {
		t.Fatalf("super_admin got %v, want the engine group and the platform residual", got["shared_groups"])
	}
	first := groups[0].(map[string]any)
	if first["group"] != runtime.CostRoleGroupEngine || first["unblended_micro"].(float64) != 4_720_000 {
		t.Errorf("largest group = %+v, want the engines at 4.72", first)
	}
	if roles, _ := got["shared_roles"].([]any); len(roles) != 2 {
		t.Errorf("shared_roles = %v, want one per af-role value", got["shared_roles"])
	}
}

// An older Control Plane, or one whose extra Cost Explorer request keeps failing, has no
// by-role rows at all. The response then omits both keys rather than sending empty lists,
// so the Console draws the service breakdown alone instead of an empty section headed
// "what it was for".
func TestSharedRolesAreOmittedWhenThePollerHasNeverLandedThem(t *testing.T) {
	st, mgr, _, _ := cloudCostFixture(t)
	if _, err := st.UpsertIdentity(context.Background(), "root@acme.co.jp", "root-acme-co-jp", "super_admin"); err != nil {
		t.Fatalf("super admin: %v", err)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/admin/cloud-cost?from=2026-08-01&to=2026-08-31", nil)
	r.Header.Set("X-Forwarded-Email", "root@acme.co.jp")
	w := httptest.NewRecorder()
	newAdminAPI(mgr).cloudCost(w, r)
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := got["shared_micro"]; !ok {
		t.Fatalf("the fixture's caller is not seeing the shared bucket at all: %v", got)
	}
	if _, ok := got["shared_roles"]; ok {
		t.Errorf("shared_roles present with no rows behind it: %v", got["shared_roles"])
	}
	if _, ok := got["shared_groups"]; ok {
		t.Errorf("shared_groups present with no rows behind it: %v", got["shared_groups"])
	}
}
