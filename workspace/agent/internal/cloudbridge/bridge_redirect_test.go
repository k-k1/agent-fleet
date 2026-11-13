package cloudbridge

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// A CP that answers with a redirect must not receive the bridge token a second time at the
// target: the fetch fails (and Pull falls back to the cache) instead.
func TestFetchRedirectOffOriginSendsNoToken(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_CP_INTERNAL_URL", "")
	var otherHits atomic.Int32
	var otherAuth atomic.Value
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		otherHits.Add(1)
		otherAuth.Store(r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{"profiles":[{"name":"stolen"}]}`))
	}))
	defer other.Close()
	for _, code := range []int{301, 302, 307, 308} {
		var first atomic.Int32
		cp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			first.Add(1)
			http.Redirect(w, r, other.URL+r.URL.Path, code)
		}))
		t.Setenv("AF_CP_BASE_URL", cp.URL)
		t.Setenv("AF_TEST_PROFILES_TOKEN", "tok-secret")
		_, err := testBridge().Fetch()
		cp.Close()
		if err == nil {
			t.Fatalf("%d: a redirect off the CP's origin was followed", code)
		}
		if n := otherHits.Load(); n != 0 {
			t.Errorf("%d: the other origin received %d request(s), Authorization=%v", code, n, otherAuth.Load())
		}
		if n := first.Load(); n != 1 {
			t.Errorf("%d: CP saw %d requests, want 1", code, n)
		}
	}
}

func TestFetchFollowsSameOriginRedirect(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_CP_INTERNAL_URL", "")
	cp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/internal/test-profiles" {
			http.Redirect(w, r, "/internal/moved", http.StatusMovedPermanently)
			return
		}
		if r.Header.Get("Authorization") != "Bearer tok-a" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"profiles":[{"name":"prod"}]}`))
	}))
	defer cp.Close()
	t.Setenv("AF_CP_BASE_URL", cp.URL)
	t.Setenv("AF_TEST_PROFILES_TOKEN", "tok-a")
	l, err := testBridge().Fetch()
	if err != nil || len(l.Profiles) != 1 {
		t.Fatalf("same-origin GET redirect: %+v, %v", l, err)
	}
}
