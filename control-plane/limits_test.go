package main

import "testing"

func TestParseLimitsTerminalHistoryRetention(t *testing.T) {
	got := parseLimits(`{"terminal_history_retention_days":7}`)
	if got.TerminalHistoryRetentionDays != 7 {
		t.Fatalf("retention = %d; want 7", got.TerminalHistoryRetentionDays)
	}
	if zero := parseLimits("").TerminalHistoryRetentionDays; zero != 0 {
		t.Fatalf("default retention = %d; want 0", zero)
	}
}
