package agy

import (
	"strings"
	"testing"
	"time"
)

// A cleaned capture of the real v1.1.4 /usage panel (measured 2026-07-20 with a tmux probe),
// prefixed with an earlier partial frame to exercise the last-render cut.
const usagePanel = `
? for shortcuts
└ Models & Quota
GEMINI MODELS
  Weekly Limit
` + "└" + ` Models & Quota
  Account: dev@example.com
      dev@example.com (Antigravity Starter Quota)
GEMINI MODELS
  Models within this group: Gemini Flash, Gemini Pro
  Weekly Limit
    [████████████████████████████████████████████░░░░░░] 87.41%
    87% remaining · Refreshes in 167h 27m
CLAUDE AND GPT MODELS
  Models within this group: Claude Opus, Claude Sonnet, GPT-OSS
  Weekly Limit
    [██████████████████████████████████████████████████] 100.00%
    Quota available
  │Within each group, models share a weekly limit. Quota is consumed
  ↑/↓ Scroll · pgup/pgdown Page · ctrl+end Bottom · ctrl+home Top · esc Close
`

func TestParseUsage(t *testing.T) {
	res, err := parseUsage(usagePanel)
	if err != nil {
		t.Fatal(err)
	}
	if res.Account != "dev@example.com" {
		t.Errorf("account = %q", res.Account)
	}
	if res.Plan != "Antigravity Starter Quota" {
		t.Errorf("plan = %q", res.Plan)
	}
	if len(res.Groups) != 2 {
		t.Fatalf("groups = %d, want 2", len(res.Groups))
	}
	g := res.Groups[0]
	if g.Label != "GEMINI MODELS" || g.RemainingPct != 87.41 {
		t.Errorf("gemini group = %+v", g)
	}
	if g.Models != "Gemini Flash, Gemini Pro" {
		t.Errorf("gemini models = %q", g.Models)
	}
	if g.ResetsAt == "" {
		t.Error("gemini group missing resetsAt")
	} else if at, err := time.Parse(time.RFC3339, g.ResetsAt); err != nil {
		t.Errorf("resetsAt not RFC3339: %v", err)
	} else if d := time.Until(at); d < 167*time.Hour || d > 168*time.Hour {
		t.Errorf("resetsAt %v not ~167h27m out", d)
	}
	c := res.Groups[1]
	if c.Label != "CLAUDE AND GPT MODELS" || c.RemainingPct != 100 {
		t.Errorf("claude group = %+v", c)
	}
	if c.ResetsAt != "" {
		t.Errorf("full pool should have no resetsAt, got %q", c.ResetsAt)
	}
}

// A cleaned capture of the real v1.1.4 /usage panel on AI Pro (measured 2026-07-20 with a
// PTY probe after the D-4 upgrade): each group carries a Weekly Limit AND a
// Five Hour Limit bar, and the startup header no longer shows a plan suffix.
const usagePanelPro = `
? for shortcuts
└ Models & Quota
  Account: dev@example.com
GEMINI MODELS
  Models within this group: Gemini Flash, Gemini Pro
  Weekly Limit
    [██████████████████████████████████████████████████] 99.75%
    100% remaining · Refreshes in 167h 48m
  Five Hour Limit
    [█████████████████████████████████████████████████░] 98.48%
    98% remaining · Refreshes in 4h 48m
CLAUDE AND GPT MODELS
  Models within this group: Claude Opus, Claude Sonnet, GPT-OSS
  Weekly Limit
    [█████████████████████████████████████████████████░] 98.77%
    99% remaining · Refreshes in 167h 49m
  Five Hour Limit
    [████████████████████████████████████████████████░░] 96.31%
    96% remaining · Refreshes in 4h 49m
  │Within each group, models share a weekly limit and a 5-hour limit. Quota is
  ↑/↓ Scroll · pgup/pgdown Page · ctrl+end Bottom · ctrl+home Top · esc Close
`

func TestParseUsageProFourBars(t *testing.T) {
	res, err := parseUsage(usagePanelPro)
	if err != nil {
		t.Fatal(err)
	}
	if res.Account != "dev@example.com" {
		t.Errorf("account = %q", res.Account)
	}
	if res.Plan != "" {
		t.Errorf("plan = %q, want empty (Pro header has no plan suffix)", res.Plan)
	}
	if len(res.Groups) != 2 {
		t.Fatalf("groups = %d, want 2", len(res.Groups))
	}
	g := res.Groups[0]
	if g.Label != "GEMINI MODELS" || g.RemainingPct != 99.75 {
		t.Errorf("gemini weekly = %+v", g)
	}
	if g.FiveHour == nil || g.FiveHour.RemainingPct != 98.48 {
		t.Fatalf("gemini fiveHour = %+v", g.FiveHour)
	}
	if at, err := time.Parse(time.RFC3339, g.FiveHour.ResetsAt); err != nil {
		t.Errorf("fiveHour resetsAt not RFC3339: %v", err)
	} else if d := time.Until(at); d < 4*time.Hour || d > 5*time.Hour {
		t.Errorf("fiveHour resetsAt %v not ~4h48m out", d)
	}
	if at, err := time.Parse(time.RFC3339, g.ResetsAt); err != nil {
		t.Errorf("weekly resetsAt not RFC3339: %v", err)
	} else if d := time.Until(at); d < 167*time.Hour || d > 168*time.Hour {
		t.Errorf("weekly resetsAt %v not ~167h48m out", d)
	}
	c := res.Groups[1]
	if c.Label != "CLAUDE AND GPT MODELS" || c.RemainingPct != 98.77 {
		t.Errorf("claude weekly = %+v", c)
	}
	if c.FiveHour == nil || c.FiveHour.RemainingPct != 96.31 {
		t.Errorf("claude fiveHour = %+v", c.FiveHour)
	}
}

// The Starter panel (weekly only) must keep parsing with FiveHour absent —
// the wire shape the M1 AgyCard shipped with.
func TestParseUsageStarterNoFiveHour(t *testing.T) {
	res, err := parseUsage(usagePanel)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range res.Groups {
		if g.FiveHour != nil {
			t.Errorf("group %q has FiveHour = %+v, want nil on Starter", g.Label, g.FiveHour)
		}
	}
}

func TestParseUsageNoGroups(t *testing.T) {
	if _, err := parseUsage("? for shortcuts\nnothing here"); err == nil {
		t.Fatal("want error on output without quota groups")
	}
}

func TestParseRefreshDur(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want time.Duration
	}{
		{"167h 27m", 167*time.Hour + 27*time.Minute},
		{"6d 23h 5m", 6*24*time.Hour + 23*time.Hour + 5*time.Minute},
		{"45m", 45 * time.Minute},
		{"", 0},
	} {
		if got := parseRefreshDur(tc.in); got != tc.want {
			t.Errorf("parseRefreshDur(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// The section regex must not swallow the explanation prose between renders.
func TestGroupHeaderShape(t *testing.T) {
	if groupRe.MatchString("  Models within this group: Gemini Flash") {
		t.Error("groupRe matched a non-header line")
	}
	if !groupRe.MatchString("CLAUDE AND GPT MODELS") {
		t.Error("groupRe missed a header")
	}
	if strings.Contains(usagePanel, "\r") {
		t.Error("fixture should be pre-cleaned (no CR)")
	}
}
