package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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

func TestShareOperationBoundsPersistedResponse(t *testing.T) {
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	h := withShareOperationIdempotency(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Private-Path", "/private/worktree")
		_, _ = w.Write(make([]byte, shareOperationMaxBody+4096))
	}))
	id := "cccccccccccccccccccccccccccccccc"
	req := httptest.NewRequest(http.MethodPost, "/sessions/s/turn", nil)
	req.Header.Set(shareOperationHeader, id)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Body.Len() != shareOperationMaxBody || rec.Header().Get("X-Agent-Fleet-Response-Truncated") != "true" {
		t.Fatalf("body=%d truncated=%q", rec.Body.Len(), rec.Header().Get("X-Agent-Fleet-Response-Truncated"))
	}
	record, ok := readShareOperation(id)
	if !ok || len(record.Body) != shareOperationMaxBody || http.Header(record.Header).Get("X-Private-Path") != "" {
		t.Fatalf("record ok=%v body=%d header=%v", ok, len(record.Body), record.Header)
	}
	info, err := os.Stat(shareOperationPath(id))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > shareOperationMaxRecord {
		t.Fatalf("record size=%d", info.Size())
	}
}

func TestShareOperationGCHasSeparateUnknownRetention(t *testing.T) {
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	if err := os.MkdirAll(shareOperationDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	write := func(id, state string, age time.Duration) {
		t.Helper()
		record := shareOperationRecord{State: state, UpdatedAt: now.Add(-age).Format(time.RFC3339)}
		if state == "applied" {
			record.StatusCode = http.StatusOK
		}
		if err := writeShareOperation(shareOperationPath(id), record); err != nil {
			t.Fatal(err)
		}
	}
	applied := "dddddddddddddddddddddddddddddddd"
	unknownRecent := "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	unknownExpired := "ffffffffffffffffffffffffffffffff"
	write(applied, "applied", 8*24*time.Hour)
	write(unknownRecent, "processing", 8*24*time.Hour)
	write(unknownExpired, "processing", 91*24*time.Hour)
	if err := gcShareOperations(now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(shareOperationPath(applied)); !os.IsNotExist(err) {
		t.Fatalf("expired applied record remains: %v", err)
	}
	if _, err := os.Stat(shareOperationPath(unknownRecent)); err != nil {
		t.Fatalf("recent unknown record removed: %v", err)
	}
	if _, err := os.Stat(shareOperationPath(unknownExpired)); !os.IsNotExist(err) {
		t.Fatalf("expired unknown record remains: %v", err)
	}
}

func TestShareOperationCapacityRejectsBeforeClaim(t *testing.T) {
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	if err := os.MkdirAll(shareOperationDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	body := []byte(fmt.Sprintf(`{"state":"processing","updatedAt":%q}`, now))
	for i := range shareOperationMaxRecords {
		id := fmt.Sprintf("%032x", i+1)
		if err := os.WriteFile(filepath.Join(shareOperationDir(), id+".json"), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	id := "0000000000000000000000000000ffff"
	if _, _, err := claimShareOperation(id); err == nil {
		t.Fatal("capacity exhaustion accepted a new claim")
	}
	if _, err := os.Stat(shareOperationPath(id)); !os.IsNotExist(err) {
		t.Fatalf("claim was published despite capacity error: %v", err)
	}
}

func TestShareOperationGCCompactsLegacyOversizedRecord(t *testing.T) {
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	if err := os.MkdirAll(shareOperationDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	id := "abababababababababababababababab"
	if err := os.WriteFile(shareOperationPath(id), make([]byte, shareOperationMaxRecord+4096), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := gcShareOperations(time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	record, ok := readShareOperation(id)
	if !ok || record.State != "processing" {
		t.Fatalf("legacy record was not compacted to evidence: %+v ok=%v", record, ok)
	}
	info, err := os.Stat(shareOperationPath(id))
	if err != nil || info.Size() >= shareOperationMaxRecord {
		t.Fatalf("compacted size=%v err=%v", info, err)
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
