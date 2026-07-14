package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
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

// usageWindow は internal/agents/claude（usage.go）の同名 struct の複製（極小の
// ため共有せず重複を許容）: percent used (0–100) + the ISO reset instant.
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
	u := readUsage()
	out := map[string]any{"ok": u.OK, "fiveHour": u.FiveHour, "sevenDay": u.SevenDay}
	if resets, ok := fetchResetCredits(r.Context()); ok {
		out["resetCredits"] = resets
		// Reset credits alone are enough to keep the Codex WS-bar chip visible.
		out["ok"] = true
	}
	if u.PlanType != "" {
		out["planType"] = u.PlanType
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
}

const resetCreditsURL = "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits"

func fetchResetCredits(ctx context.Context) (resetCredits, bool) {
	b, err := os.ReadFile(filepath.Join(paths.HomeDir(), ".codex", "auth.json"))
	if err != nil {
		return resetCredits{}, false
	}
	var auth struct {
		AuthMode string `json:"auth_mode"`
		Tokens   struct {
			AccessToken string `json:"access_token"`
			AccountID   string `json:"account_id"`
		} `json:"tokens"`
	}
	if json.Unmarshal(b, &auth) != nil || auth.AuthMode != "chatgpt" || auth.Tokens.AccessToken == "" {
		return resetCredits{}, false
	}
	return getResetCredits(ctx, http.DefaultClient, resetCreditsURL, auth.Tokens.AccessToken, auth.Tokens.AccountID)
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

// readUsage finds the freshest rate_limits across codex rollouts. rate_limits are
// account-wide, so the most recently written rollout that carries one wins — we iterate
// rollout files newest-first and return the first reading found.
func readUsage() usage {
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
	// Both windows can be absent very early; require at least one to call it a reading.
	if rl.Primary == nil && rl.Secondary == nil {
		return usage{}, false
	}
	u := usage{OK: true, PlanType: rl.PlanType, AgeSec: -1}
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
		{window: toWin(rl.Primary), minutes: windowMinutes(rl.Primary), primary: true},
		{window: toWin(rl.Secondary), minutes: windowMinutes(rl.Secondary)},
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
