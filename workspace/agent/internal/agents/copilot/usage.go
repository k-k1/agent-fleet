package copilot

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// planLabel turns the internal-API fields into a human tier for the chip. copilot_plan
// is only the ACCOUNT category (individual / business / enterprise), NOT the tier — the
// actual Free/Pro tier for an individual lives in access_type_sku (e.g.
// "free_limited_copilot" = Free). So derive the tier: Free from the sku, Business /
// Enterprise from the plan, a paid individual as Pro (the only individual paid family —
// Pro / Pro+). Unknown values fall back to the raw plan so nothing is lost.
func planLabel(plan, sku string) string {
	s := strings.ToLower(sku)
	switch {
	case strings.Contains(s, "free"):
		return "Free"
	case plan == "business":
		return "Business"
	case plan == "enterprise":
		return "Enterprise"
	case plan == "individual":
		return "Pro"
	case plan != "":
		return plan
	default:
		return sku
	}
}

// copilot usage: per-account credit balance plus plan. No TUI scraping as with agy — the
// internal endpoint the copilot CLI itself uses, GET
// https://api.github.com/copilot_internal/user, returns structured JSON, and the gh
// transparent-auth token works against it directly (measured: Authorization: token alone
// gets a 200). Unlike the per-session statusLine (ai_used / context%), this is per account,
// so a TUI and a managed session read the same values.
//
// The parts of the response that matter (measured on a personal plan,
// access_type_sku=free_limited_copilot):
//   copilot_plan          "individual"
//   access_type_sku       "free_limited_copilot"
//   can_upgrade_plan      true
//   quota_reset_date_utc  "2026-08-01T00:00:00.000Z"
//   quota_snapshots.{chat,completions,premium_interactions}:
//       { percent_remaining, remaining, entitlement, unlimited, has_quota, ... }
//
// Plans differ: on individual (Free) chat/completions carry has_quota while
// premium_interactions has has_quota=false; on paid plans premium_interactions is the main
// pool. Only the pools with has_quota=true are passed to the frontend.

const usageURL = "https://api.github.com/copilot_internal/user"

// QuotaSnap is one credit pool the account has (chat / completions /
// premium_interactions). RemainingPct is 0–100 remaining (not used), matching the
// wire convention the Console's other usage chips consume.
type QuotaSnap struct {
	ID           string  `json:"id"`
	RemainingPct float64 `json:"remainingPct"`
	Remaining    float64 `json:"remaining"`
	Entitlement  float64 `json:"entitlement"`
	Unlimited    bool    `json:"unlimited"`
}

type usageResult struct {
	User       string      `json:"user,omitempty"`     // login (GitHub account)
	Plan       string      `json:"plan"`               // copilot_plan, e.g. "individual"
	Sku        string      `json:"sku"`                // access_type_sku, e.g. "free_limited_copilot"
	CanUpgrade bool        `json:"canUpgrade"`         // can_upgrade_plan
	ResetsAt   string      `json:"resetsAt,omitempty"` // quota_reset_date_utc (RFC3339)
	Quotas     []QuotaSnap `json:"quotas"`
}

// The account API is cheap but hit on every chip mount/refresh; cache like the
// other usage sources and let the Console force a refresh (?refresh=1).
const usageCacheTTL = 5 * time.Minute

var (
	usageMu     sync.Mutex
	usageCached *usageResult
	usageAt     time.Time
)

// quotaOrder gives the pools a stable, meaningful display order (map iteration is
// random). Unknown ids fall after these, alphabetically.
var quotaOrder = map[string]int{"premium_interactions": 0, "chat": 1, "completions": 2}

// HandleUsage serves GET /copilot/usage for the Console's WS-bar chip. Always 200
// with {ok, authed, …} so the chip can degrade in place (self-hides when unauthed).
func HandleUsage(w http.ResponseWriter, r *http.Request) {
	if Token() == "" {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": false, "authed": false})
		return
	}
	usageMu.Lock()
	defer usageMu.Unlock()
	if usageCached == nil || time.Since(usageAt) > usageCacheTTL || r.URL.Query().Get("refresh") != "" {
		res, err := fetchUsage(r.Context())
		if err != nil {
			out := map[string]any{"ok": false, "authed": true, "error": err.Error()}
			// stale-if-error: keep an already-shown gauge rather than blanking it on a
			// transient failure.
			if usageCached != nil {
				out["ok"] = true
				out["user"] = usageCached.User
				out["plan"], out["sku"], out["canUpgrade"] = usageCached.Plan, usageCached.Sku, usageCached.CanUpgrade
				out["resetsAt"], out["quotas"] = usageCached.ResetsAt, usageCached.Quotas
				out["ageSec"] = int(time.Since(usageAt).Seconds())
			}
			httpx.WriteJSON(w, http.StatusOK, out)
			return
		}
		usageCached, usageAt = res, time.Now()
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"authed":     true,
		"user":       usageCached.User,
		"plan":       usageCached.Plan,
		"sku":        usageCached.Sku,
		"canUpgrade": usageCached.CanUpgrade,
		"resetsAt":   usageCached.ResetsAt,
		"quotas":     usageCached.Quotas,
		"ageSec":     int(time.Since(usageAt).Seconds()),
	})
}

// internalUser mirrors the fields we read from copilot_internal/user.
type internalUser struct {
	Login             string `json:"login"`
	CopilotPlan       string `json:"copilot_plan"`
	AccessTypeSku     string `json:"access_type_sku"`
	CanUpgradePlan    bool   `json:"can_upgrade_plan"`
	QuotaResetDateUTC string `json:"quota_reset_date_utc"`
	QuotaSnapshots    map[string]struct {
		PercentRemaining float64 `json:"percent_remaining"`
		Remaining        float64 `json:"remaining"`
		Entitlement      float64 `json:"entitlement"`
		Unlimited        bool    `json:"unlimited"`
		HasQuota         bool    `json:"has_quota"`
	} `json:"quota_snapshots"`
}

func fetchUsage(ctx context.Context) (*usageResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, usageURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "token "+Token())
	req.Header.Set("Editor-Version", "CopilotCLI/agent-fleet")
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errStatus(resp.StatusCode)
	}
	var u internalUser
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return nil, err
	}
	return buildUsage(u), nil
}

// buildUsage maps the internal-API shape to our wire result, keeping only the
// pools the plan actually includes and giving them a stable order. Split out from
// the HTTP call so it is unit-testable offline.
func buildUsage(u internalUser) *usageResult {
	res := &usageResult{
		User:       u.Login,
		Plan:       planLabel(u.CopilotPlan, u.AccessTypeSku),
		Sku:        u.AccessTypeSku,
		CanUpgrade: u.CanUpgradePlan,
		ResetsAt:   u.QuotaResetDateUTC,
	}
	for id, q := range u.QuotaSnapshots {
		if !q.HasQuota { // pools the plan doesn't include (e.g. premium on Free) are noise
			continue
		}
		res.Quotas = append(res.Quotas, QuotaSnap{
			ID:           id,
			RemainingPct: q.PercentRemaining,
			Remaining:    q.Remaining,
			Entitlement:  q.Entitlement,
			Unlimited:    q.Unlimited,
		})
	}
	sort.Slice(res.Quotas, func(i, j int) bool {
		oi, oj := quotaRank(res.Quotas[i].ID), quotaRank(res.Quotas[j].ID)
		if oi != oj {
			return oi < oj
		}
		return res.Quotas[i].ID < res.Quotas[j].ID
	})
	return res
}

func quotaRank(id string) int {
	if n, ok := quotaOrder[id]; ok {
		return n
	}
	return len(quotaOrder)
}

type errStatus int

func (e errStatus) Error() string { return "copilot_internal/user HTTP " + itoa(int(e)) }

// itoa avoids pulling strconv just for an error string.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
