package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	cetypes "github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
)

// --- Cost Explorer parsing -----------------------------------------------------

func ceGroup(membershipKey, service, unblended, amortized string) cetypes.Group {
	return cetypes.Group{
		Keys: []string{membershipKey, service},
		Metrics: map[string]cetypes.MetricValue{
			"UnblendedCost": {Amount: aws.String(unblended), Unit: aws.String("USD")},
			"AmortizedCost": {Amount: aws.String(amortized), Unit: aws.String("USD")},
		},
	}
}

func TestCostRowFromParsesTheTagKey(t *testing.T) {
	tenants := map[string]string{"M-1": "T-1"}

	// The ordinary case: a tagged line item belongs to a person and, through them, a tenant.
	got, ok := costRowFrom("2026-08-17", false, ceGroup("af-membership$M-1", "Amazon EC2", "1.25", "1.25"), tenants)
	if !ok {
		t.Fatal("a tagged group must be stored")
	}
	if got.MembershipID != "M-1" || got.TenantID != "T-1" || got.Service != "Amazon EC2" {
		t.Errorf("row = %+v", got)
	}
	if got.Unblended != 1_250_000 {
		t.Errorf("unblended = %d micro, want 1250000", got.Unblended)
	}

	// The UNTAGGED case is not an error and must not be dropped: it is the shared
	// bucket, which is ~78% of the bill on the reference deployment. Losing it would
	// make the deployment look 5x cheaper than it is.
	shared, ok := costRowFrom("2026-08-17", true, ceGroup("af-membership$", "Amazon Route 53", "1.50", "1.50"), tenants)
	if !ok || shared.MembershipID != "" {
		t.Fatalf("untagged group must be stored as shared, got ok=%v %+v", ok, shared)
	}
	if !shared.Estimated {
		t.Error("the estimated flag has to survive — a number that quietly moves is worse than a labelled one")
	}

	// A key that is not the tag we grouped by means we are reading someone else's
	// shape. Guessing would attribute every dollar to one fictional member.
	if _, ok := costRowFrom("2026-08-17", false, ceGroup("SomeOtherTag$M-1", "Amazon EC2", "1.00", "1.00"), tenants); ok {
		t.Error("a group whose key is not af-membership$ must be refused, not guessed at")
	}

	// Cost Explorer returns a row for every service it knows about, nearly all zero.
	if _, ok := costRowFrom("2026-08-17", false, ceGroup("af-membership$M-1", "AWS Glue", "0", "0"), tenants); ok {
		t.Error("zero-amount groups carry no information and must not be stored")
	}
}

func TestCostAmountKeepsTheCurrencyAndScale(t *testing.T) {
	// Cost Explorer really does return this many decimals; the store holds micro-units
	// so that a month of them still adds up to the invoice.
	v, cur := costAmount(cetypes.MetricValue{Amount: aws.String("0.0002819758"), Unit: aws.String("USD")})
	if v != 282 || cur != "USD" {
		t.Errorf("got %d %q, want 282 USD", v, cur)
	}
	// A malformed amount must be 0, not a panic and not a wild number.
	if v, _ := costAmount(cetypes.MetricValue{Amount: aws.String("n/a")}); v != 0 {
		t.Errorf("unparseable amount = %d, want 0", v)
	}
}

// --- the poller ----------------------------------------------------------------

type fakeCE struct {
	pages []costexplorer.GetCostAndUsageOutput
	calls []costexplorer.GetCostAndUsageInput
	err   error

	// cost allocation tag activation (cost_tags.go)
	tags        []cetypes.CostAllocationTag
	listErr     error
	updErr      error
	updRefuse   map[string]string // tag key -> error message the API returns per entry
	activated   [][]string        // one entry per UpdateCostAllocationTagsStatus call
	listCalls   int
	updateCalls int
}

func (f *fakeCE) ListCostAllocationTags(_ context.Context, _ *costexplorer.ListCostAllocationTagsInput, _ ...func(*costexplorer.Options)) (*costexplorer.ListCostAllocationTagsOutput, error) {
	f.listCalls++
	if f.listErr != nil {
		return nil, f.listErr
	}
	return &costexplorer.ListCostAllocationTagsOutput{CostAllocationTags: f.tags}, nil
}

func (f *fakeCE) UpdateCostAllocationTagsStatus(_ context.Context, in *costexplorer.UpdateCostAllocationTagsStatusInput, _ ...func(*costexplorer.Options)) (*costexplorer.UpdateCostAllocationTagsStatusOutput, error) {
	f.updateCalls++
	if f.updErr != nil {
		return nil, f.updErr
	}
	var asked []string
	var errs []cetypes.UpdateCostAllocationTagsStatusError
	for _, e := range in.CostAllocationTagsStatus {
		k := aws.ToString(e.TagKey)
		asked = append(asked, k)
		if msg, bad := f.updRefuse[k]; bad {
			errs = append(errs, cetypes.UpdateCostAllocationTagsStatusError{
				TagKey: aws.String(k), Code: aws.String("ValidationException"), Message: aws.String(msg),
			})
			continue
		}
		// Reflect the change, like the real API: the next List sees it Active.
		f.setTag(k, cetypes.CostAllocationTagStatusActive, "2026-08-17T05:26:16Z")
	}
	f.activated = append(f.activated, asked)
	return &costexplorer.UpdateCostAllocationTagsStatusOutput{Errors: errs}, nil
}

// setTag adds or updates one entry in the fake's view of the account.
// lastUpdated "" = AWS has seen the key but nobody has ever changed its status.
func (f *fakeCE) setTag(key string, status cetypes.CostAllocationTagStatus, lastUpdated string) {
	for i := range f.tags {
		if aws.ToString(f.tags[i].TagKey) == key {
			f.tags[i].Status = status
			if lastUpdated != "" {
				f.tags[i].LastUpdatedDate = aws.String(lastUpdated)
			}
			return
		}
	}
	t := cetypes.CostAllocationTag{TagKey: aws.String(key), Status: status, Type: cetypes.CostAllocationTagTypeUserDefined}
	if lastUpdated != "" {
		t.LastUpdatedDate = aws.String(lastUpdated)
	}
	f.tags = append(f.tags, t)
}

func (f *fakeCE) GetCostAndUsage(_ context.Context, in *costexplorer.GetCostAndUsageInput, _ ...func(*costexplorer.Options)) (*costexplorer.GetCostAndUsageOutput, error) {
	f.calls = append(f.calls, *in)
	if f.err != nil {
		return nil, f.err
	}
	out := f.pages[0]
	f.pages = f.pages[1:]
	return &out, nil
}

func costPollerFixture(t *testing.T, ce costExplorerAPI) (*sqlStore, *manager, *cloudCostPoller) {
	t.Helper()
	st := p3Store(t)
	mgr := p3Manager(t, st)
	p := newCloudCostPoller(mgr, ce, time.Hour, 3)
	p.now = func() time.Time { return time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC) }
	return st, mgr, p
}

// One request per pass is the whole cost model ($0.01 each), and both axes have to be
// in it because Cost Explorer allows at most two.
func TestCloudCostPollerAsksOnceWithBothAxes(t *testing.T) {
	ce := &fakeCE{pages: []costexplorer.GetCostAndUsageOutput{{
		ResultsByTime: []cetypes.ResultByTime{{
			TimePeriod: &cetypes.DateInterval{Start: aws.String("2026-08-17")},
			Estimated:  true,
			Groups:     []cetypes.Group{ceGroup("af-membership$M-1", "Amazon EC2", "2.00", "2.00")},
		}},
	}}}
	_, _, p := costPollerFixture(t, ce)
	if _, _, err := p.fetch(context.Background()); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(ce.calls) != 1 {
		t.Fatalf("Cost Explorer called %d times — each one is $0.01", len(ce.calls))
	}
	in := ce.calls[0]
	if len(in.GroupBy) != 2 {
		t.Fatalf("GroupBy = %+v, want (tag, service)", in.GroupBy)
	}
	if aws.ToString(in.GroupBy[0].Key) != ec2TagMembership || in.GroupBy[0].Type != cetypes.GroupDefinitionTypeTag {
		t.Errorf("first axis = %+v, want the af-membership tag", in.GroupBy[0])
	}
	// End is EXCLUSIVE. Asking through today would leave today out — and today is the
	// day whose number is still moving, so it is the one that must be re-fetched.
	if got := aws.ToString(in.TimePeriod.End); got != "2026-08-18" {
		t.Errorf("End = %q, want 2026-08-18 (exclusive, so today is included)", got)
	}
	if got := aws.ToString(in.TimePeriod.Start); got != "2026-08-15" {
		t.Errorf("Start = %q, want 2026-08-15 (a 3-day window ending today)", got)
	}
	if len(in.Metrics) != 2 {
		t.Errorf("Metrics = %v — extra metrics are free, extra requests are not", in.Metrics)
	}
}

func TestCloudCostPollerFollowsPagination(t *testing.T) {
	ce := &fakeCE{pages: []costexplorer.GetCostAndUsageOutput{
		{
			NextPageToken: aws.String("p2"),
			ResultsByTime: []cetypes.ResultByTime{{
				TimePeriod: &cetypes.DateInterval{Start: aws.String("2026-08-16")},
				Groups:     []cetypes.Group{ceGroup("af-membership$M-1", "Amazon EC2", "1.00", "1.00")},
			}},
		},
		{
			ResultsByTime: []cetypes.ResultByTime{{
				TimePeriod: &cetypes.DateInterval{Start: aws.String("2026-08-17")},
				Groups:     []cetypes.Group{ceGroup("af-membership$", "Amazon Route 53", "0.50", "0.50")},
			}},
		},
	}}
	_, _, p := costPollerFixture(t, ce)
	rows, days, err := p.fetch(context.Background())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(rows) != 2 || len(days) != 2 {
		t.Fatalf("rows=%d days=%v — a dropped page silently loses spend", len(rows), days)
	}
}

// A poller failure must be REPORTED, not swallowed. AccessDenied here (the account
// never enabled IAM access to billing) produces exactly the same empty screen as "this
// deployment spent nothing", and only one of those is true.
func TestCloudCostPollerSurfacesTheError(t *testing.T) {
	ce := &fakeCE{err: fmt.Errorf("AccessDeniedException: not authorized to perform ce:GetCostAndUsage")}
	_, mgr, p := costPollerFixture(t, ce)
	mgr.costPoller = p
	p.pollOnce(context.Background())
	if p.lastError() == "" {
		t.Fatal("a Cost Explorer failure must reach the API, not look like $0")
	}
}

// --- the store -----------------------------------------------------------------

// Cost Explorer restates recent days, so re-fetching one has to REPLACE it. If this
// accumulated (like AddUsage next door does, correctly, for seconds) every six-hourly
// pass would double the month.
func TestPutCloudCostReplacesRatherThanAccumulates(t *testing.T) {
	ctx := context.Background()
	st := p3Store(t)
	row := CloudCostRow{Day: "2026-08-17", MembershipID: "M-1", TenantID: "T-1",
		Service: "Amazon EC2", Unblended: 1_000_000, Amortized: 1_000_000, Currency: "USD"}

	for i := 0; i < 3; i++ {
		if err := st.PutCloudCost(ctx, []string{"2026-08-17"}, []CloudCostRow{row}); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}
	rows, err := st.ListCloudCost(ctx, "", "", "2026-08-01", "2026-08-31")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 || rows[0].Unblended != 1_000_000 {
		t.Fatalf("three identical fetches produced %+v — the day must be replaced, not summed", rows)
	}

	// A day that comes back with nothing has to become empty. Otherwise a resource that
	// lost its tag leaves its last attributed row frozen there, and the invoice keeps
	// blaming that person forever.
	if err := st.PutCloudCost(ctx, []string{"2026-08-17"}, nil); err != nil {
		t.Fatalf("put empty: %v", err)
	}
	rows, _ = st.ListCloudCost(ctx, "", "", "2026-08-01", "2026-08-31")
	if len(rows) != 0 {
		t.Fatalf("a re-fetch that returned nothing left %+v behind", rows)
	}
}

func TestCloudCostStoreScopesAndTotals(t *testing.T) {
	ctx := context.Background()
	st := p3Store(t)
	tn, _ := st.CreateTenant(ctx, "acme", "Acme")
	other, _ := st.CreateTenant(ctx, "beta", "Beta")
	ident, _ := st.UpsertIdentity(ctx, "a@x.com", "a-x-com", "")
	mem, _ := st.EnsureMembership(ctx, ident.ID, tn.ID, "member")

	rows := []CloudCostRow{
		{Day: "2026-08-17", MembershipID: mem.ID, TenantID: tn.ID, Service: "Amazon EC2", Unblended: 2_000_000, Currency: "USD"},
		{Day: "2026-08-17", MembershipID: "M-other", TenantID: other.ID, Service: "Amazon EC2", Unblended: 5_000_000, Currency: "USD"},
		{Day: "2026-08-17", MembershipID: "", TenantID: "", Service: "Amazon Route 53", Unblended: 9_000_000, Currency: "USD"},
	}
	if err := st.PutCloudCost(ctx, []string{"2026-08-17"}, rows); err != nil {
		t.Fatalf("put: %v", err)
	}

	// ⚠️ A tenant-scoped read must NOT see the shared bucket. It is the deployment's own
	// infrastructure bill, which is information about the deployment rather than about
	// this tenant.
	scoped, err := st.ListCloudCost(ctx, tn.ID, "", "2026-08-01", "2026-08-31")
	if err != nil {
		t.Fatalf("scoped list: %v", err)
	}
	for _, r := range scoped {
		if r.MembershipID == "" {
			t.Error("the shared bucket leaked into a tenant-scoped read")
		}
		if r.TenantID != tn.ID {
			t.Errorf("another tenant's row leaked: %+v", r)
		}
	}

	// The member's own view is one person and nothing else.
	mine, _ := st.ListCloudCost(ctx, "", mem.ID, "2026-08-01", "2026-08-31")
	if len(mine) != 1 || mine[0].Unblended != 2_000_000 {
		t.Fatalf("own view = %+v", mine)
	}

	totals, err := st.CloudCostTotals(ctx, "", "2026-08-01", "2026-08-31")
	if err != nil {
		t.Fatalf("totals: %v", err)
	}
	byMember := map[string]CloudCostTotal{}
	for _, tt := range totals {
		byMember[tt.MembershipID] = tt
	}
	if got := byMember[mem.ID]; got.Email != "a@x.com" || got.TenantSlug != "acme" {
		t.Errorf("labels are joined at read time so a deleted member keeps their spend; got %+v", got)
	}
	if got := byMember[""]; got.Unblended != 9_000_000 {
		t.Errorf("shared total = %+v", got)
	}

	first, last, err := st.CloudCostDays(ctx)
	if err != nil || first != "2026-08-17" || last != "2026-08-17" {
		t.Errorf("coverage = %q..%q (%v) — it is what tells an honest zero from a gap", first, last, err)
	}
}

// --- the API boundary ----------------------------------------------------------

func cloudCostFixture(t *testing.T) (*sqlStore, *manager, Tenant, Membership) {
	t.Helper()
	ctx := context.Background()
	st := p3Store(t)
	mgr := p3Manager(t, st)
	tn, _ := st.CreateTenant(ctx, "sales", "営業部")
	boss, _ := st.UpsertIdentity(ctx, "boss@acme.co.jp", "boss-acme-co-jp", "")
	if _, err := st.EnsureMembership(ctx, boss.ID, tn.ID, "tenant_admin"); err != nil {
		t.Fatalf("admin membership: %v", err)
	}
	worker, _ := st.UpsertIdentity(ctx, "w@acme.co.jp", "w-acme-co-jp", "")
	mem, err := st.EnsureMembership(ctx, worker.ID, tn.ID, "member")
	if err != nil {
		t.Fatalf("membership: %v", err)
	}
	if err := st.PutCloudCost(ctx, []string{"2026-08-17"}, []CloudCostRow{
		{Day: "2026-08-17", MembershipID: mem.ID, TenantID: tn.ID, Service: "Amazon EC2", Unblended: 2_000_000, Currency: "USD"},
		{Day: "2026-08-17", MembershipID: "", TenantID: "", Service: "Amazon Route 53", Unblended: 9_000_000, Currency: "USD"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return st, mgr, tn, mem
}

// ⚠️ The shared bucket is the deployment's infrastructure bill. A tenant_admin asking
// about their own tenant must get their members and NOT that — the same line ADR 0043
// 決定 24/25 draws between "reaches outside the tenant" and "closed inside it".
func TestCloudCostHidesTheSharedBucketFromATenantAdmin(t *testing.T) {
	_, mgr, _, mem := cloudCostFixture(t)
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
	if _, ok := got["shared_micro"]; ok {
		t.Error("a tenant_admin must not see the deployment's shared infrastructure bill")
	}
	if _, ok := got["shared_services"]; ok {
		t.Error("shared_services leaked to a tenant_admin")
	}
	members, _ := got["members"].([]any)
	if len(members) != 1 {
		t.Fatalf("members = %v, want just the one in this tenant", members)
	}
	m := members[0].(map[string]any)
	if m["membership_id"] != mem.ID {
		t.Errorf("member row = %+v", m)
	}
	// The attributed figure must exclude the shared bucket — it is what the Console
	// labels "directly attributable", and folding shared into it would make the label
	// a lie.
	if got["attributed_micro"].(float64) != 2_000_000 {
		t.Errorf("attributed = %v, want 2000000", got["attributed_micro"])
	}
}

// A member sees their own attributed spend and nothing that would let them work out
// anyone else's — no deployment total to subtract from, no shared bucket.
func TestMyCloudCostIsScopedToTheCaller(t *testing.T) {
	_, mgr, tn, mem := cloudCostFixture(t)
	r := httptest.NewRequest(http.MethodGet, "/api/cost/me?from=2026-08-01&to=2026-08-31", nil)
	w := httptest.NewRecorder()
	newAdminAPI(mgr).myCloudCost(w, r, Identity{}, MembershipView{MembershipID: mem.ID, TenantID: tn.ID, TenantSlug: tn.Slug})
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["total_micro"].(float64) != 2_000_000 {
		t.Errorf("total = %v, want only this member's 2000000", got["total_micro"])
	}
	for _, k := range []string{"shared_micro", "shared_services", "members", "attributed_micro"} {
		if _, ok := got[k]; ok {
			t.Errorf("%q must not be in a member's own response — it exposes other people's spend", k)
		}
	}
	// The coverage window has to travel with the number. Without it, a member looking
	// at a month before cost allocation was switched on reads $0.00 as "free".
	meta := got["meta"].(map[string]any)
	if meta["first_day"] != "2026-08-17" {
		t.Errorf("meta.first_day = %v — it is what tells a real zero from a gap", meta["first_day"])
	}
}

// --- one member, seen from the admin's member detail page (docs/log/67 §67.15) ------

// memberCostReq builds a request against the per-member cost route with the path
// values the mux would have filled in.
func memberCostReq(slug, key, email string) *http.Request {
	r := httptest.NewRequest(http.MethodGet,
		"/api/admin/tenants/"+slug+"/members/"+key+"/cost?from=2026-08-01&to=2026-08-31", nil)
	r.SetPathValue("slug", slug)
	r.SetPathValue("key", key)
	if email != "" {
		r.Header.Set("X-Forwarded-Email", email)
	}
	return r
}

// The member detail page exists to answer "what is this person's spend made of", which
// the per-member LIST cannot: it carries a total and nothing else. So the daily shape
// and the service breakdown are the point of this endpoint.
func TestMemberCloudCostGivesTheAdminTheShapeTheListCannot(t *testing.T) {
	_, mgr, _, _ := cloudCostFixture(t)
	w := httptest.NewRecorder()
	newAdminAPI(mgr).memberCloudCost(w, memberCostReq("sales", "w-acme-co-jp", "boss@acme.co.jp"))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["total_micro"].(float64) != 2_000_000 {
		t.Errorf("total = %v, want this member's 2000000", got["total_micro"])
	}
	days, _ := got["days"].([]any)
	if len(days) != 1 || days[0].(map[string]any)["day"] != "2026-08-17" {
		t.Fatalf("days = %v — without them this panel just repeats the list's number", days)
	}
	services, _ := got["services"].([]any)
	if len(services) != 1 || services[0].(map[string]any)["service"] != "Amazon EC2" {
		t.Fatalf("services = %v", services)
	}
	// ⚠️ The same response as /api/cost/me, so the Console renders both with one
	// component. Anything extra here would be a second shape to keep in step.
	for _, k := range []string{"shared_micro", "shared_services", "members", "attributed_micro"} {
		if _, ok := got[k]; ok {
			t.Errorf("%q must not appear on a member's detail — the shared bill is not a tenant's to see", k)
		}
	}
	meta := got["meta"].(map[string]any)
	if meta["first_day"] != "2026-08-17" {
		t.Errorf("meta.first_day = %v — the coverage window has to travel with the number", meta["first_day"])
	}
}

// ⚠️ Scoped by MEMBERSHIP, not by tenant. tenantByMembership resolves what exists
// TODAY, so once a workspace is destroyed its rows are rewritten with an empty
// tenant_id — and a tenant-scoped read would then show a confident $0.00 for somebody
// who plainly did spend. The membership already proved it belongs to this tenant.
func TestMemberCloudCostStillFindsSpendWhoseTenantWasLost(t *testing.T) {
	st, mgr, _, mem := cloudCostFixture(t)
	if err := st.PutCloudCost(context.Background(), []string{"2026-08-18"}, []CloudCostRow{
		{Day: "2026-08-18", MembershipID: mem.ID, TenantID: "", Service: "Amazon Elastic Block Store",
			Unblended: 500_000, Currency: "USD"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	w := httptest.NewRecorder()
	newAdminAPI(mgr).memberCloudCost(w, memberCostReq("sales", "w-acme-co-jp", "boss@acme.co.jp"))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var got map[string]any
	json.Unmarshal(w.Body.Bytes(), &got)
	if got["total_micro"].(float64) != 2_500_000 {
		t.Errorf("total = %v, want 2500000 — spend whose tenant_id was lost must not vanish into a confident zero",
			got["total_micro"])
	}
}

// The gate is the same as the other two member-detail endpoints: tenant_admin of this
// tenant, or a super_admin. An ordinary member of the tenant is not one.
func TestMemberCloudCostNeedsTenantAdmin(t *testing.T) {
	_, mgr, _, _ := cloudCostFixture(t)
	w := httptest.NewRecorder()
	newAdminAPI(mgr).memberCloudCost(w, memberCostReq("sales", "w-acme-co-jp", "w@acme.co.jp"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403: an ordinary member must not read a colleague's spend: %s", w.Code, w.Body.String())
	}
}

// And a person who is not a member of this tenant at all is a 404, not an empty chart:
// an empty chart reads as "they cost nothing".
func TestMemberCloudCostRefusesANonMember(t *testing.T) {
	st, mgr, _, _ := cloudCostFixture(t)
	if _, err := st.UpsertIdentity(context.Background(), "outsider@x.com", "outsider-x-com", ""); err != nil {
		t.Fatalf("identity: %v", err)
	}
	w := httptest.NewRecorder()
	newAdminAPI(mgr).memberCloudCost(w, memberCostReq("sales", "outsider-x-com", "boss@acme.co.jp"))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404: %s", w.Code, w.Body.String())
	}
}
