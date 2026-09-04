package envx

// Package envx holds the Control Plane's environment-variable parsers, so that each one
// has exactly one implementation: `main` and `internal/auth` both call these instead of
// keeping copies that can drift (ADR 0067 §1).
//
// What belongs here: pure, configuration-free helpers that are not auth-specific. envBool
// has only ever had one caller in main.go, so it stays there.

import (
	"os"
	"strings"
	"time"
)

func Or(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func DurationOr(s string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(strings.TrimSpace(s)); err == nil && d > 0 {
		return d
	}
	return def
}

// EmailSet parses a CSV of emails into a lowercased lookup set (SUPER_ADMIN_EMAILS).
func EmailSet(s string) map[string]bool {
	m := map[string]bool{}
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(strings.ToLower(p)); p != "" {
			m[p] = true
		}
	}
	return m
}

// DomainSet parses a CSV of email domains into a lowercased lookup set, tolerating
// a leading "@" on each entry (AF_OAUTH_ALLOWED_DOMAINS).
func DomainSet(s string) map[string]bool {
	m := map[string]bool{}
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimPrefix(strings.TrimSpace(strings.ToLower(p)), "@")
		if p != "" {
			m[p] = true
		}
	}
	return m
}

// SplitCSV parses "A=1,B=2" into ["A=1","B=2"], dropping blanks.
func SplitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
