package claude

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

// A cached *failure* (ok:false) gets a much shorter life than a success. Without this,
// a single transient failure on an empty cache — likely right after the agent process
// restarts, or when the rate-limited endpoint returns 429 — would be served for the
// full usageTTL and hide the Console's chip for 5 minutes. A short negative TTL still
// throttles retries (so we don't hammer the endpoint) but recovers within seconds.
const usageFailTTL = 20 * time.Second

// When the endpoint rate-limits us (429/503 with Retry-After), honor it: don't touch
// the endpoint again until it says the window resets — retrying early just earns
// another 429 and can prolong the throttle. Capped so a pathological value can't wedge
// the chip's live numbers forever (the chip stays visible meanwhile via `authed`).
const usageBackoffMax = 30 * time.Minute

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
	usageMu           sync.Mutex
	usageAt           time.Time
	usageVal          usage
	usageBackoffUntil time.Time // don't hit the endpoint before this (server Retry-After)
)

// HandleUsage serves GET /claude/usage for the Console's WsBar chip.
func HandleUsage(w http.ResponseWriter, r *http.Request) {
	refresh := r.URL.Query().Get("refresh") == "1"
	// authed reflects "a subscription token is present" — a cheap, current file check,
	// independent of the (cached, network) usage read. The Console keeps the chip visible
	// whenever authed even if the live numbers are momentarily unavailable, so a transient
	// failure never hides it; only a truly signed-out agent has no chip.
	authed := oauthToken() != ""

	usageMu.Lock()
	have := !usageAt.IsZero()
	age := time.Since(usageAt)
	// A cached failure expires far sooner than a good value, so a transient error can't
	// hide the chip for the full TTL — it just retries on the next poll.
	ttl := usageTTL
	if have && !usageVal.OK {
		ttl = usageFailTTL
	}
	// While the server has us backed off (Retry-After from a 429/503), never touch the
	// endpoint — even on an explicit refresh — and just serve whatever we have.
	backedOff := time.Now().Before(usageBackoffUntil)
	// Hit the real endpoint only when the cache is empty or older than the TTL, or on
	// an explicit refresh that isn't within the floor window. Else serve the cache.
	fetch := !backedOff && (!have || age >= ttl || (refresh && age >= usageMinRefresh))
	if !fetch {
		v, at := usageVal, usageAt
		usageMu.Unlock()
		writeUsage(w, v, at, authed)
		return
	}
	usageMu.Unlock()

	v, retryAfter := fetchUsage(r.Context())

	usageMu.Lock()
	// A rate-limit signal parks the next fetch until the window resets (capped).
	if retryAfter > 0 {
		if retryAfter > usageBackoffMax {
			retryAfter = usageBackoffMax
		}
		usageBackoffUntil = time.Now().Add(retryAfter)
	}
	// Keep the last good value if a refresh failed — don't blank a working chip; the
	// growing age tells the user it's stale. Only overwrite on success (or first time).
	if v.OK || !have {
		usageVal, usageAt = v, time.Now()
	}
	rv, rat := usageVal, usageAt
	usageMu.Unlock()
	writeUsage(w, rv, rat, authed)
}

// writeUsage emits the usage plus its age in seconds (so the Console can show
// "N分前" and offer a manual refresh) and whether the agent is signed in (so the
// Console keeps a degraded chip visible when the live numbers are unavailable).
func writeUsage(w http.ResponseWriter, v usage, at time.Time, authed bool) {
	out := map[string]any{"ok": v.OK, "authed": authed, "fiveHour": v.FiveHour, "sevenDay": v.SevenDay}
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

// fetchUsage returns the current usage plus a Retry-After backoff (0 unless the
// endpoint rate-limited us) so the caller can park the next fetch.
func fetchUsage(ctx context.Context) (usage, time.Duration) {
	tok := oauthToken()
	if tok == "" {
		return usage{OK: false}, 0
	}
	// Try the bare endpoint first — this is what personal Pro/Max accounts need,
	// and it leaves their usage context untouched. ONLY when it rejects with 401
	// (the Team-plan signal) do we resolve the org uuid and retry with
	// ?organization_uuid=. Don't attach an org to a request that already works.
	u, status, retry := getUsage(ctx, tok, usageURL)
	if status == http.StatusOK {
		return u, 0
	}
	if status == http.StatusUnauthorized {
		if org := orgUUID(ctx, tok); org != "" {
			// uuid chars are query-safe, no escaping needed.
			u2, s2, retry2 := getUsage(ctx, tok, usageURL+"?organization_uuid="+org)
			if s2 == http.StatusOK {
				return u2, 0
			}
			if retry2 > 0 {
				retry = retry2
			}
		}
	}
	return usage{OK: false}, retry
}

// getUsage performs one usage GET and returns the parsed value, the HTTP status
// (0 on a transport/parse error), and any Retry-After the server asked for. The
// caller decides whether to retry.
func getUsage(ctx context.Context, tok, url string) (usage, int, time.Duration) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return usage{OK: false}, 0, 0
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("User-Agent", "agent-fleet-console")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return usage{OK: false}, 0, 0
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return usage{OK: false}, resp.StatusCode, retryAfter(resp)
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
		return usage{OK: false}, resp.StatusCode, 0
	}
	out := usage{OK: true}
	if raw.FiveHour != nil {
		out.FiveHour = &usageWindow{Pct: raw.FiveHour.Utilization, ResetsAt: raw.FiveHour.ResetsAt}
	}
	if raw.SevenDay != nil {
		out.SevenDay = &usageWindow{Pct: raw.SevenDay.Utilization, ResetsAt: raw.SevenDay.ResetsAt}
	}
	return out, resp.StatusCode, 0
}

// retryAfter reads the Retry-After header (delta-seconds or an HTTP-date, per
// RFC 9110) into a duration. 0 when absent, unparseable, or already elapsed.
func retryAfter(resp *http.Response) time.Duration {
	h := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if h == "" {
		return 0
	}
	if secs, err := strconv.Atoi(h); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if when, err := http.ParseTime(h); err == nil {
		if d := time.Until(when); d > 0 {
			return d
		}
	}
	return 0
}
