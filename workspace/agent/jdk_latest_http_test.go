package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// stubLookup replaces a lookup's fetch and clock and empties its cache. It returns
// pointers to the fetch count and the clock.
func stubLookup(t *testing.T, l *versionLookup, version string, err error) (*int, *time.Time) {
	t.Helper()
	calls := 0
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	prevFetch, prevNow := l.fetch, l.now
	reset := func() {
		l.mu.Lock()
		l.cache = map[string]latestEntry{}
		l.inflight = map[string]*latestCall{}
		l.mu.Unlock()
	}
	l.fetch = func(string) (string, error) { calls++; return version, err }
	l.now = func() time.Time { return now }
	reset()
	t.Cleanup(func() {
		l.fetch, l.now = prevFetch, prevNow
		reset()
	})
	return &calls, &now
}

func stubJDKLatest(t *testing.T, version string, err error) (*int, *time.Time) {
	return stubLookup(t, jdkLatestLookup, version, err)
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

// For a major that is not installed, latest is what Install would download, and
// there is nothing to update.
func TestJDKLatestNotInstalledReportsInstallVersion(t *testing.T) {
	jdkRoots(t)
	calls, _ := stubJDKLatest(t, "17.0.21+9", nil)
	code, out := jdkLatestReq(t, "17")
	if code != http.StatusOK || out["current"] != "" || out["latest"] != "17.0.21+9" || out["update_available"] != false {
		t.Fatalf("code %d body %v", code, out)
	}
	if *calls != 1 {
		t.Fatalf("fetches = %d, want 1", *calls)
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
	*now = now.Add(latestTTL)
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
	*now = now.Add(latestFailTTL)
	jdkLatestReq(t, "21")
	if *calls != 2 {
		t.Fatalf("fetches after the failure TTL = %d, want 2", *calls)
	}
}

// Two lookups racing on a cold cache share one fetch, so a slow failure can never
// overwrite the success the other one just stored.
func TestJDKLatestConcurrentCallersShareOneFetch(t *testing.T) {
	stubJDKLatest(t, "", nil)
	var calls atomic.Int32
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	jdkLatestLookup.fetch = func(string) (string, error) {
		calls.Add(1)
		entered <- struct{}{}
		<-release
		return "21.0.13+9", nil
	}
	results := make(chan string, 2)
	get := func() {
		v, _ := jdkLatestLookup.latest("21")
		results <- v
	}
	go get()
	<-entered
	go get()
	// Give the second caller time to reach the fetch if it is (wrongly) allowed to.
	time.Sleep(100 * time.Millisecond)
	close(release)
	for i := 0; i < 2; i++ {
		if v := <-results; v != "21.0.13+9" {
			t.Fatalf("caller %d got %q", i, v)
		}
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("fetches = %d, want 1 shared fetch", n)
	}
}

// Not offered (999) is refused too: the major is a cache key, and an absent major now
// costs a lookup.
func TestJDKLatestRejectsBadMajor(t *testing.T) {
	jdkRoots(t)
	calls, _ := stubJDKLatest(t, "x", nil)
	for _, m := range []string{"", "21%26x%3D1", "..%2F..", "latest", "999"} {
		if code, _ := jdkLatestReq(t, m); code != http.StatusBadRequest {
			t.Errorf("major %q: want 400 got %d", m, code)
		}
	}
	if *calls != 0 {
		t.Fatalf("a refused major cost %d lookups", *calls)
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
