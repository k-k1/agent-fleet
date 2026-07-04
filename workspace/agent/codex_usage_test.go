package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A real token_count line captured from a live codex rollout (2026-07-04).
const codexRateLimitLine = `{"timestamp":"2026-07-04T03:10:38.864Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":297059,"cached_input_tokens":226304,"output_tokens":3320,"reasoning_output_tokens":867,"total_tokens":300379},"last_token_usage":{"input_tokens":38619,"cached_input_tokens":24448,"output_tokens":540,"reasoning_output_tokens":84,"total_tokens":39159},"model_context_window":258400},"rate_limits":{"limit_id":"codex","limit_name":null,"primary":{"used_percent":3.0,"window_minutes":300,"resets_at":1783142674},"secondary":{"used_percent":0.0,"window_minutes":10080,"resets_at":1783729474},"credits":null,"individual_limit":null,"plan_type":"plus","rate_limit_reached_type":null}}}`

func TestCodexRateLimits(t *testing.T) {
	// Parse via the same path handleCodexUsage uses. The recorded %s are time-adjusted
	// (see TestCodexAdjustWindow), so here we only assert the shape parses.
	u, ok := codexUsageFromRolloutBytes([]byte(codexRateLimitLine))
	if !ok || !u.OK {
		t.Fatalf("expected a usage reading, got ok=%v u=%+v", ok, u)
	}
	if u.FiveHour == nil || u.SevenDay == nil {
		t.Fatalf("both windows expected, got %+v", u)
	}
	if u.PlanType != "plus" {
		t.Fatalf("planType = %q, want plus", u.PlanType)
	}
	if u.FiveHour.ResetsAt == "" {
		t.Fatalf("fiveHour.ResetsAt empty")
	}
}

func TestCodexAdjustWindow(t *testing.T) {
	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)

	// A window still in the future passes through unchanged.
	future := now.Add(2 * time.Hour).Unix()
	if w := codexAdjustWindow(37.0, 300, future, now); w.Pct != 37.0 {
		t.Fatalf("live window pct = %v, want 37.0", w.Pct)
	}

	// An expired window (reset in the past) zeroes the %, and rolls the reset forward
	// by whole windows to a future instant.
	past := now.Add(-7 * time.Hour).Unix() // 5h window reset 7h ago
	w := codexAdjustWindow(3.0, 300, past, now)
	if w.Pct != 0 {
		t.Fatalf("expired window pct = %v, want 0", w.Pct)
	}
	rt, err := time.Parse(time.RFC3339, w.ResetsAt)
	if err != nil || !rt.After(now) {
		t.Fatalf("expired reset = %q (err=%v), want a future instant", w.ResetsAt, err)
	}
}

// A line with no rate_limits must not be read as a usage reading.
func TestCodexRateLimitsAbsent(t *testing.T) {
	line := `{"timestamp":"2026-07-04T03:10:38.864Z","type":"event_msg","payload":{"type":"token_count","info":{"model_context_window":258400}}}`
	if _, ok := codexUsageFromRolloutBytes([]byte(line)); ok {
		t.Fatalf("expected no reading for a rate_limits-free line")
	}
}

// readCodexUsage picks the freshest reading from the newest rollout on disk.
func TestReadCodexUsageFromDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	roll := filepath.Join(dir, ".codex", "sessions", "2026", "07", "04")
	if err := os.MkdirAll(roll, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(roll, "rollout-2026-07-04T09-24-19-abc.jsonl"), []byte(codexRateLimitLine+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	u := readCodexUsage()
	if !u.OK || u.FiveHour == nil || u.SevenDay == nil {
		t.Fatalf("readCodexUsage = %+v, want ok with both windows", u)
	}
}
