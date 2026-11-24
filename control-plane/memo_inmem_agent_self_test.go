package main

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// A request whose context is already done never reaches the fake Agent, as a real Transport
// refuses it before sending.
func TestInMemAgentCancelledBeforeSendRunsNoHandler(t *testing.T) {
	var calls atomic.Int32
	url, closeAgent := registerInMemAgent(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url+"/sessions", nil)
	if _, err := agentHTTPClient.Do(req); err == nil {
		t.Fatal("a request with a cancelled context was answered")
	}
	closeAgent() // waits for any handler the call started
	if calls.Load() != 0 {
		t.Fatalf("the handler ran %d times for a cancelled request", calls.Load())
	}
}

// A call cancelled while the handler waits returns the cancellation, not a synthesised answer,
// and the handler ends with the context.
func TestInMemAgentCancelDuringTheCallIsAnError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ended := make(chan struct{})
		url, closeAgent := registerInMemAgent(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			defer close(ended)
			<-r.Context().Done() // answers nothing
		}))
		defer closeAgent()
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url+"/sessions", nil)
		resp, err := agentHTTPClient.Do(req)
		if err == nil {
			resp.Body.Close()
			t.Fatalf("a call that got no answer returned %d", resp.StatusCode)
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("the call ended with %v, want the deadline", err)
		}
		<-ended
	})
}

// The answer built after the handler returned honours a context that ended meanwhile: a handler
// that wrote nothing because the context ended must not become a 200.
func TestInMemAgentResponseAfterCancelIsAnError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://x"+inMemAgentHost+"/", nil)
	cancel()
	if resp, err := (&inMemWriter{header: http.Header{}}).response(req); err == nil {
		t.Fatalf("a cancelled call got %d", resp.StatusCode)
	}
	live, _ := http.NewRequest(http.MethodGet, "http://x"+inMemAgentHost+"/", nil)
	if resp, err := (&inMemWriter{header: http.Header{}}).response(live); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("a live call: %v %v", resp, err)
	}
}

// Closing the Agent waits for a running call and refuses a call that arrives meanwhile.
func TestInMemAgentCloseDrainsAndRefuses(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		started := make(chan struct{})
		url, closeAgent := registerInMemAgent(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(started)
			<-release
		}))
		firstErr := make(chan error, 1)
		go func() {
			req, _ := http.NewRequest(http.MethodGet, url+"/sessions", nil)
			resp, err := agentHTTPClient.Do(req)
			if err == nil {
				resp.Body.Close()
			}
			firstErr <- err
		}()
		<-started
		closed := make(chan struct{})
		go func() { closeAgent(); close(closed) }()
		synctest.Wait() // close is now waiting for the first call
		select {
		case <-closed:
			t.Fatal("close returned while a call was still running")
		default:
		}
		req, _ := http.NewRequest(http.MethodGet, url+"/sessions", nil)
		if _, err := agentHTTPClient.Do(req); err == nil {
			t.Fatal("a call arriving during close was served")
		}
		close(release)
		<-closed
		if err := <-firstErr; err != nil {
			t.Fatalf("the running call failed: %v", err)
		}
	})
}

// A closed Agent refuses new calls.
func TestInMemAgentClosedRefusesCalls(t *testing.T) {
	url, closeAgent := registerInMemAgent(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	closeAgent()
	req, _ := http.NewRequest(http.MethodGet, url+"/sessions", nil)
	if _, err := agentHTTPClient.Do(req); err == nil {
		t.Fatal("a closed Agent answered")
	}
}
