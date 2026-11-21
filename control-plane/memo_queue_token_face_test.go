package main

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

// The token face (AF_MEMO_TOKEN) is in the environment of every session of a workspace. Under
// /internal/memo-queues it carries exactly two reads; a write, a release, a return, a fire, a
// resume or an end there would let a run that read an injected memo do what only the person's
// Console login may do (ADR 0137, addendum 2026-11-21). Exact allowlist, so a new route fails
// here until someone argues for it.
func TestMemoQueueTokenFaceIsTheReadOnlyAllowlist(t *testing.T) {
	_, mux := smokeEnvWith(t, allRouteSwitches(t)...)
	var got []string
	for _, r := range muxRoutes(t, mux) {
		if strings.Contains(r, " /internal/memo-queues") {
			got = append(got, r)
		}
	}
	want := []string{
		"GET /internal/memo-queues",
		"GET /internal/memo-queues/{categoryId}/deliveries",
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("routes under /internal/memo-queues:\n got %v\nwant %v", got, want)
	}
	for _, r := range muxRoutes(t, mux) {
		if strings.Contains(r, " /internal/") && (strings.Contains(r, "release") || strings.Contains(r, "/return")) {
			t.Errorf("release or return on the token face: %s", r)
		}
	}
}

// The token face reads the history without the sessions' hand-back reasons, which the Console
// shows to the person (ADR 0137), and only for the token's own member.
func TestMemoQueueTokenFaceDeliveriesHideDeferReasonAndAreScoped(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		_, d, _ := e.openDeferrable(map[string]any{"enabled": true})
		const reason = "waiting for #12 to merge"
		rec := e.deferCall(d.ID, map[string]any{"session": d.Session, "reason": reason,
			"notBefore": time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)})
		if rec.Code != http.StatusOK {
			t.Fatalf("defer: %d %s", rec.Code, rec.Body.String())
		}
		path := "/memo-queues/" + d.QueueID + "/deliveries"

		// Positive control: the Console list carries the reason.
		con := e.do("a", "GET", "/api"+path, nil)
		if con.Code != http.StatusOK || !strings.Contains(con.Body.String(), reason) {
			t.Fatalf("console list lacks the reason (%d): %s", con.Code, con.Body.String())
		}
		tok := e.memoTokenDo("a", "GET", "/internal"+path, nil)
		if tok.Code != http.StatusOK || !strings.Contains(tok.Body.String(), d.ID) {
			t.Fatalf("token list (%d): %s", tok.Code, tok.Body.String())
		}
		if strings.Contains(tok.Body.String(), reason) {
			t.Errorf("the token face carries another session's reason: %s", tok.Body.String())
		}
		if !strings.Contains(tok.Body.String(), `"defer":{`) {
			t.Errorf("the token face dropped the hand-back itself: %s", tok.Body.String())
		}

		// Member b's valid token does not read a's history; a bad token reads nothing.
		if r := e.memoTokenDo("b", "GET", "/internal"+path, nil); strings.Contains(r.Body.String(), d.ID) {
			t.Errorf("member b read a's run: %d %s", r.Code, r.Body.String())
		}
		req := httptest.NewRequest("GET", "/internal"+path, nil)
		req.Header.Set("Authorization", "Bearer not-a-token")
		bad := httptest.NewRecorder()
		e.mux.ServeHTTP(bad, req)
		if bad.Code != http.StatusUnauthorized || strings.Contains(bad.Body.String(), d.ID) {
			t.Errorf("bad token: %d %s", bad.Code, bad.Body.String())
		}
		for _, m := range []string{"POST", "PUT", "DELETE", "PATCH"} {
			for _, p := range []string{"", "/fire", "/resume", "/deliveries/" + d.ID + "/end", "/deliveries/" + d.ID + "/return"} {
				if r := e.memoTokenDo("a", m, "/internal/memo-queues/"+d.QueueID+p, map[string]any{"instruction": "x"}); r.Code < 400 {
					t.Errorf("%s /internal/memo-queues/%s%s answered %d", m, d.QueueID, p, r.Code)
				}
			}
		}
	})
}
