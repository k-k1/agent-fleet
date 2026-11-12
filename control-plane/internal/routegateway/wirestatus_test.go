package routegateway

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Same wire statuses as the Agent's gateway (workspace/agent/internal/routegw/wirestatus_test.go):
// the Agent passes the CP's answer through, and Claude Code prints every 401 under "AWS
// authentication failed · refresh your AWS credentials" (measured, #3891), so a grant that is off
// must not go out as one.
func TestWriteRouteRefusalKeepsPermissionRefusalsOff401(t *testing.T) {
	for _, c := range []struct {
		in, want int
		code     string
	}{
		{http.StatusUnauthorized, http.StatusUnauthorized, "route_token_invalid"},
		{http.StatusForbidden, http.StatusPreconditionFailed, "route_not_granted"},
		{http.StatusConflict, http.StatusPreconditionFailed, "route_connection_changed"},
		{http.StatusInternalServerError, http.StatusServiceUnavailable, "route_unavailable"},
		{http.StatusBadRequest, http.StatusBadRequest, "route_bad_request"},
	} {
		rec := httptest.NewRecorder()
		writeRouteRefusal(rec, &routeRefusal{status: c.in, code: c.code, msg: "m"})
		if rec.Code != c.want || rec.Header().Get("X-Should-Retry") != "false" || rec.Header().Get("X-Amzn-Errortype") != c.code {
			t.Errorf("%d %s: got %d, x-should-retry %q, errortype %q; want %d", c.in, c.code, rec.Code,
				rec.Header().Get("X-Should-Retry"), rec.Header().Get("X-Amzn-Errortype"), c.want)
		}
	}
}
