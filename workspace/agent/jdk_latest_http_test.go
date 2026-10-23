package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"
)

// stubJDKLatest replaces the Adoptium lookup and the clock, and empties the cache.
// It returns a pointer to the fetch count.
func stubJDKLatest(t *testing.T, version string, err error) (*int, *time.Time) {
	t.Helper()
	calls := 0
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	prevFetch, prevNow := jdkLatestFetch, jdkLatestNow
	jdkLatestFetch = func(string) (string, error) { calls++; return version, err }
	jdkLatestNow = func() time.Time { return now }
	jdkLatestMu.Lock()
	jdkLatestCache = map[string]jdkLatestEntry{}
	jdkLatestMu.Unlock()
	t.Cleanup(func() {
		jdkLatestFetch, jdkLatestNow = prevFetch, prevNow
		jdkLatestMu.Lock()
		jdkLatestCache = map[string]jdkLatestEntry{}
		jdkLatestMu.Unlock()
	})
	return &calls, &now
}

func jdkLatestReq(t *testing.T, major string) (int, map[string]any) {
	t.Helper()
	w := httptest.NewRecorder()
	handleJDKLatest(w, httptest.NewRequest(http.MethodGet, "/env/jdk-latest?major="+major, nil))
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func TestAdoptiumVersionSemantic(t *testing.T) {
	cases := []struct {
		v    adoptiumVersion
		want string
	}{
		{adoptiumVersion{Major: 21, Security: 13, Build: 9}, "21.0.13+9"},
		{adoptiumVersion{Major: 8, Security: 504, Build: 1}, "8.0.504+1"},
		{adoptiumVersion{Major: 17, Security: 4, Patch: 1, Build: 1}, "17.0.4.1+1"},
	}
	for _, c := range cases {
		if got := c.v.semantic(); got != c.want {
			t.Errorf("semantic(%+v) = %q, want %q", c.v, got, c.want)
		}
	}
}

func TestJDKLatestOffersUpdateOnlyWhenNewer(t *testing.T) {
	sys, _ := jdkRoots(t)
	fakeJDK(t, sys, "temurin-21-jdk-"+runtime.GOARCH, "21.0.11+10")

	stubJDKLatest(t, "21.0.13+9", nil)
	code, out := jdkLatestReq(t, "21")
	if code != http.StatusOK || out["current"] != "21.0.11+10" || out["latest"] != "21.0.13+9" || out["update_available"] != true {
		t.Fatalf("outdated: code %d body %v", code, out)
	}

	stubJDKLatest(t, "21.0.11+10", nil)
	if _, out := jdkLatestReq(t, "21"); out["update_available"] != false {
		t.Fatalf("up to date: update_available = %v, want false", out["update_available"])
	}
}

// A major that is not installed has nothing to update, and must not cost a lookup.
func TestJDKLatestNotInstalledSkipsLookup(t *testing.T) {
	jdkRoots(t)
	calls, _ := stubJDKLatest(t, "17.0.21+9", nil)
	code, out := jdkLatestReq(t, "17")
	if code != http.StatusOK || out["update_available"] != false || out["current"] != "" {
		t.Fatalf("code %d body %v", code, out)
	}
	if *calls != 0 {
		t.Fatalf("fetched %d times for an absent major", *calls)
	}
}

func TestJDKLatestCachesAndExpires(t *testing.T) {
	sys, _ := jdkRoots(t)
	fakeJDK(t, sys, "temurin-21-jdk-"+runtime.GOARCH, "21.0.11+10")
	calls, now := stubJDKLatest(t, "21.0.13+9", nil)
	jdkLatestReq(t, "21")
	jdkLatestReq(t, "21")
	if *calls != 1 {
		t.Fatalf("fetches within the TTL = %d, want 1", *calls)
	}
	*now = now.Add(jdkLatestTTL)
	jdkLatestReq(t, "21")
	if *calls != 2 {
		t.Fatalf("fetches after the TTL = %d, want 2", *calls)
	}
}

// Offline: 502 so the Console shows nothing, and the failure is remembered briefly
// instead of hitting the network on every tab open.
func TestJDKLatestFailureIsCachedShortly(t *testing.T) {
	sys, _ := jdkRoots(t)
	fakeJDK(t, sys, "temurin-21-jdk-"+runtime.GOARCH, "21.0.11+10")
	calls, now := stubJDKLatest(t, "", errors.New("dial tcp: no route"))
	if code, _ := jdkLatestReq(t, "21"); code != http.StatusBadGateway {
		t.Fatalf("offline: want 502 got %d", code)
	}
	jdkLatestReq(t, "21")
	if *calls != 1 {
		t.Fatalf("fetches within the failure TTL = %d, want 1", *calls)
	}
	*now = now.Add(jdkLatestFailTTL)
	jdkLatestReq(t, "21")
	if *calls != 2 {
		t.Fatalf("fetches after the failure TTL = %d, want 2", *calls)
	}
}

func TestJDKLatestRejectsBadMajor(t *testing.T) {
	for _, m := range []string{"", "21%26x%3D1", "..%2F..", "latest"} {
		if code, _ := jdkLatestReq(t, m); code != http.StatusBadRequest {
			t.Errorf("major %q: want 400 got %d", m, code)
		}
	}
}

func TestInstalledJavaVersions(t *testing.T) {
	sys, home := jdkRoots(t)
	fakeJDK(t, sys, "temurin-21-jdk-"+runtime.GOARCH, "21.0.11+10")
	fakeJDK(t, home, "temurin-21-jdk-"+runtime.GOARCH, "21.0.13+9")
	fakeJDK(t, sys, "temurin-8-jdk-"+runtime.GOARCH, "8.0.492+9")
	got := installedJavaVersions()
	if got["21"] != "21.0.13+9" || got["8"] != "8.0.492+9" || len(got) != 2 {
		t.Fatalf("installedJavaVersions = %v", got)
	}
}
