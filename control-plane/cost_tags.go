// cost_tags.go — keep the cost allocation tags activated, without anyone having to
// remember (docs/log/67 §67.5, ADR 0048 decision 11).
//
// Why this is automated at all: forgetting is PERMANENT. A cost allocation tag only
// applies from the moment it is switched on, so every day the step is left undone is a
// day of real spend that can never be attributed to anyone, ever. Every other
// prerequisite in this system can be fixed late; this one cannot. A line in a runbook
// loses that race (ADR 0044 decision 3 — a step nobody performs is a step that does not
// exist).
//
// And it cannot be a one-shot at boot either: AWS refuses to activate a tag key it has
// never seen on a real resource (`ValidationException: Tag keys not found`), and
// discovery takes up to a day after the CP first stamps it. So this rides the cloud-cost
// poller's tick and retries until each key lands.
//
// This writes ACCOUNT-LEVEL billing configuration — the only thing in this codebase
// that reaches outside the resources the CP created itself. Two guards keep that
// defensible:
//
//   - The key set is a fixed allow-list. In particular `af-workspace` is NEVER activated:
//     its value is built from a member's email address, and activating it would copy that
//     into the billing data (CUR / Cost Explorer / invoice CSVs). See costTagKeys.
//   - A key a human deliberately turned OFF is left off. AWS stamps LastUpdatedDate the
//     moment anyone changes a tag's status, so "Inactive with a LastUpdatedDate" means
//     somebody decided that — and re-enabling it would be the CP overruling an operator
//     in their own billing console.
package main

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	cetypes "github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// costTagKeys is the allow-list: the tags the CP writes that are BILLING AXES.
//
// Deliberately excluded, and each for a reason:
//   - af-workspace — derived from the member's email. Activating it puts personal data
//     in the billing export (ADR 0048 decision 1). This is the important one.
//   - af-claim / af-claim-at / af-idle-since / af-hibernating / af-backup-at /
//     af-quarantine-* / af-image — operational state, not axes. They change constantly
//     and would add churning columns to the bill that answer no question.
//   - af-managed-by — would be useful to separate agent-fleet from anything else in a
//     shared account, but it is only on instances (the launch template sets it), not on
//     volumes or EFS, so it cannot actually draw that line.
var costTagKeys = []string{
	runtime.EC2TagMembership, // who — the axis this whole feature exists for
	runtime.EC2TagTenant,     // which tenant (slug, so Cost Explorer is readable without the CP)
	runtime.EC2TagRole,       // home / slot / golden / backup — what KIND of resource
	runtime.EC2TagPool,       // which deployment, when an account hosts more than one
	runtime.EC2TagSlotSize,   // which instance size, for reading the pool's shape
}

// costTagState is what the API reports so a reader can tell "nothing was spent" apart
// from "this axis is not switched on yet".
type costTagState struct {
	// Active — done, and spend from the activation date onwards is attributed.
	Active []string `json:"active,omitempty"`
	// Pending — the CP has stamped it but AWS has not discovered the key yet (up to
	// ~24h). It will be retried. Spend during this window is NOT recoverable.
	Pending []string `json:"pending,omitempty"`
	// Declined — a human turned it off. Left alone on purpose.
	Declined []string `json:"declined,omitempty"`
	// Error — why the CP could not do it (no permission, or a member account under
	// AWS Organizations where only the payer may activate).
	Error string `json:"error,omitempty"`
	// Attributed are keys whose activation state could NOT be read, but whose values
	// are actually coming back in the bill — proven on by evidence.
	//
	// This exists because on a member (linked) account `ListCostAllocationTags` is
	// structurally AccessDenied (measured): only the payer may activate, and what the
	// payer did is unreadable from here. Whether it is working is still knowable — if
	// attribution is on, the cost data the poller already fetches comes back with
	// af-membership carrying a value. So when the state cannot be read, judge by
	// whether attribution actually happened. That needs no extra Cost Explorer call,
	// which matters at $0.01 per request.
	Attributed []string `json:"attributed,omitempty"`
}

// settled reports whether there is anything left to do. Once every key is either Active
// or Declined the poller stops calling Cost Explorer for this — a permanent no-op that
// bills $0.01 every six hours forever would be its own small bug.
func (s costTagState) settled() bool {
	if len(s.Attributed) > 0 {
		// The state is unreadable but attribution is demonstrably working, and more
		// List calls cannot change that — a linked account is AccessDenied forever.
		return true
	}
	return len(s.Pending) == 0 && s.Error == ""
}

// ensureCostTagsActive brings the allow-listed keys to Active and reports what is left.
// Idempotent, and a no-op once settled.
func (p *cloudCostPoller) ensureCostTagsActive(ctx context.Context) costTagState {
	if s, ok := p.tagState.Load().(costTagState); ok && s.settled() {
		return s
	}
	var state costTagState
	out, err := p.ce.ListCostAllocationTags(ctx, &costexplorer.ListCostAllocationTagsInput{
		TagKeys: costTagKeys,
	})
	if err != nil {
		state.Error = err.Error()
		state.Pending = append([]string(nil), costTagKeys...)
		p.tagState.Store(state)
		log.Printf("cost tags: cannot read activation state: %v", err)
		return state
	}
	seen := map[string]cetypes.CostAllocationTag{}
	for _, t := range out.CostAllocationTags {
		seen[aws.ToString(t.TagKey)] = t
	}
	var toActivate []string
	for _, k := range costTagKeys {
		t, found := seen[k]
		switch {
		case !found:
			// AWS has never seen this key on a billed resource. Not an error — the CP
			// may have only just started stamping it. Retry next tick.
			state.Pending = append(state.Pending, k)
		case t.Status == cetypes.CostAllocationTagStatusActive:
			state.Active = append(state.Active, k)
		case t.LastUpdatedDate != nil && aws.ToString(t.LastUpdatedDate) != "":
			// Somebody set this to Inactive on purpose. Not ours to overrule.
			state.Declined = append(state.Declined, k)
		default:
			toActivate = append(toActivate, k)
		}
	}
	if len(toActivate) == 0 {
		p.tagState.Store(state)
		if len(state.Pending) > 0 {
			log.Printf("cost tags: waiting for AWS to discover %s (spend before activation is not recoverable)",
				strings.Join(state.Pending, ", "))
		}
		return state
	}
	entries := make([]cetypes.CostAllocationTagStatusEntry, 0, len(toActivate))
	for _, k := range toActivate {
		entries = append(entries, cetypes.CostAllocationTagStatusEntry{
			TagKey: aws.String(k), Status: cetypes.CostAllocationTagStatusActive,
		})
	}
	res, err := p.ce.UpdateCostAllocationTagsStatus(ctx, &costexplorer.UpdateCostAllocationTagsStatusInput{
		CostAllocationTagsStatus: entries,
	})
	if err != nil {
		state.Error = err.Error()
		state.Pending = append(state.Pending, toActivate...)
		p.tagState.Store(state)
		log.Printf("cost tags: activating %s failed: %v", strings.Join(toActivate, ", "), err)
		return state
	}
	// Partial failure arrives in the RESPONSE, not as a Go error. Checking only `err`
	// would log "activated" for keys that were refused, and the gap would then be
	// invisible until somebody wondered why a column was missing months later.
	failed := map[string]string{}
	for _, e := range res.Errors {
		failed[aws.ToString(e.TagKey)] = fmt.Sprintf("%s: %s", aws.ToString(e.Code), aws.ToString(e.Message))
	}
	var activated []string
	for _, k := range toActivate {
		if msg, bad := failed[k]; bad {
			state.Pending = append(state.Pending, k)
			log.Printf("cost tags: %s refused (%s)", k, msg)
			continue
		}
		activated = append(activated, k)
		state.Active = append(state.Active, k)
	}
	if len(activated) > 0 {
		log.Printf("cost tags: activated %s — spend is attributed from today onwards", strings.Join(activated, ", "))
	}
	p.tagState.Store(state)
	return state
}

// costTags reports the last known activation state (empty before the first pass).
func (p *cloudCostPoller) costTags() costTagState {
	s, _ := p.tagState.Load().(costTagState)
	return s
}

// noteAttribution decides whether attribution is working from the actual cost data. It
// takes the rows the poller already fetched this tick (grouped by af-membership), so there
// is no extra Cost Explorer request.
//
// Call it only when the activation state could not be read. When it can, that is
// authoritative: values not having arrived yet — up to 24h right after activation — must
// not be misread as inactive.
func (p *cloudCostPoller) noteAttribution(rows []store.CloudCostRow) {
	s, _ := p.tagState.Load().(costTagState)
	if s.Error == "" || len(s.Attributed) > 0 {
		return // readable, or already settled by evidence
	}
	attributed := false
	for _, r := range rows {
		if r.MembershipID != "" {
			attributed = true
			break
		}
	}
	if !attributed {
		return
	}
	s.Attributed = []string{runtime.EC2TagMembership}
	s.Pending = nil
	// Replace the raw AWS error with something the reader can act on. Only af-membership
	// is asserted: that is the one axis the poller groups by, and the other keys can only
	// be presumed on — a linked account has no way to check, so do not claim a check that
	// was never made.
	s.Error = "activation state is not readable from a member account (only the payer may " +
		"activate), but " + runtime.EC2TagMembership + " values are coming back in the bill — the axis is on"
	p.tagState.Store(s)
	log.Printf("cost tags: %s is attributed in the bill; the activation state stays unreadable "+
		"from this account (member of an organization) — not retrying", runtime.EC2TagMembership)
}
