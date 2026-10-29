package jirax

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// resetConn closes the connection with an RST (SO_LINGER 0), the way the affected path does.
func resetConn(w http.ResponseWriter) {
	conn, _, err := w.(http.Hijacker).Hijack()
	if err != nil {
		return
	}
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetLinger(0)
	}
	conn.Close()
}

// cutMidBody promises more bytes than it sends, then drops the connection.
func cutMidBody(w http.ResponseWriter) {
	conn, buf, err := w.(http.Hijacker).Hijack()
	if err != nil {
		return
	}
	buf.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 100\r\nContent-Type: application/json\r\n\r\n{\"id\"")
	buf.Flush()
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetLinger(0)
	}
	conn.Close()
}

func fastRetries(t *testing.T) {
	t.Helper()
	old := jiraRetryBase
	jiraRetryBase = time.Millisecond
	t.Cleanup(func() { jiraRetryBase = old })
}

// flaky resets the first n requests, then answers with status/reply.
func flaky(t *testing.T, n int32, status int, reply string) (*secrets.JiraCreds, *atomic.Int32) {
	t.Helper()
	fastRetries(t)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) <= n {
			resetConn(w)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	return &secrets.JiraCreds{Site: srv.URL, Email: "me@example.com", Token: "fake-api-tok"}, &hits
}

func TestJiraRequestRetriesResets(t *testing.T) {
	for _, m := range []string{"GET", "PUT", "DELETE"} {
		c, hits := flaky(t, 2, 200, `{"ok":true}`)
		body, status, err := Request(c, m, c.Site+"/x", nil)
		if err != nil || status != 200 || !strings.Contains(string(body), "ok") {
			t.Fatalf("%s: body=%q status=%d err=%v", m, body, status, err)
		}
		if hits.Load() != 3 {
			t.Errorf("%s: %d attempts, want 3", m, hits.Load())
		}
	}
}

func TestJiraRequestRetriesTruncatedBody(t *testing.T) {
	fastRetries(t)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			cutMidBody(w)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	c := &secrets.JiraCreds{Site: srv.URL, Token: "t"}
	body, status, err := Request(c, "GET", srv.URL, nil)
	if err != nil || status != 200 || string(body) != `{"ok":true}` || hits.Load() != 2 {
		t.Fatalf("body=%q status=%d err=%v hits=%d", body, status, err, hits.Load())
	}
}

func TestJiraRequestNeverRetriesPost(t *testing.T) {
	c, hits := flaky(t, 5, 200, `{}`)
	_, _, err := Request(c, "POST", c.Site+"/x", []byte(`{}`))
	if err == nil || !strings.Contains(err.Error(), "connection reset") {
		t.Fatalf("err = %v, want a named connection reset", err)
	}
	if hits.Load() != 1 {
		t.Errorf("POST attempted %d times", hits.Load())
	}
	// A 503 on a POST is not re-sent either.
	c, hits = flaky(t, 0, 503, `x`)
	_, status, _ := Request(c, "POST", c.Site+"/x", []byte(`{}`))
	if status != 503 || hits.Load() != 1 {
		t.Errorf("POST 503: status=%d hits=%d", status, hits.Load())
	}
}

func TestJiraRequestRetriesServerErrorsAndRateLimit(t *testing.T) {
	for _, st := range []int{429, 502, 503} {
		fastRetries(t)
		var hits atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if hits.Add(1) == 1 {
				w.WriteHeader(st)
				return
			}
			w.WriteHeader(200)
		}))
		c := &secrets.JiraCreds{Site: srv.URL, Token: "t"}
		_, status, err := Request(c, "GET", srv.URL, nil)
		srv.Close()
		if err != nil || status != 200 || hits.Load() != 2 {
			t.Errorf("%d: status=%d err=%v hits=%d", st, status, err, hits.Load())
		}
	}
}

func TestJiraRequestPermanentStatusNotRetried(t *testing.T) {
	for _, st := range []int{400, 401, 403, 404} {
		c, hits := flaky(t, 0, st, `{}`)
		_, status, err := Request(c, "GET", c.Site, nil)
		if err != nil || status != st || hits.Load() != 1 {
			t.Errorf("%d: status=%d err=%v hits=%d", st, status, err, hits.Load())
		}
	}
}

func TestJiraRequestGivesUpWithinBudget(t *testing.T) {
	c, hits := flaky(t, 1000, 200, ``)
	oldB := jiraRetryBudget
	jiraRetryBudget = 50 * time.Millisecond
	jiraRetryBase = 30 * time.Millisecond
	defer func() { jiraRetryBudget = oldB }()
	start := time.Now()
	_, _, err := Request(c, "GET", c.Site, nil)
	if err == nil || !strings.Contains(err.Error(), "could not reach Jira (connection reset)") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "fake-api-tok") || strings.Contains(err.Error(), "127.0.0.1") {
		t.Errorf("error leaks detail: %v", err)
	}
	if hits.Load() >= jiraRetryMax || time.Since(start) > 2*time.Second {
		t.Errorf("hits=%d elapsed=%v: budget not honoured", hits.Load(), time.Since(start))
	}
}

func TestJiraTransportClass(t *testing.T) {
	// A closed port is refused; a stalled server times out.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	c := &secrets.JiraCreds{Token: "t"}
	_, _, err := Request(c, "POST", "http://"+addr, nil)
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("refused: %v", err)
	}
	_, _, err = Request(c, "POST", "http://no-such-host.invalid/", nil)
	if err == nil || !strings.Contains(err.Error(), "DNS") {
		t.Errorf("dns: %v", err)
	}
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(300 * time.Millisecond) }))
	defer slow.Close()
	old := jiraHTTPClient
	jiraHTTPClient = &http.Client{Timeout: 50 * time.Millisecond}
	defer func() { jiraHTTPClient = old }()
	_, _, err = Request(c, "POST", slow.URL, nil)
	if err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Errorf("timeout: %v", err)
	}
}

func TestJiraCommentDeleteAfterResetThenNotFound(t *testing.T) {
	fastRetries(t)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			resetConn(w) // applied upstream, answer lost
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()
	c := &secrets.JiraCreds{Site: srv.URL, Email: "me@example.com", Token: "fake-api-tok"}
	rc, out, errs := runComment(c, "", "issue", "comment", "delete", "-p", "T", "T-1", "10001")
	if rc != 0 || !strings.Contains(out, "probably already deleted") || errs == "" {
		t.Fatalf("rc=%d out=%q err=%q", rc, out, errs)
	}
}

func TestJiraCommentDeletePlain404StaysAnError(t *testing.T) {
	c, _ := commentStub(t, 404, `{}`)
	rc, _, errs := runComment(c, "", "issue", "comment", "delete", "-p", "T", "T-1", "10001")
	if rc != 1 || !strings.Contains(errs, "404") {
		t.Fatalf("rc=%d err=%q", rc, errs)
	}
}

func TestJiraCommentEditRetriesReset(t *testing.T) {
	c, hits := flaky(t, 1, 200, `{"id":"1"}`)
	rc, _, errs := runComment(c, "", "issue", "comment", "edit", "-p", "T", "T-1", "1", "-b", "hi")
	if rc != 0 {
		t.Fatalf("rc=%d err=%q", rc, errs)
	}
	if hits.Load() != 3 { // reset + GET + PUT
		t.Errorf("hits=%d", hits.Load())
	}
}

// A failure before the request left this host proves nothing about the delete: a later 404 is a
// real "no such comment".
func TestJiraCommentDeleteAfterRefusedThenNotFoundIsAnError(t *testing.T) {
	fastRetries(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) }))
	defer srv.Close()
	good := srv.Listener.Addr().String()
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := ln.Addr().String()
	ln.Close()
	var n atomic.Int32
	old := jiraHTTPClient
	jiraHTTPClient = &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, nw, _ string) (net.Conn, error) {
		a := good
		if n.Add(1) == 1 {
			a = dead
		}
		return (&net.Dialer{}).DialContext(ctx, nw, a)
	}}}
	defer func() { jiraHTTPClient = old }()
	c := &secrets.JiraCreds{Site: "http://jira.invalid", Email: "me@example.com", Token: "t"}
	rc, out, errs := runComment(c, "", "issue", "comment", "delete", "-p", "T", "T-1", "99999")
	if rc != 1 || strings.Contains(out, "probably") || !strings.Contains(errs, "404") {
		t.Fatalf("rc=%d out=%q err=%q", rc, out, errs)
	}
}

func TestJiraMaybeApplied(t *testing.T) {
	dialTimeout := &net.OpError{Op: "dial", Net: "tcp", Err: os.ErrDeadlineExceeded}
	readReset := &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}
	readTimeout := &net.OpError{Op: "read", Net: "tcp", Err: os.ErrDeadlineExceeded}
	for err, want := range map[error]bool{dialTimeout: false, readReset: true, readTimeout: true, io.EOF: true} {
		if got := jiraMaybeApplied(err); got != want {
			t.Errorf("jiraMaybeApplied(%v) = %v, want %v", err, got, want)
		}
	}
}
