package claude

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// Claude subscription usage (the 5-hour + weekly bars claude shows in its /usage
// view) surfaced for the Console's WsBar. There is NO supported API for this: we
// read the OAuth access token claude stores in <config>/.credentials.json and call
// the same undocumented endpoint claude's own /usage view uses. Unofficial and
// best-effort — any failure (no token, expired, endpoint/shape change) returns
// {ok:false} so the Console simply hides the chip instead of erroring. Cached
// briefly so the Console's poll can't hammer the endpoint (it rate-limits).

const usageURL = "https://api.anthropic.com/api/oauth/usage"

// The unofficial usage endpoint is rate-limited, so we keep it soft: the cached value
// is served for up to 5 minutes, and the real endpoint is hit only when the cache is
// empty/older than that on a request, or on an explicit ?refresh=1 from the user.
// Forced refreshes are floored so a user mashing the button can't hammer it.
const usageTTL = 5 * time.Minute
const usageMinRefresh = 10 * time.Second

// usageWindow is one limit window: percent used (0–100) and the ISO reset instant
// (the Console formats it as a relative "あとN時間/N日" + an absolute date-time).
type usageWindow struct {
	Pct      float64 `json:"pct"`
	ResetsAt string  `json:"resetsAt"`
}

type usage struct {
	OK       bool         `json:"ok"`
	FiveHour *usageWindow `json:"fiveHour,omitempty"`
	SevenDay *usageWindow `json:"sevenDay,omitempty"`
}

var (
	usageMu  sync.Mutex
	usageAt  time.Time
	usageVal usage
)

// HandleUsage serves GET /claude/usage for the Console's WsBar chip.
func HandleUsage(w http.ResponseWriter, r *http.Request) {
	refresh := r.URL.Query().Get("refresh") == "1"

	usageMu.Lock()
	have := !usageAt.IsZero()
	age := time.Since(usageAt)
	// Hit the real endpoint only when the cache is empty or older than the TTL, or on
	// an explicit refresh that isn't within the floor window. Else serve the cache.
	fetch := !have || age >= usageTTL || (refresh && age >= usageMinRefresh)
	if !fetch {
		v, at := usageVal, usageAt
		usageMu.Unlock()
		writeUsage(w, v, at)
		return
	}
	usageMu.Unlock()

	v := fetchUsage(r.Context())

	usageMu.Lock()
	// Keep the last good value if a refresh failed — don't blank a working chip; the
	// growing age tells the user it's stale. Only overwrite on success (or first time).
	if v.OK || !have {
		usageVal, usageAt = v, time.Now()
	}
	rv, rat := usageVal, usageAt
	usageMu.Unlock()
	writeUsage(w, rv, rat)
}

// writeUsage emits the usage plus its age in seconds (so the Console can show
// "N分前" and offer a manual refresh).
func writeUsage(w http.ResponseWriter, v usage, at time.Time) {
	out := map[string]any{"ok": v.OK, "fiveHour": v.FiveHour, "sevenDay": v.SevenDay}
	if !at.IsZero() {
		out["ageSec"] = int(time.Since(at).Seconds())
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// oauthToken reads the subscription OAuth access token from the credentials
// file claude maintains (and refreshes) under its config dir. "" when absent.
func oauthToken() string {
	b, err := os.ReadFile(filepath.Join(ConfigDir(), ".credentials.json"))
	if err != nil {
		return ""
	}
	var c struct {
		ClaudeAiOauth struct {
			AccessToken string `json:"accessToken"`
		} `json:"claudeAiOauth"`
	}
	if json.Unmarshal(b, &c) != nil {
		return ""
	}
	return c.ClaudeAiOauth.AccessToken
}

// profileURL is the (undocumented) OAuth profile endpoint; we read the
// account's organization uuid from it.
const profileURL = "https://api.anthropic.com/api/oauth/profile"

// The org uuid is stable for a container's lifetime, so cache it once resolved.
var (
	orgUUIDMu    sync.Mutex
	orgUUIDVal   string
	orgUUIDKnown bool
)

// orgUUID resolves the account's organization uuid (cached). Only used as the
// Team-plan fallback: the usage endpoint returns 401 for a Team token without
// ?organization_uuid=<uuid>. We deliberately do NOT attach it to a request that
// already works (personal Pro/Max) — a Pro/Max account can also belong to other
// orgs, and the profile's organization may not be the usage context we want.
// Returns "" on failure.
func orgUUID(ctx context.Context, tok string) string {
	orgUUIDMu.Lock()
	if orgUUIDKnown {
		v := orgUUIDVal
		orgUUIDMu.Unlock()
		return v
	}
	orgUUIDMu.Unlock()

	uuid := fetchOrgUUID(ctx, tok)
	if uuid != "" {
		orgUUIDMu.Lock()
		orgUUIDVal, orgUUIDKnown = uuid, true
		orgUUIDMu.Unlock()
	}
	return uuid
}

func fetchOrgUUID(ctx context.Context, tok string) string {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, profileURL, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("User-Agent", "agent-fleet-console")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var raw struct {
		Organization struct {
			UUID string `json:"uuid"`
		} `json:"organization"`
	}
	if json.NewDecoder(resp.Body).Decode(&raw) != nil {
		return ""
	}
	return raw.Organization.UUID
}

func fetchUsage(ctx context.Context) usage {
	tok := oauthToken()
	if tok == "" {
		return usage{OK: false}
	}
	// Try the bare endpoint first — this is what personal Pro/Max accounts need,
	// and it leaves their usage context untouched. ONLY when it rejects with 401
	// (the Team-plan signal) do we resolve the org uuid and retry with
	// ?organization_uuid=. Don't attach an org to a request that already works.
	if u, status := getUsage(ctx, tok, usageURL); status == http.StatusOK {
		return u
	} else if status == http.StatusUnauthorized {
		if org := orgUUID(ctx, tok); org != "" {
			// uuid chars are query-safe, no escaping needed.
			u2, s2 := getUsage(ctx, tok, usageURL+"?organization_uuid="+org)
			if s2 == http.StatusOK {
				return u2
			}
		}
	}
	return usage{OK: false}
}

// getUsage performs one usage GET and returns the parsed value plus the HTTP
// status (0 on a transport/parse error). The caller decides whether to retry.
func getUsage(ctx context.Context, tok, url string) (usage, int) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return usage{OK: false}, 0
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("User-Agent", "agent-fleet-console")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return usage{OK: false}, 0
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return usage{OK: false}, resp.StatusCode
	}
	var raw struct {
		FiveHour *struct {
			Utilization float64 `json:"utilization"`
			ResetsAt    string  `json:"resets_at"`
		} `json:"five_hour"`
		SevenDay *struct {
			Utilization float64 `json:"utilization"`
			ResetsAt    string  `json:"resets_at"`
		} `json:"seven_day"`
	}
	if json.NewDecoder(resp.Body).Decode(&raw) != nil {
		return usage{OK: false}, resp.StatusCode
	}
	out := usage{OK: true}
	if raw.FiveHour != nil {
		out.FiveHour = &usageWindow{Pct: raw.FiveHour.Utilization, ResetsAt: raw.FiveHour.ResetsAt}
	}
	if raw.SevenDay != nil {
		out.SevenDay = &usageWindow{Pct: raw.SevenDay.Utilization, ResetsAt: raw.SevenDay.ResetsAt}
	}
	return out, resp.StatusCode
}
