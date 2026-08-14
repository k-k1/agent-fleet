package claude

import (
	"net/http"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// Claude subscription usage (the 5-hour + weekly bars claude shows in its /usage view)
// surfaced for the Console's WsBar chip. Source: the rate_limits block claude passes to
// its statusLine command on every render, captured locally by `workspace-agent
// statusline` (see statusline.go). There is NO network call — the previous approach hit
// the unofficial /api/oauth/usage endpoint, which is aggressively IP-rate-limited (429)
// and hid the chip. This local read mirrors how codex reads its rate_limits.

// usageWindow is one limit window: percent used (0–100) and the ISO reset instant
// (the Console formats it as a relative "あとN時間/N日" + an absolute date-time).
type usageWindow struct {
	Pct      float64 `json:"pct"`
	ResetsAt string  `json:"resetsAt"`
}

// HandleUsage serves GET /claude/usage for the Console's WsBar chip. It converts the
// last captured rate_limits into the chip's window shape; `authed` reflects whether a
// subscription token is present, so the Console keeps the chip visible (degraded) even
// before the first capture (a fresh session that hasn't made an API call yet).
func HandleUsage(w http.ResponseWriter, r *http.Request) {
	authed := oauthToken() != ""
	cap, at := readCapturedUsage()

	out := map[string]any{"ok": false, "authed": authed}
	// Plan (subscription tier) + account for the chip's popover — the rate_limits capture
	// has neither, so read them from `claude auth status` (cached). Only when signed in.
	if authed {
		if p := Plan(); p != "" {
			out["plan"] = p
		}
		if u := Account(); u != "" {
			out["user"] = u
		}
	}
	if cap != nil {
		now := time.Now().UTC()
		var fh, sd *usageWindow
		if cap.FiveHour != nil && cap.FiveHour.ResetsAt > 0 {
			fh = adjustWindow(cap.FiveHour.UsedPercent, 5*60, cap.FiveHour.ResetsAt, now)
		}
		if cap.SevenDay != nil && cap.SevenDay.ResetsAt > 0 {
			sd = adjustWindow(cap.SevenDay.UsedPercent, 7*24*60, cap.SevenDay.ResetsAt, now)
		}
		out["fiveHour"], out["sevenDay"] = fh, sd
		if fh != nil || sd != nil {
			out["ok"] = true
			if !at.IsZero() {
				out["ageSec"] = int(time.Since(at).Seconds())
			}
		}
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// adjustWindow maps one captured rate-limit window onto a usageWindow, correcting for
// staleness. A capture is a snapshot from the last statusLine render; if its window has
// since reset (resetEpoch is at/before now), the recorded % no longer applies — usage is
// back to 0 — so we zero it and roll the reset forward by whole windows to the next
// boundary. A window still in the future passes through unchanged. (Same semantics as
// codex's adjustWindow.)
func adjustWindow(pct float64, windowMin int, resetEpoch int64, now time.Time) *usageWindow {
	reset := time.Unix(resetEpoch, 0).UTC()
	if !reset.After(now) {
		pct = 0
		if windowMin > 0 {
			step := time.Duration(windowMin) * time.Minute
			// Advance directly (not a loop) so a very stale capture can't spin.
			n := now.Sub(reset)/step + 1
			reset = reset.Add(step * n)
		}
	}
	return &usageWindow{Pct: pct, ResetsAt: reset.Format(time.RFC3339)}
}

// oauthToken reads the subscription OAuth access token from the credentials file claude
// maintains under its config dir. "" when absent. Used only as the "signed in" signal
// for the chip — we no longer call any usage endpoint with it. The file itself is read
// (and stat-cached) by authexpiry.go, which owns the one parse of it.
func oauthToken() string {
	c, ok := readCreds()
	if !ok {
		return ""
	}
	return c.ClaudeAiOauth.AccessToken
}
