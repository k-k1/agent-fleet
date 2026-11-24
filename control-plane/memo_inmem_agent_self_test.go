package main

import (
	"context"
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
		<-ended
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
