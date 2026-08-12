package main

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

func TestShareOperationPersistsAndReplaysOnce(t *testing.T) {
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	var calls atomic.Int32
	h := withShareOperationIdempotency(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	id := "0123456789abcdef0123456789abcdef"
	invoke := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/sessions/s/turn", nil)
		req.Header.Set(shareOperationHeader, id)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	first := invoke()
	second := invoke()
	if first.Code != http.StatusCreated || second.Code != http.StatusCreated || calls.Load() != 1 {
		t.Fatalf("first=%d second=%d calls=%d", first.Code, second.Code, calls.Load())
	}
	if second.Header().Get("X-Agent-Fleet-Operation-Replay") != "true" {
		t.Fatal("second response was not marked as replay")
	}
}

func TestShareOperationProcessingIsNeverRetried(t *testing.T) {
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	id := "fedcba9876543210fedcba9876543210"
	if _, existed, err := claimShareOperation(id); err != nil || existed {
		t.Fatalf("claim existed=%v err=%v", existed, err)
	}
	var calls atomic.Int32
	h := withShareOperationIdempotency(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	req := httptest.NewRequest(http.MethodPost, "/sessions/s/turn", nil)
	req.Header.Set(shareOperationHeader, id)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict || calls.Load() != 0 {
		t.Fatalf("status=%d calls=%d body=%s", rec.Code, calls.Load(), rec.Body.String())
	}
}

func TestShareOperationReplaysServerErrorAfterSideEffect(t *testing.T) {
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	var calls atomic.Int32
	h := withShareOperationIdempotency(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1) // side effect happened before a downstream 5xx
		http.Error(w, "late failure", http.StatusInternalServerError)
	}))
	id := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	for range 2 {
		req := httptest.NewRequest(http.MethodPost, "/sessions/s/turn", nil)
		req.Header.Set(shareOperationHeader, id)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status=%d", rec.Code)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("side effect calls=%d", calls.Load())
	}
}

func TestShareOperationConcurrentClaimHasSingleExecutor(t *testing.T) {
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	var calls atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	h := withShareOperationIdempotency(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			close(entered)
		}
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	id := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const n = 8
	var wg sync.WaitGroup
	wg.Add(n)
	statuses := make(chan int, n)
	for range n {
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/sessions/s/turn", nil)
			req.Header.Set(shareOperationHeader, id)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			statuses <- rec.Code
		}()
	}
	<-entered
	close(release)
	wg.Wait()
	close(statuses)
	if calls.Load() != 1 {
		t.Fatalf("executors=%d", calls.Load())
	}
	for status := range statuses {
		if status != http.StatusOK && status != http.StatusConflict {
			t.Fatalf("unexpected status=%d", status)
		}
	}
}
