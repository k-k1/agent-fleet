package routegw

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The wire status decides what Claude Code prints: a 401 comes under its "AWS authentication
// failed · refresh your AWS credentials" line (measured, #3891), a 403 is retried about seven
// times, and 412 / 503 with x-should-retry: false go out once and show the message alone.
func TestWireStatusKeepsAuthAs401AndMovesPermissionRefusalsOffIt(t *testing.T) {
	for _, c := range []struct {
		name         string
		in, wantWire int
	}{
		{"authentication stays 401", http.StatusUnauthorized, http.StatusUnauthorized},
		{"forbidden is not an AWS login problem", http.StatusForbidden, http.StatusPreconditionFailed},
		{"conflict is not an AWS login problem", http.StatusConflict, http.StatusPreconditionFailed},
		{"server side is 503", http.StatusBadGateway, http.StatusServiceUnavailable},
		{"bad request keeps its status", http.StatusBadRequest, http.StatusBadRequest},
		{"unknown operation keeps its status", http.StatusNotFound, http.StatusNotFound},
	} {
		if got := wireStatus(c.in); got != c.wantWire {
			t.Errorf("%s: wireStatus(%d) = %d, want %d", c.name, c.in, got, c.wantWire)
		}
	}
	rec := httptest.NewRecorder()
	writeRefusal(rec, &refusal{http.StatusForbidden, "route_connection_forbidden", "not marked"})
	if rec.Code != http.StatusPreconditionFailed || rec.Header().Get("X-Should-Retry") != "false" {
		t.Fatalf("refusal = %d, x-should-retry %q", rec.Code, rec.Header().Get("X-Should-Retry"))
	}
}
