package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// bitbucketGet retries transient failures (transport error / 429 / 5xx) and succeeds once the
// provider recovers, so an intermittent blip doesn't surface as "取得に失敗" in the picker.
func TestBitbucketGetRetriesTransient(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&hits, 1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable) // 503 twice
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	body, err := bitbucketGet(client, "Bearer x", srv.URL)
	if err != nil {
		t.Fatalf("expected success after retries, got %v", err)
	}
	if string(body) != `{"ok":true}` {
		t.Fatalf("unexpected body %q", body)
	}
	if got := atomic.LoadInt32(&hits); got != 3 {
		t.Fatalf("expected 3 attempts, got %d", got)
	}
}

// A 401 is surfaced as the sentinel (so the caller can force a token refresh and retry) and is
// NOT retried in-place — retrying the same rejected token would just spin.
func TestBitbucketGetUnauthorizedNoRetry(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := bitbucketGet(&http.Client{Timeout: 5 * time.Second}, "Bearer x", srv.URL)
	if !errors.Is(err, errBitbucketUnauthorized) {
		t.Fatalf("expected errBitbucketUnauthorized, got %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("401 should not retry; expected 1 attempt, got %d", got)
	}
}

// A non-transient 4xx (e.g. 404) is permanent and returns immediately without retrying.
func TestBitbucketGetPermanentNoRetry(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	if _, err := bitbucketGet(&http.Client{Timeout: 5 * time.Second}, "Bearer x", srv.URL); err == nil {
		t.Fatal("expected error for 404")
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("404 should not retry; expected 1 attempt, got %d", got)
	}
}

func TestRetryBackoffGrowsAndCaps(t *testing.T) {
	if retryBackoff(1) != 300*time.Millisecond {
		t.Fatalf("attempt 1 backoff = %v", retryBackoff(1))
	}
	if retryBackoff(2) != 600*time.Millisecond {
		t.Fatalf("attempt 2 backoff = %v", retryBackoff(2))
	}
	if retryBackoff(100) != 2*time.Second {
		t.Fatalf("backoff should cap at 2s, got %v", retryBackoff(100))
	}
}
