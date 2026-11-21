package main

import (
	"strings"
	"testing"
)

// The token face (AF_MEMO_TOKEN) is reachable from every session of a workspace, so the queue
// operations that change what unattended sessions are told, or that approve memos for them,
// must not be on it (ADR 0137 section 3 and the 2026-11-21 addendum).
func TestMemoQueueTokenFaceHasNoSettingsWriteReleaseOrReturn(t *testing.T) {
	_, mux := smokeEnvWith(t, allRouteSwitches(t)...)
	routes := muxRoutes(t, mux)

	want := map[string]bool{
		"GET /internal/memo-queues":                                   false,
		"POST /internal/memo-queues/{categoryId}/fire":                false,
		"POST /internal/memo-queues/{categoryId}/resume":              false,
		"GET /internal/memo-queues/{categoryId}/deliveries":           false,
		"POST /internal/memo-queues/{categoryId}/deliveries/{id}/end": false,
	}
	for _, r := range routes {
		if _, ok := want[r]; ok {
			want[r] = true
		}
		if !strings.Contains(r, " /internal/") {
			continue
		}
		method := strings.SplitN(r, " ", 2)[0]
		switch {
		case strings.Contains(r, "/internal/memo-queues") && method != "GET" &&
			!strings.HasSuffix(r, "/fire") && !strings.HasSuffix(r, "/resume") && !strings.HasSuffix(r, "/end"):
			t.Errorf("settings write or return on the token face: %s", r)
		case strings.Contains(r, "release"), strings.Contains(r, "/return"):
			t.Errorf("release or return on the token face: %s", r)
		}
	}
	for r, found := range want {
		if !found {
			t.Errorf("missing token-face route %s", r)
		}
	}
}
