package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Codex subscription usage (the 5-hour + weekly rate-limit bars) surfaced for the
// Console's WsBar, mirroring the Claude usage chip. Unlike Claude — which needs an
// undocumented network call — codex records its own rate limits INTO the rollout
// JSONL: every `token_count` event carries a `rate_limits` block. So we just read the
// newest rollout, take the last rate_limits reading, and map it onto the same
// {ok, fiveHour, sevenDay} shape the Claude endpoint returns (so the Console renders
// both chips with one code path). No token, no network, best-effort — any miss returns
// {ok:false} and the Console hides the chip.
//
// rate_limits shape (verified against real rollouts):
//   "rate_limits":{"primary":{"used_percent":3.0,"window_minutes":300,"resets_at":<epoch>},
//                  "secondary":{"used_percent":0.0,"window_minutes":10080,"resets_at":<epoch>},
//                  "plan_type":"plus"}
// primary = the ~5h window, secondary = the weekly (10080 min = 7d) window.

func handleCodexUsage(w http.ResponseWriter, _ *http.Request) {
	u := readCodexUsage()
	out := map[string]any{"ok": u.OK, "fiveHour": u.FiveHour, "sevenDay": u.SevenDay}
	if u.PlanType != "" {
		out["planType"] = u.PlanType
	}
	if u.AgeSec >= 0 {
		out["ageSec"] = u.AgeSec
	}
	writeJSON(w, http.StatusOK, out)
}

type codexUsage struct {
	OK       bool
	FiveHour *usageWindow
	SevenDay *usageWindow
	PlanType string
	AgeSec   int
}

// readCodexUsage finds the freshest rate_limits across codex rollouts. rate_limits are
// account-wide, so the most recently written rollout that carries one wins — we iterate
// rollout files newest-first and return the first reading found.
func readCodexUsage() codexUsage {
	root := filepath.Join(homeDir(), ".codex", "sessions")
	matches, err := filepath.Glob(filepath.Join(root, "*", "*", "*", "rollout-*.jsonl"))
	if err != nil || len(matches) == 0 {
		return codexUsage{OK: false, AgeSec: -1}
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
		if u, ok := codexUsageFromRollout(path); ok {
			return u
		}
	}
	return codexUsage{OK: false, AgeSec: -1}
}

// codexUsageFromRollout scans a rollout file from the end for the last token_count
// event bearing a rate_limits block. ok is false when the file has no such reading yet.
func codexUsageFromRollout(path string) (codexUsage, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return codexUsage{}, false
	}
	return codexUsageFromRolloutBytes(b)
}

// codexUsageFromRolloutBytes is the parse over rollout content (split out for tests).
func codexUsageFromRolloutBytes(b []byte) (codexUsage, bool) {
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
		if u, ok := codexRateLimits(ev.Payload); ok {
			u.AgeSec = ageSecFrom(ev.Timestamp)
			return u, true
		}
	}
	return codexUsage{}, false
}

// codexRateLimits parses the rate_limits from a token_count payload into the shared
// usageWindow shape. ok is false for payloads without a rate_limits block.
func codexRateLimits(payload json.RawMessage) (codexUsage, bool) {
	type win struct {
		UsedPercent float64 `json:"used_percent"`
		ResetsAt    int64   `json:"resets_at"` // unix epoch seconds
	}
	var p struct {
		Type       string `json:"type"`
		RateLimits *struct {
			Primary   *win   `json:"primary"`
			Secondary *win   `json:"secondary"`
			PlanType  string `json:"plan_type"`
		} `json:"rate_limits"`
	}
	if json.Unmarshal(payload, &p) != nil || p.Type != "token_count" || p.RateLimits == nil {
		return codexUsage{}, false
	}
	rl := p.RateLimits
	// Both windows can be absent very early; require at least one to call it a reading.
	if rl.Primary == nil && rl.Secondary == nil {
		return codexUsage{}, false
	}
	u := codexUsage{OK: true, PlanType: rl.PlanType, AgeSec: -1}
	toWin := func(x *win) *usageWindow {
		if x == nil {
			return nil
		}
		return &usageWindow{Pct: x.UsedPercent, ResetsAt: time.Unix(x.ResetsAt, 0).UTC().Format(time.RFC3339)}
	}
	u.FiveHour = toWin(rl.Primary)
	u.SevenDay = toWin(rl.Secondary)
	return u, true
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
