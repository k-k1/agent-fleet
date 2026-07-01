package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Claude subscription usage (the 5-hour + weekly bars claude shows in its /usage
// view) surfaced for the Console's WsBar. There is NO supported API for this: we
// read the OAuth access token claude stores in <config>/.credentials.json and call
// the same undocumented endpoint claude's own /usage view uses. Unofficial and
// best-effort — any failure (no token, expired, endpoint/shape change) returns
// {ok:false} so the Console simply hides the chip instead of erroring. Cached
// briefly so the Console's poll can't hammer the endpoint (it rate-limits).

const claudeUsageURL = "https://api.anthropic.com/api/oauth/usage"
const claudeUsageTTL = 60 * time.Second

// usageWindow is one limit window: percent used (0–100) and the ISO reset instant
// (the Console formats it as a relative "あとN時間/N日" + an absolute date-time).
type usageWindow struct {
	Pct      float64 `json:"pct"`
	ResetsAt string  `json:"resetsAt"`
}

type claudeUsage struct {
	OK       bool         `json:"ok"`
	FiveHour *usageWindow `json:"fiveHour,omitempty"`
	SevenDay *usageWindow `json:"sevenDay,omitempty"`
}

var (
	usageMu  sync.Mutex
	usageAt  time.Time
	usageVal claudeUsage
)

func handleClaudeUsage(w http.ResponseWriter, r *http.Request) {
	usageMu.Lock()
	if !usageAt.IsZero() && time.Since(usageAt) < claudeUsageTTL {
		v := usageVal
		usageMu.Unlock()
		writeJSON(w, http.StatusOK, v)
		return
	}
	usageMu.Unlock()

	v := fetchClaudeUsage(r.Context())

	usageMu.Lock()
	usageVal, usageAt = v, time.Now()
	usageMu.Unlock()
	writeJSON(w, http.StatusOK, v)
}

// claudeOAuthToken reads the subscription OAuth access token from the credentials
// file claude maintains (and refreshes) under its config dir. "" when absent.
func claudeOAuthToken() string {
	b, err := os.ReadFile(filepath.Join(claudeConfigDir(), ".credentials.json"))
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

func fetchClaudeUsage(ctx context.Context) claudeUsage {
	tok := claudeOAuthToken()
	if tok == "" {
		return claudeUsage{OK: false}
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, claudeUsageURL, nil)
	if err != nil {
		return claudeUsage{OK: false}
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("User-Agent", "agent-fleet-console")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return claudeUsage{OK: false}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return claudeUsage{OK: false}
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
		return claudeUsage{OK: false}
	}
	out := claudeUsage{OK: true}
	if raw.FiveHour != nil {
		out.FiveHour = &usageWindow{Pct: raw.FiveHour.Utilization, ResetsAt: raw.FiveHour.ResetsAt}
	}
	if raw.SevenDay != nil {
		out.SevenDay = &usageWindow{Pct: raw.SevenDay.Utilization, ResetsAt: raw.SevenDay.ResetsAt}
	}
	return out
}
