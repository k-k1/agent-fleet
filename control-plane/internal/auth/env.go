package auth

import (
	"os"
	"strings"
	"time"
)

// The provider adapters read their own configuration out of the environment
// (AF_OIDC_<ID>_*, GITHUB_OAUTH_*), so these four parsers have to be reachable
// from here.
//
// ★ They are copies of the identically-named helpers in control-plane/main.go,
// not a move: main.go belongs to no track in this refactor wave and the whole
// Control Plane calls them, so relocating them was not this transport's to make
// (ADR 0067 §1 ②). They are pure, four lines each, and have no configuration of
// their own — but they ARE duplication, and the reclaim session that folds the
// aliases away should fold these back together too.

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func parseDurationOr(s string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(strings.TrimSpace(s)); err == nil && d > 0 {
		return d
	}
	return def
}

func emailSet(s string) map[string]bool {
	m := map[string]bool{}
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(strings.ToLower(p)); p != "" {
			m[p] = true
		}
	}
	return m
}

func domainSet(s string) map[string]bool {
	m := map[string]bool{}
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimPrefix(strings.TrimSpace(strings.ToLower(p)), "@")
		if p != "" {
			m[p] = true
		}
	}
	return m
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
