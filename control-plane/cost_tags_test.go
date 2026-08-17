package main

import (
	"context"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	cetypes "github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
)

// Automatic activation of the cost allocation tags (docs/67 §67.5, ADR 0048 決定 11).
//
// This is the one piece of the system that writes ACCOUNT-LEVEL billing configuration,
// so the tests are mostly about what it must REFUSE to do. Getting activation wrong in
// the other direction is not recoverable either: a key left off silently loses spend
// that can never be attributed afterwards.

func has(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// ⚠️ The single most important assertion in this file. `af-workspace` is built from the
// member's email address, so activating it would copy personal data into the billing
// export (CUR / Cost Explorer / invoice CSVs) — permanently, since past bills are not
// rewritten. It must never be in the set the CP acts on.
func TestCostTagAllowListExcludesThePersonalDataTag(t *testing.T) {
	if has(costTagKeys, ec2TagWorkspace) {
		t.Fatal("af-workspace must never be activated: its value is derived from a member's email address")
	}
	for _, k := range []string{ec2TagMembership, ec2TagTenant} {
		if !has(costTagKeys, k) {
			t.Errorf("%s is the axis this feature exists for and must be activated", k)
		}
	}
	// Operational state tags churn constantly and answer no billing question.
	for _, k := range []string{ec2TagClaim, ec2TagIdleSince, ec2TagHibernating, ec2TagBackupAt} {
		if has(costTagKeys, k) {
			t.Errorf("%s is operational state, not a billing axis", k)
		}
	}
}

func tagPoller(t *testing.T, ce *fakeCE) *cloudCostPoller {
	t.Helper()
	_, _, p := costPollerFixture(t, ce)
	return p
}

// A key AWS has already discovered but nobody ever configured is exactly the case the
// automation exists for: flip it and say so.
func TestEnsureCostTagsActivatesUnconfiguredKeys(t *testing.T) {
	ce := &fakeCE{}
	for _, k := range costTagKeys {
		ce.setTag(k, cetypes.CostAllocationTagStatusInactive, "") // discovered, never configured
	}
	st := tagPoller(t, ce).ensureCostTagsActive(context.Background())
	if len(ce.activated) != 1 || len(ce.activated[0]) != len(costTagKeys) {
		t.Fatalf("activation calls = %v, want one call covering every key", ce.activated)
	}
	if len(st.Active) != len(costTagKeys) || len(st.Pending) != 0 {
		t.Fatalf("state = %+v", st)
	}
	if !st.settled() {
		t.Error("with everything active there is nothing left to do")
	}
}

// ⚠️ A human who turned a tag OFF in their own billing console must not be overruled by
// a background loop. AWS stamps LastUpdatedDate whenever anyone changes the status, so
// "Inactive AND stamped" is a decision, while "Inactive and never stamped" is a default.
func TestEnsureCostTagsLeavesADeliberatelyDisabledKeyAlone(t *testing.T) {
	ce := &fakeCE{}
	for _, k := range costTagKeys {
		ce.setTag(k, cetypes.CostAllocationTagStatusInactive, "")
	}
	ce.setTag(ec2TagSlotSize, cetypes.CostAllocationTagStatusInactive, "2026-08-10T00:00:00Z") // switched off by a person

	st := tagPoller(t, ce).ensureCostTagsActive(context.Background())
	if !has(st.Declined, ec2TagSlotSize) {
		t.Fatalf("af-slot-size was turned off deliberately and must be left alone; state = %+v", st)
	}
	for _, asked := range ce.activated {
		if has(asked, ec2TagSlotSize) {
			t.Error("the CP re-enabled a tag a human had switched off")
		}
	}
	// The rest still get done — one declined key must not stall the others.
	if !has(st.Active, ec2TagMembership) {
		t.Errorf("the other keys should still be activated; state = %+v", st)
	}
}

// A key AWS has not discovered yet cannot be activated at all. That is not an error and
// not a permanent state — it is why this runs on a tick instead of once at boot.
func TestEnsureCostTagsRetriesAnUndiscoveredKey(t *testing.T) {
	ce := &fakeCE{}
	for _, k := range costTagKeys {
		if k == ec2TagTenant {
			continue // brand new: the CP only just started stamping it
		}
		ce.setTag(k, cetypes.CostAllocationTagStatusInactive, "")
	}
	p := tagPoller(t, ce)
	st := p.ensureCostTagsActive(context.Background())
	if !has(st.Pending, ec2TagTenant) {
		t.Fatalf("an undiscovered key must be pending, not dropped; state = %+v", st)
	}
	if st.settled() {
		t.Error("something is still pending, so the poller must keep trying")
	}

	// Next tick, after AWS has discovered it.
	ce.setTag(ec2TagTenant, cetypes.CostAllocationTagStatusInactive, "")
	st = p.ensureCostTagsActive(context.Background())
	if !has(st.Active, ec2TagTenant) || !st.settled() {
		t.Fatalf("the retry should have landed it; state = %+v", st)
	}

	// And once settled it must stop calling: a permanent no-op that bills $0.01 every
	// six hours forever is its own bug.
	before := ce.listCalls
	p.ensureCostTagsActive(context.Background())
	if ce.listCalls != before {
		t.Errorf("Cost Explorer called again after settling (%d → %d)", before, ce.listCalls)
	}
}

// ⚠️ Partial failure comes back in the RESPONSE body, not as a Go error. Trusting `err`
// alone would log "activated" for a key that was refused, and nobody would find out
// until they wondered why a column was missing months later.
func TestEnsureCostTagsReadsPerEntryErrors(t *testing.T) {
	ce := &fakeCE{updRefuse: map[string]string{ec2TagTenant: "Tag keys not found: af-tenant"}}
	for _, k := range costTagKeys {
		ce.setTag(k, cetypes.CostAllocationTagStatusInactive, "")
	}
	st := tagPoller(t, ce).ensureCostTagsActive(context.Background())
	if has(st.Active, ec2TagTenant) {
		t.Fatal("a refused key must not be reported as active")
	}
	if !has(st.Pending, ec2TagTenant) {
		t.Errorf("a refused key must stay pending so it is retried; state = %+v", st)
	}
	if !has(st.Active, ec2TagMembership) {
		t.Errorf("the keys that succeeded in the same call are still active; state = %+v", st)
	}
}

// No permission (or a member account under AWS Organizations, where only the payer may
// activate) has to be REPORTED. Swallowed, it looks identical to "this deployment spent
// nothing" — and the spend it is silently losing cannot be recovered later.
func TestEnsureCostTagsSurfacesAccessDenied(t *testing.T) {
	ce := &fakeCE{listErr: fmt.Errorf("AccessDeniedException: not authorized to perform ce:ListCostAllocationTags")}
	p := tagPoller(t, ce)
	st := p.ensureCostTagsActive(context.Background())
	if st.Error == "" {
		t.Fatal("the failure must reach the API, not be swallowed")
	}
	if st.settled() {
		t.Error("an errored state is not settled — it has to be retried, and reported meanwhile")
	}
	if len(st.Pending) != len(costTagKeys) {
		t.Errorf("with no visibility, every key is unknown-and-pending; state = %+v", st)
	}
	// The state has to be readable from the request path, since that is where the
	// Console gets it.
	if p.costTags().Error == "" {
		t.Error("costTags() must expose the same failure")
	}
}

// The activation entries must ask for Active explicitly — an empty status would be a
// silent no-op against the real API.
func TestEnsureCostTagsAsksForActive(t *testing.T) {
	ce := &fakeCE{}
	ce.setTag(ec2TagMembership, cetypes.CostAllocationTagStatusInactive, "")
	for _, k := range costTagKeys[1:] {
		ce.setTag(k, cetypes.CostAllocationTagStatusActive, "2026-08-01T00:00:00Z")
	}
	tagPoller(t, ce).ensureCostTagsActive(context.Background())
	if ce.updateCalls != 1 {
		t.Fatalf("update calls = %d, want exactly the one key that needed it", ce.updateCalls)
	}
	if len(ce.activated[0]) != 1 || ce.activated[0][0] != ec2TagMembership {
		t.Fatalf("asked for %v, want only af-membership", ce.activated[0])
	}
	for _, tg := range ce.tags {
		if aws.ToString(tg.TagKey) == ec2TagMembership && tg.Status != cetypes.CostAllocationTagStatusActive {
			t.Error("af-membership did not end up Active")
		}
	}
}
