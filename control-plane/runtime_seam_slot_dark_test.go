package main

import (
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
)

// Each slot-dark event reaches the audit log under its own action; the Console has a label for
// each of the three.
func TestAuditSlotDarkWritesOneActionPerEvent(t *testing.T) {
	st, ctx := newEgressMemberFixture(t)
	m := &manager{store: st}
	for action, want := range map[string]string{
		"":        "pool.slot_dark_detected",
		"rescued": "pool.slot_dark_rescued",
		"blocked": "pool.slot_dark_blocked",
	} {
		m.auditSlotDark(ctx, runtime.SlotDark{Slot: "i-" + want, Volume: "vol-1", Workspace: "af-ws-x",
			MembershipID: "M-none", Since: time.Now(), Action: action})
		rows, err := st.ListAuditByTenant(ctx, "", 100)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, r := range rows {
			if r.Action == want && r.Target == "i-"+want && r.ActorKind == "system" {
				found = true
			}
		}
		if !found {
			t.Fatalf("action %q: no %s row in %+v", action, want, rows)
		}
	}
}

// Each wedged-slot event reaches the audit log under its own action, and a blocked one carries
// its reason.
func TestAuditSlotWedgedWritesOneActionPerEvent(t *testing.T) {
	st, ctx := newEgressMemberFixture(t)
	m := &manager{store: st}
	for action, want := range map[string]string{
		"":         "pool.slot_wedged_detected",
		"released": "pool.slot_wedged_released",
		"blocked":  "pool.slot_wedged_blocked",
	} {
		m.auditSlotWedged(ctx, runtime.SlotWedged{Slot: "i-" + want, Volume: "vol-1", Workspace: "af-ws-x",
			MembershipID: "M-none", Since: time.Now(), Action: action, Reason: "ssm_offline"})
		rows, err := st.ListAuditByTenant(ctx, "", 100)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, r := range rows {
			if r.Action == want && r.Target == "i-"+want && r.ActorKind == "system" {
				found = true
				if hasReason := strings.Contains(r.Detail, "reason=ssm_offline"); hasReason != (action == "blocked") {
					t.Fatalf("%s: detail %q", want, r.Detail)
				}
			}
		}
		if !found {
			t.Fatalf("action %q: no %s row in %+v", action, want, rows)
		}
	}
}
