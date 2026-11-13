package routegw

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// The route issuing credential and the POST body must not follow a CP redirect.
func TestCPRoutesPostRefusesRedirect(t *testing.T) {
	var otherHits atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { otherHits.Add(1) }))
	defer other.Close()
	for _, code := range []int{301, 302, 307, 308} {
		cp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, other.URL+"/stolen", code)
		}))
		t.Setenv("AF_CP_BASE_URL", cp.URL)
		t.Setenv("AF_CP_INTERNAL_URL", "")
		t.Setenv("AF_ROUTE_ISSUE_TOKEN", "route-secret")
		var out CPUsable
		ref := newCPRoutes().(cpRoutes).post(context.Background(), "/internal/route/check", map[string]string{"route": "r"}, &out)
		cp.Close()
		if ref == nil {
			t.Fatalf("%d: a redirected POST succeeded", code)
		}
		if n := otherHits.Load(); n != 0 {
			t.Fatalf("%d: the other origin received %d request(s)", code, n)
		}
	}
}
