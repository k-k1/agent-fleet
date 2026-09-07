package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// Codex subscription usage (the 5-hour + weekly rate-limit bars) surfaced for the
// Console's WsBar, mirroring the Claude usage chip. Unlike Claude — which needs an
// undocumented network call — codex records its own rate limits INTO the rollout
// JSONL: every `token_count` event carries a `rate_limits` block. We read the newest
// rollout for the quota windows. Earned Full reset credits are not recorded there, so
// HandleUsage also makes the same authenticated, read-only request as Codex's account
// rate-limits view. Tokens remain local and only the count/expiry dates are returned.
//
// rate_limits shape (verified against real rollouts):
//   "rate_limits":{"primary":{"used_percent":3.0,"window_minutes":300,"resets_at":<epoch>},
//                  "secondary":{"used_percent":0.0,"window_minutes":10080,"resets_at":<epoch>},
//                  "plan_type":"plus"}
// Do not infer the window from primary/secondary: newer account-limit configurations
// can put the weekly window in primary and omit the 5h window. window_minutes is the
// semantic discriminator (300 = ~5h, 10080 = ~7d).

// usageWindow is a copy of the struct of the same name in internal/agents/claude (usage.go);
// it is small enough that the duplication is preferred to sharing. Percent used (0–100) plus
// the ISO reset instant.
type usageWindow struct {
	Pct      float64 `json:"pct"`
	ResetsAt string  `json:"resetsAt"`
}

type recordedWindow struct {
	UsedPercent   float64 `json:"used_percent"`
	WindowMinutes int     `json:"window_minutes"`
	ResetsAt      int64   `json:"resets_at"` // unix epoch seconds
}

type resetCredit struct {
	ExpiresAt string `json:"expiresAt,omitempty"`
}

type resetCredits struct {
	AvailableCount int           `json:"availableCount"`
	Credits        []resetCredit `json:"credits,omitempty"`
}

// HandleUsage serves GET /codex/usage for the Console's WsBar chip.
func HandleUsage(w http.ResponseWriter, r *http.Request) {
	u := readUsageWith(r.Context())
	// authed = a ChatGPT-subscription login is present. The Console keeps the chip
	// visible whenever authed even if no rollout reading is available yet, so the chip
	// never vanishes on a transient miss; a codex the user isn't signed into has none.
	out := map[string]any{"ok": u.OK, "authed": codexAuthed(), "fiveHour": u.FiveHour, "sevenDay": u.SevenDay}
	if resets, ok := fetchResetCredits(r.Context()); ok {
		out["resetCredits"] = resets
		// Reset credits alone are enough to keep the Codex WS-bar chip visible.
		out["ok"] = true
	}
	if u.PlanType != "" {
		out["planType"] = u.PlanType
	}
	if e := AccountEmail(); e != "" {
		out["user"] = e
	}
	if u.AgeSec >= 0 {
		out["ageSec"] = u.AgeSec
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

type usage struct {
	OK       bool
	FiveHour *usageWindow
	SevenDay *usageWindow
	PlanType string
	AgeSec   int
	// LimitReached is the account view's own verdict that the plan is out of quota RIGHT NOW.
	// Only that source can assert it — a rollout reading carries percentages and nothing else —
	// so false here means "not asserted", never "known to be fine". PlanExhausted is the one
	// place that distinction is spelled out.
	LimitReached bool
}

const resetCreditsURL = "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits"

// accountUsageURL is the account's OWN live quota view — the same host and login the reset
// credits above already use. It is the only source that does not depend on a codex session
// having written something locally:
//
//	{"plan_type":"plus","rate_limit":{"primary_window":{"used_percent":0,
//	  "limit_window_seconds":18000,"reset_at":…},"secondary_window":{…}}}
//
// Measured 2026-09-07 with this container's login. It matters because both local sources can
// be arbitrarily stale — the rollout reading only advances when a codex session WRITES a
// rollout, and an image generation deliberately runs `--ephemeral`, which writes none
// (measured: a real generation left no new rollout file). Without this the chip keeps showing
// the last interactive session's numbers while the quota is really being spent.
//
// Unofficial, like the reset-credits call next to it — so it is one source among three and a
// failure falls back to the local readings rather than emptying the chip.
const accountUsageURL = "https://chatgpt.com/backend-api/wham/usage"

type codexAuth struct {
	AuthMode string `json:"auth_mode"`
	Tokens   struct {
		AccessToken string `json:"access_token"`
		AccountID   string `json:"account_id"`
	} `json:"tokens"`
}

// readCodexAuth loads the ChatGPT-subscription login codex stores in auth.json. ok is
// false when the file is absent/unparseable or it isn't a chatgpt login with a token.
func readCodexAuth() (codexAuth, bool) {
	b, err := os.ReadFile(filepath.Join(paths.HomeDir(), ".codex", "auth.json"))
	if err != nil {
		return codexAuth{}, false
	}
	var auth codexAuth
	if json.Unmarshal(b, &auth) != nil || auth.AuthMode != "chatgpt" || auth.Tokens.AccessToken == "" {
		return codexAuth{}, false
	}
	return auth, true
}

// codexAuthed reports whether a ChatGPT-subscription login is present — used to keep
// the WsBar chip visible even when no live reading is available.
func codexAuthed() bool {
	_, ok := readCodexAuth()
	return ok
}

// resetCreditsCache throttles the chatgpt.com backend-api call: the WS-bar chip
// polls every few seconds, and hammering the unofficial endpoint with the user's
// token invites a rate limit (the claude chip died to exactly this 429 pattern) —
// besides adding up to 8s of latency to every poll. One fetch per TTL; a failed
// refresh serves the last good value.
var resetCreditsCache struct {
	sync.Mutex
	val     resetCredits
	ok      bool
	fetched time.Time
}

const resetCreditsTTL = 5 * time.Minute

// accountUsageCache throttles the quota call for the same reason resetCreditsCache does: the
// chip polls every few seconds and this is an unofficial endpoint reached with the user's own
// token.
type accountUsageCacheT struct {
	sync.Mutex
	val     usage
	ok      bool
	fetched time.Time
}

var accountUsageCache accountUsageCacheT

const accountUsageTTL = 5 * time.Minute

func accountUsage(ctx context.Context) (usage, bool) {
	auth, ok := readCodexAuth()
	if !ok {
		return usage{}, false
	}
	c := &accountUsageCache
	c.Lock() // also single-flights concurrent polls
	defer c.Unlock()
	if !c.fetched.IsZero() && time.Since(c.fetched) < accountUsageTTL {
		return c.aged(), c.ok
	}
	val, got := getAccountUsage(ctx, http.DefaultClient, accountUsageURL, auth.Tokens.AccessToken, auth.Tokens.AccountID)
	if got {
		c.val, c.ok = val, true
	}
	c.fetched = time.Now()
	return c.aged(), c.ok
}

// aged stamps how old the cached reading is, so readUsage can compare it against a local one
// on the same scale. Caller holds the lock.
func (c *accountUsageCacheT) aged() usage {
	u := c.val
	if c.ok {
		u.AgeSec = int(time.Since(c.fetched).Seconds())
	}
	return u
}

// getAccountUsage maps the account view onto the same window shape the rollout reading uses,
// so classifyWindows stays the single place that decides which window is which. The endpoint
// gives SECONDS where a rollout gives minutes.
func getAccountUsage(ctx context.Context, client *http.Client, url, token, accountID string) (usage, bool) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return usage{}, false
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if accountID != "" {
		req.Header.Set("ChatGPT-Account-Id", accountID)
	}
	resp, err := client.Do(req)
	if err != nil {
		return usage{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return usage{}, false
	}
	var raw struct {
		PlanType  string `json:"plan_type"`
		RateLimit struct {
			LimitReached bool           `json:"limit_reached"`
			Primary      *accountWindow `json:"primary_window"`
			Secondary    *accountWindow `json:"secondary_window"`
		} `json:"rate_limit"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&raw) != nil {
		return usage{}, false
	}
	u, ok := classifyWindows(raw.RateLimit.Primary.recorded(), raw.RateLimit.Secondary.recorded(), raw.PlanType)
	if !ok {
		return usage{}, false
	}
	u.LimitReached = raw.RateLimit.LimitReached
	return u, true
}

// PlanExhausted reports whether the ChatGPT plan behind the Codex login is out of quota right
// now. known=false means it could not be found out — no login, the call failed, or the answer
// is only a local reading, which carries no such flag.
//
// It exists so that a feature which SPENDS that plan can step aside before spending it: image
// generation (ADR 0069) routes to the next provider instead of calling codex and getting an
// honest but useless failure. The distinction matters — "not asserted" must never be read as
// "known to be fine", or a caller would treat an unreachable endpoint as permission to go
// ahead. Here that is the right default anyway (fail open, try codex, let it say no itself),
// but it has to be a decision rather than an accident.
func PlanExhausted(ctx context.Context) (exhausted, known bool) {
	u, ok := accountUsage(ctx)
	if !ok || !u.OK {
		return false, false
	}
	return u.LimitReached, true
}

// accountWindow is one window as the account view spells it.
type accountWindow struct {
	UsedPercent float64 `json:"used_percent"`
	WindowSec   int     `json:"limit_window_seconds"`
	ResetAt     int64   `json:"reset_at"`
}

func (w *accountWindow) recorded() *recordedWindow {
	if w == nil {
		return nil
	}
	return &recordedWindow{UsedPercent: w.UsedPercent, WindowMinutes: w.WindowSec / 60, ResetsAt: w.ResetAt}
}

func fetchResetCredits(ctx context.Context) (resetCredits, bool) {
	auth, ok := readCodexAuth()
	if !ok {
		return resetCredits{}, false
	}
	c := &resetCreditsCache
	c.Lock() // also single-flights concurrent polls
	defer c.Unlock()
	if !c.fetched.IsZero() && time.Since(c.fetched) < resetCreditsTTL {
		return c.val, c.ok
	}
	val, got := getResetCredits(ctx, http.DefaultClient, resetCreditsURL, auth.Tokens.AccessToken, auth.Tokens.AccountID)
	if got {
		c.val, c.ok = val, true
	}
	c.fetched = time.Now()
	return c.val, c.ok
}

func getResetCredits(ctx context.Context, client *http.Client, url, token, accountID string) (resetCredits, bool) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return resetCredits{}, false
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if accountID != "" {
		req.Header.Set("ChatGPT-Account-Id", accountID)
	}
	resp, err := client.Do(req)
	if err != nil {
		return resetCredits{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return resetCredits{}, false
	}
	var raw struct {
		Credits []struct {
			ExpiresAt *string `json:"expires_at"`
		} `json:"credits"`
		AvailableCount int `json:"available_count"`
	}
	if json.NewDecoder(resp.Body).Decode(&raw) != nil {
		return resetCredits{}, false
	}
	out := resetCredits{AvailableCount: raw.AvailableCount}
	for _, credit := range raw.Credits {
		if credit.ExpiresAt != nil {
			out.Credits = append(out.Credits, resetCredit{ExpiresAt: *credit.ExpiresAt})
		}
	}
	return out, true
}

// RateLimitWindow is one rate-limit window as pushed by the shared app-server's
// account/rateLimits/updated notification (package main, codex_appserver.go). The
// json tags follow the app-server wire names; ResetsAt is unix epoch seconds
// (verified live against `account/rateLimits/read`, CLI 0.144.4).
type RateLimitWindow struct {
	UsedPercent   float64 `json:"usedPercent"`
	WindowMinutes int     `json:"windowDurationMins"`
	ResetsAt      int64   `json:"resetsAt"`
}

// observedRateLimits holds the latest app-server push. The rollout snapshot is
// only as fresh as the last recorded turn event; the push arrives the moment the
// backend reports a new reading, so it wins whenever it is the newer of the two.
var observedRateLimits struct {
	sync.Mutex
	primary, secondary *RateLimitWindow
	planType           string
	at                 time.Time
}

// SetObservedRateLimits records an account/rateLimits/updated push from the
// app-server observer. Not cleared on observer disconnect: readUsage compares
// ages, so a stale observation simply loses to a fresher rollout reading.
//
// The push is a SPARSE rolling update: as the 0.144.4 schema states, nullable means
// "unchanged", not "gone". A missing window or plan keeps the previously observed value and
// only non-nil fields are taken in. Overwriting wholesale would let a primary-only push erase
// the weekly window and planType, and WsBar's 7d bar would go missing until the next full
// read.
func SetObservedRateLimits(primary, secondary *RateLimitWindow, planType string) {
	observedRateLimits.Lock()
	defer observedRateLimits.Unlock()
	if primary != nil {
		observedRateLimits.primary = primary
	}
	if secondary != nil {
		observedRateLimits.secondary = secondary
	}
	if planType != "" {
		observedRateLimits.planType = planType
	}
	observedRateLimits.at = time.Now()
}

func observedUsage() (usage, bool) {
	observedRateLimits.Lock()
	p, s := observedRateLimits.primary, observedRateLimits.secondary
	plan, at := observedRateLimits.planType, observedRateLimits.at
	observedRateLimits.Unlock()
	if at.IsZero() {
		return usage{}, false
	}
	conv := func(w *RateLimitWindow) *recordedWindow {
		if w == nil {
			return nil
		}
		return &recordedWindow{UsedPercent: w.UsedPercent, WindowMinutes: w.WindowMinutes, ResetsAt: w.ResetsAt}
	}
	u, ok := classifyWindows(conv(p), conv(s), plan)
	if !ok {
		return usage{}, false
	}
	u.AgeSec = int(time.Since(at).Seconds())
	return u, true
}

// observedFreshSkipSec: when the push is newer than this, the rollout glob/scan is skipped
// entirely. Both sources carry the same value for the same account, so even if a rollout held
// a fresher reading inside this window it would be equivalent — no need to stat every rollout
// on each poll of the chip.
const observedFreshSkipSec = 10

// readUsage returns the fresher of the two sources of the same account-wide
// reading: the app-server push and the newest rollout-recorded snapshot.
// readUsageWith adds the account's own live quota to the two LOCAL readings and takes
// whichever is freshest. The account view is the only one that does not depend on a codex
// session having written something here, so it is what keeps the chip honest across an
// image generation (`--ephemeral`, no rollout) or any other quiet stretch. When the call
// fails the chip is exactly what it was before.
func readUsageWith(ctx context.Context) usage {
	local := readUsage()
	acct, ok := accountUsage(ctx)
	return pickUsage(local, acct, ok)
}

// pickUsage is the choice itself, kept apart from the fetching so it can be tested without a
// network. Both are real readings: the older one is the one to drop — a rollout written
// seconds ago beats a 5-minute-old cached fetch, and vice versa.
func pickUsage(local, acct usage, acctOK bool) usage {
	if !acctOK {
		return local
	}
	if !local.OK {
		return acct
	}
	if local.AgeSec >= 0 && local.AgeSec <= acct.AgeSec {
		return local
	}
	return acct
}

func readUsage() usage {
	observed, ok := observedUsage()
	if ok && observed.AgeSec <= observedFreshSkipSec {
		return observed
	}
	rollout := rolloutUsage()
	if !ok {
		return rollout
	}
	if !rollout.OK || rollout.AgeSec < 0 || observed.AgeSec <= rollout.AgeSec {
		return observed
	}
	return rollout
}

// rolloutUsage finds the freshest rate_limits across codex rollouts. rate_limits are
// account-wide, so the most recently written rollout that carries one wins — we iterate
// rollout files newest-first and return the first reading found.
func rolloutUsage() usage {
	root := filepath.Join(paths.HomeDir(), ".codex", "sessions")
	matches, err := filepath.Glob(filepath.Join(root, "*", "*", "*", "rollout-*.jsonl"))
	if err != nil || len(matches) == 0 {
		return usage{OK: false, AgeSec: -1}
	}
	// newest file first
	sort.Slice(matches, func(i, j int) bool {
		return jsonlMtime(matches[i]).After(jsonlMtime(matches[j]))
	})
	// Only the few newest files can hold the current reading; cap the scan so a large
	// session history doesn't turn a chip poll into a full-disk read.
	if len(matches) > 8 {
		matches = matches[:8]
	}
	for _, path := range matches {
		if u, ok := usageFromRollout(path); ok {
			return u
		}
	}
	return usage{OK: false, AgeSec: -1}
}

// usageFromRollout scans a rollout file from the end for the last token_count
// event bearing a rate_limits block. ok is false when the file has no such reading yet.
func usageFromRollout(path string) (usage, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return usage{}, false
	}
	return usageFromRolloutBytes(b)
}

// usageFromRolloutBytes is the parse over rollout content (split out for tests).
func usageFromRolloutBytes(b []byte) (usage, bool) {
	lines := bytes.Split(b, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		line := bytes.TrimSpace(lines[i])
		// Cheap reject before JSON parse: the reading only lives on token_count lines.
		if len(line) == 0 || !bytes.Contains(line, []byte("rate_limits")) {
			continue
		}
		var ev struct {
			Timestamp string          `json:"timestamp"`
			Payload   json.RawMessage `json:"payload"`
		}
		if json.Unmarshal(line, &ev) != nil {
			continue
		}
		if u, ok := parseRateLimits(ev.Payload); ok {
			u.AgeSec = ageSecFrom(ev.Timestamp)
			return u, true
		}
	}
	return usage{}, false
}

// parseRateLimits parses the rate_limits from a token_count payload into the shared
// usageWindow shape. ok is false for payloads without a rate_limits block.
func parseRateLimits(payload json.RawMessage) (usage, bool) {
	var p struct {
		Type       string `json:"type"`
		RateLimits *struct {
			Primary   *recordedWindow `json:"primary"`
			Secondary *recordedWindow `json:"secondary"`
			PlanType  string          `json:"plan_type"`
		} `json:"rate_limits"`
	}
	if json.Unmarshal(payload, &p) != nil || p.Type != "token_count" || p.RateLimits == nil {
		return usage{}, false
	}
	rl := p.RateLimits
	return classifyWindows(rl.Primary, rl.Secondary, rl.PlanType)
}

// classifyWindows maps a primary/secondary rate-limit pair onto the 5h/weekly
// usage slots. Shared by the rollout parse and the app-server push, which carry
// the same reading under different field spellings.
func classifyWindows(primary, secondary *recordedWindow, planType string) (usage, bool) {
	// Both windows can be absent very early; require at least one to call it a reading.
	if primary == nil && secondary == nil {
		return usage{}, false
	}
	u := usage{OK: true, PlanType: planType, AgeSec: -1}
	now := time.Now().UTC()
	toWin := func(x *recordedWindow) *usageWindow {
		if x == nil {
			return nil
		}
		return adjustWindow(x.UsedPercent, x.WindowMinutes, x.ResetsAt, now)
	}
	// Classify by duration first. primary/secondary are ordering slots, not stable
	// names: the backend may return a weekly-only limit in primary. Keep a positional
	// fallback for old or unusual payloads that omit/introduce an unknown duration.
	type candidate struct {
		window   *usageWindow
		minutes  int
		primary  bool
		assigned bool
	}
	candidates := []candidate{
		{window: toWin(primary), minutes: windowMinutes(primary), primary: true},
		{window: toWin(secondary), minutes: windowMinutes(secondary)},
	}
	for i := range candidates {
		c := &candidates[i]
		if c.window == nil {
			c.assigned = true
			continue
		}
		switch {
		case isApproxWindow(c.minutes, 5*60) && u.FiveHour == nil:
			u.FiveHour, c.assigned = c.window, true
		case isApproxWindow(c.minutes, 7*24*60) && u.SevenDay == nil:
			u.SevenDay, c.assigned = c.window, true
		}
	}
	for i := range candidates {
		c := &candidates[i]
		if c.assigned {
			continue
		}
		if c.primary && u.FiveHour == nil {
			u.FiveHour = c.window
		} else if !c.primary && u.SevenDay == nil {
			u.SevenDay = c.window
		} else if u.FiveHour == nil {
			u.FiveHour = c.window
		} else if u.SevenDay == nil {
			u.SevenDay = c.window
		}
	}
	return u, true
}

func windowMinutes(w *recordedWindow) int {
	if w == nil {
		return 0
	}
	return w.WindowMinutes
}

// isApproxWindow matches Codex's own display tolerance: window durations within
// 5% of a known duration receive that label.
func isApproxWindow(minutes, expected int) bool {
	if minutes <= 0 || expected <= 0 {
		return false
	}
	diff := minutes - expected
	if diff < 0 {
		diff = -diff
	}
	return diff*20 <= expected
}

// adjustWindow maps one recorded rate-limit window onto a usageWindow, correcting
// for staleness. A codex reading is a snapshot from the last turn; if its window has
// since reset (resetEpoch is at/before now), the recorded % no longer applies — usage
// is back to 0 — so we zero it and roll the reset forward by whole windows to the next
// sensible boundary. A window still in the future passes through unchanged.
func adjustWindow(pct float64, windowMin int, resetEpoch int64, now time.Time) *usageWindow {
	reset := time.Unix(resetEpoch, 0).UTC()
	if !reset.After(now) {
		pct = 0
		if windowMin > 0 {
			step := time.Duration(windowMin) * time.Minute
			for !reset.After(now) {
				reset = reset.Add(step)
			}
		}
	}
	return &usageWindow{Pct: pct, ResetsAt: reset.Format(time.RFC3339)}
}

// ageSecFrom returns whole seconds since an RFC3339 instant, or -1 if unparseable.
func ageSecFrom(iso string) int {
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return -1
	}
	if d := time.Since(t); d >= 0 {
		return int(d.Seconds())
	}
	return 0
}
