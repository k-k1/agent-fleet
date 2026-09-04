// Package agy integrates Google's Antigravity CLI (`agy`) as the fourth agent
// kind (docs/log/32, ADR 0008). This file owns the quota scrape (Track C): agy has
// no structured usage query, so we drive the TUI under a PTY (the shared
// agents.Flow plumbing), send /usage, and parse the quota panel. Launch / auth /
// status live in this package's other files (Track A).
package agy

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// The /usage panel is model-group based (ADR 0008, measured): two pools ("GEMINI
// MODELS" and "CLAUDE AND GPT MODELS") that each show REMAINING percentage
// bars and a "Refreshes in 167h 27m" line ("Quota available" at 100%). Starter
// shows one "Weekly Limit" bar per group; paid tiers (AI Pro, measured, docs/log/32
// D-4) add a "Five Hour Limit" bar — 2 groups × 2 limits = the AgyCard's four
// gauges. The scrape is the Console's only source for the remaining-quota % display.

// UsageLimit is one remaining-quota bar within a group ("Five Hour Limit").
type UsageLimit struct {
	RemainingPct float64 `json:"remainingPct"` // 0–100, remaining (not used)
	ResetsAt     string  `json:"resetsAt,omitempty"`
}

// UsageGroup is one model-group pool as shown by /usage. The flat fields carry
// the weekly limit (the only one Starter has — wire shape kept from M1);
// FiveHour is present on tiers whose panel shows the extra bar.
type UsageGroup struct {
	Label        string      `json:"label"`            // section header, e.g. "GEMINI MODELS"
	Models       string      `json:"models,omitempty"` // "Gemini Flash, Gemini Pro"
	RemainingPct float64     `json:"remainingPct"`     // weekly remaining
	ResetsAt     string      `json:"resetsAt,omitempty"`
	FiveHour     *UsageLimit `json:"fiveHour,omitempty"`
}

type usageResult struct {
	Account string       `json:"account,omitempty"`
	Plan    string       `json:"plan,omitempty"`
	Groups  []UsageGroup `json:"groups"`
}

// Spawning the TUI costs seconds; /usage output changes slowly, so cache it and
// let the Console force a refresh explicitly (?refresh=1).
const usageCacheTTL = 5 * time.Minute

var (
	usageMu     sync.Mutex
	usageCached *usageResult
	usageAt     time.Time
)

// tokenPath is where agy persists its OAuth token when no keyring exists
// (ADR 0008: plaintext under home — also on the fs denylist).
func tokenPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".gemini", "antigravity-cli", "antigravity-oauth-token")
}

// TokenPath exposes the token file location for the assistant chat's isolated
// HOME, which shares ONLY the login via a symlink to this file (chatAgyHome).
func TokenPath() string { return tokenPath() }

// SignedIn reports whether agy has a persisted OAuth token. The docs/log/32 contract
// pairs this with `agy models` for status; token presence is enough for the
// usage endpoint's cheap authed gate.
func SignedIn() bool {
	fi, err := os.Stat(tokenPath())
	return err == nil && !fi.IsDir()
}

// HandleUsage serves GET /connections/agy/usage for the Console's AgyCard.
// Always 200 with {ok, authed, …} so the card can degrade in place.
func HandleUsage(w http.ResponseWriter, r *http.Request) {
	if !SignedIn() {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": false, "authed": false})
		return
	}
	usageMu.Lock()
	defer usageMu.Unlock()
	if usageCached == nil || time.Since(usageAt) > usageCacheTTL || r.URL.Query().Get("refresh") != "" {
		res, err := scrapeUsage()
		if err != nil {
			out := map[string]any{"ok": false, "authed": true, "error": err.Error()}
			// Serve the stale cache alongside the error so a transient scrape
			// failure doesn't blank an already-shown gauge.
			if usageCached != nil {
				out["ok"] = true
				out["account"], out["plan"], out["groups"] = usageCached.Account, usageCached.Plan, usageCached.Groups
				out["ageSec"] = int(time.Since(usageAt).Seconds())
			}
			httpx.WriteJSON(w, http.StatusOK, out)
			return
		}
		usageCached, usageAt = res, time.Now()
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"authed":  true,
		"account": usageCached.Account,
		"plan":    usageCached.Plan,
		"groups":  usageCached.Groups,
		"ageSec":  int(time.Since(usageAt).Seconds()),
	})
}

var (
	// trustRe (the workspace-trust screen) is built from the question text in trustprompt.go.
	readyRe = regexp.MustCompile(`\? for shortcuts`)
	// The quota panel's footer — once it renders, both group sections are on screen.
	panelRe = regexp.MustCompile(`esc Close`)
	// Startup-header identity line: "k1.example@gmail.com (Antigravity Starter
	// Quota)". The plan suffix fills in asynchronously ~10s after startup, so a
	// normal scrape usually finishes before it renders — parsed best-effort only
	// (the AgyCard's "experimental tier" label is static and does not depend on it).
	planRe = regexp.MustCompile(`(\S+@\S+)\s+\(([^)]+)\)`)
	// The panel's own account line (no plan there).
	accountRe = regexp.MustCompile(`Account:\s*(\S+@\S+)`)
	// Section header, e.g. "GEMINI MODELS" / "CLAUDE AND GPT MODELS".
	groupRe = regexp.MustCompile(`(?m)^\s*([A-Z][A-Z0-9 /&+.-]* MODELS)\s*$`)
	// Limit sub-header within a group ("Weekly Limit" / "Five Hour Limit" —
	// the latter appears on paid tiers, docs/log/32 D-4).
	limitRe   = regexp.MustCompile(`(?m)^\s*(Weekly|Five Hour) Limit\s*$`)
	modelsRe  = regexp.MustCompile(`Models within this group:\s*([^\n]+)`)
	pctRe     = regexp.MustCompile(`\]\s*(\d+(?:\.\d+)?)%`)
	refreshRe = regexp.MustCompile(`Refreshes in\s*([0-9dhm ]+)`)
	durPartRe = regexp.MustCompile(`(\d+)\s*([dhm])`)
)

// scrapeUsage runs the agy TUI in a scratch dir, drives it to the /usage panel,
// and parses the group pools. The scratch dir keeps the workspace-trust prompt
// away from real working copies, and is pre-trusted so the prompt never renders
// (our own empty dir — nothing to trust but itself); answering it interactively
// is only the fallback, same as scrapeContext.
func scrapeUsage() (*usageResult, error) {
	dir := filepath.Join(os.TempDir(), "af-agy-usage")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	EnsureWorkspaceTrusted(dir)
	enforceTelemetryOff() // launch-time re-pin, same as BuildLaunch
	cmd := exec.Command("agy")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	f, err := agents.StartFlow(cmd)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// With trust granted beforehand this screen never appears, and even when it does, never
	// blind-press Enter: which row is selected by default changes upstream (trustprompt.go).
	if m := f.WaitFor(regexp.MustCompile(trustRe.String()+`|`+readyRe.String()), 25*time.Second); m == "" {
		return nil, errString("agy did not reach the prompt (timeout)")
	} else if trustRe.MatchString(m) {
		if err := answerTrustPrompt(f); err != nil {
			return nil, err
		}
		if f.WaitFor(readyRe, 20*time.Second) == "" {
			return nil, errString("agy did not reach the prompt after trust")
		}
	}
	// Type the slash command, then Enter as a separate keystroke (same Ink quirk
	// as the claude auth flow: a combined write drops the carriage return).
	if _, err := f.Ptmx.Write([]byte("/usage")); err != nil {
		return nil, err
	}
	time.Sleep(400 * time.Millisecond)
	if _, err := f.Ptmx.Write([]byte("\r")); err != nil {
		return nil, err
	}
	if f.WaitFor(panelRe, 20*time.Second) == "" {
		return nil, errString("usage panel did not render")
	}
	// One extra beat so the final full frame lands in the buffer before parsing.
	time.Sleep(500 * time.Millisecond)
	return parseUsage(f.Clean())
}

// parseUsage extracts the group pools from the cleaned PTY output. The TUI
// redraws constantly, so only the text from the LAST panel render is parsed.
func parseUsage(out string) (*usageResult, error) {
	res := &usageResult{}
	// Identity (email + plan) renders in the STARTUP header, which the
	// last-panel cut below discards — grab it from the full buffer first.
	if m := planRe.FindStringSubmatch(out); m != nil {
		res.Account, res.Plan = m[1], m[2]
	}
	if i := strings.LastIndex(out, "Models & Quota"); i >= 0 {
		out = out[i:]
	}
	if res.Account == "" {
		if m := accountRe.FindStringSubmatch(out); m != nil {
			res.Account = m[1]
		}
	}
	heads := groupRe.FindAllStringSubmatchIndex(out, -1)
	for i, h := range heads {
		sec := out[h[1]:]
		if i+1 < len(heads) {
			sec = out[h[1]:heads[i+1][0]]
		}
		g := UsageGroup{Label: strings.TrimSpace(out[h[2]:h[3]]), RemainingPct: -1}
		if m := modelsRe.FindStringSubmatch(sec); m != nil {
			g.Models = strings.TrimSpace(m[1])
		}
		// Split the section on its limit sub-headers. Starter has "Weekly Limit"
		// only; paid tiers add "Five Hour Limit" (docs/log/32 D-4). A panel with no
		// sub-headers at all (defensive: layout drift) falls back to treating the
		// whole section as the weekly limit.
		lims := limitRe.FindAllStringSubmatchIndex(sec, -1)
		if len(lims) == 0 {
			lims = [][]int{{0, 0, 0, 0}}
		}
		for j, lm := range lims {
			sub := sec[lm[1]:]
			if j+1 < len(lims) {
				sub = sec[lm[1]:lims[j+1][0]]
			}
			pct := -1.0
			if m := pctRe.FindStringSubmatch(sub); m != nil {
				pct, _ = strconv.ParseFloat(m[1], 64)
			}
			if pct < 0 {
				continue
			}
			var resets string
			if m := refreshRe.FindStringSubmatch(sub); m != nil {
				if d := parseRefreshDur(m[1]); d > 0 {
					resets = time.Now().Add(d).UTC().Format(time.RFC3339)
				}
			}
			if strings.TrimSpace(sec[lm[2]:lm[3]]) == "Five Hour" {
				g.FiveHour = &UsageLimit{RemainingPct: pct, ResetsAt: resets}
			} else {
				g.RemainingPct, g.ResetsAt = pct, resets
			}
		}
		if g.RemainingPct >= 0 {
			res.Groups = append(res.Groups, g)
		}
	}
	if len(res.Groups) == 0 {
		return nil, errString("no quota groups found in /usage output")
	}
	return res, nil
}

// parseRefreshDur turns "167h 27m" (optionally with a d part) into a Duration.
func parseRefreshDur(s string) time.Duration {
	var d time.Duration
	for _, m := range durPartRe.FindAllStringSubmatch(s, -1) {
		n, _ := strconv.Atoi(m[1])
		switch m[2] {
		case "d":
			d += time.Duration(n) * 24 * time.Hour
		case "h":
			d += time.Duration(n) * time.Hour
		case "m":
			d += time.Duration(n) * time.Minute
		}
	}
	return d
}

type errString string

func (e errString) Error() string { return string(e) }
