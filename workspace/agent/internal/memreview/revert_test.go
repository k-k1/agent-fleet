package memreview

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The revert route is a member write: without the Control Plane's mark nothing reaches the store,
// and a malformed run id is refused before it does.
func TestRevertRunRefusesAnUnmarkedRequest(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/agents/memory-review/runs/revert", strings.NewReader(`{"run":"aaaaaaaaaaaaaaaaaaaa"}`))
	r.Header.Set("Content-Type", "application/json")
	HandleRevertRun(w, r)
	if w.Code != http.StatusForbidden && w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d: %s", w.Code, w.Body)
	}
}
