package copilot

import (
	"encoding/json"
	"testing"
)

// Real copilot_internal/user payload (trimmed to the fields we read), captured
// from a free_limited_copilot individual account.
const sampleInternalUser = `{
  "login": "octocat",
  "access_type_sku": "free_limited_copilot",
  "copilot_plan": "individual",
  "can_upgrade_plan": true,
  "quota_reset_date": "2026-08-01",
  "quota_reset_date_utc": "2026-08-01T00:00:00.000Z",
  "quota_snapshots": {
    "chat":        {"percent_remaining": 89.6, "quota_remaining": 179.2, "remaining": 179, "entitlement": 200, "unlimited": false, "has_quota": true},
    "completions": {"percent_remaining": 100,  "remaining": 2000, "entitlement": 2000, "unlimited": false, "has_quota": true},
    "premium_interactions": {"percent_remaining": 0, "remaining": 0, "entitlement": 0, "unlimited": false, "has_quota": false}
  }
}`

func TestBuildUsageFromInternalUser(t *testing.T) {
	var u internalUser
	if err := json.Unmarshal([]byte(sampleInternalUser), &u); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	res := buildUsage(u)

	// free_limited_copilot sku → human tier "Free" (copilot_plan "individual" is only the
	// account category, not the tier).
	if res.Plan != "Free" || res.Sku != "free_limited_copilot" || !res.CanUpgrade {
		t.Fatalf("plan/sku/upgrade wrong: %+v", res)
	}
	if res.User != "octocat" {
		t.Fatalf("user = %q, want octocat", res.User)
	}
	if res.ResetsAt != "2026-08-01T00:00:00.000Z" {
		t.Fatalf("resetsAt = %q", res.ResetsAt)
	}
	// premium_interactions (has_quota:false) must be dropped; chat/completions kept.
	if len(res.Quotas) != 2 {
		t.Fatalf("expected 2 has_quota pools, got %d: %+v", len(res.Quotas), res.Quotas)
	}
	// Stable order: chat before completions (premium_interactions absent here).
	if res.Quotas[0].ID != "chat" || res.Quotas[1].ID != "completions" {
		t.Fatalf("order wrong: %s, %s", res.Quotas[0].ID, res.Quotas[1].ID)
	}
	chat := res.Quotas[0]
	if chat.RemainingPct != 89.6 || chat.Remaining != 179 || chat.Entitlement != 200 {
		t.Fatalf("chat quota parsed wrong: %+v", chat)
	}
}

func TestPlanLabel(t *testing.T) {
	cases := []struct{ plan, sku, want string }{
		{"individual", "free_limited_copilot", "Free"}, // Free's tier comes from the sku, not the plan
		{"individual", "copilot_pro", "Pro"},           // paid individual = Pro family
		{"business", "copilot_for_business", "Business"},
		{"enterprise", "copilot_enterprise", "Enterprise"},
		{"", "", ""},
	}
	for _, c := range cases {
		if got := planLabel(c.plan, c.sku); got != c.want {
			t.Errorf("planLabel(%q,%q) = %q, want %q", c.plan, c.sku, got, c.want)
		}
	}
}

// premium_interactions ranks before chat when the plan includes it (paid tiers).
func TestQuotaOrderPremiumFirst(t *testing.T) {
	u := internalUser{}
	u.QuotaSnapshots = map[string]struct {
		PercentRemaining float64 `json:"percent_remaining"`
		Remaining        float64 `json:"remaining"`
		Entitlement      float64 `json:"entitlement"`
		Unlimited        bool    `json:"unlimited"`
		HasQuota         bool    `json:"has_quota"`
	}{
		"chat":                 {PercentRemaining: 50, HasQuota: true},
		"premium_interactions": {PercentRemaining: 70, HasQuota: true},
	}
	res := buildUsage(u)
	if len(res.Quotas) != 2 || res.Quotas[0].ID != "premium_interactions" {
		t.Fatalf("premium should sort first: %+v", res.Quotas)
	}
}
