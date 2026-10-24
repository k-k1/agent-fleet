package main

// Retrying Jira REST calls over a flaky path (TCP resets between the container and Atlassian).
//
// Only idempotent methods are re-sent: a GET, PUT or DELETE that is repeated leaves Jira in the
// state one success would. A POST that died in transport may or may not have been applied, so
// it is attempted once and the caller says so. Callers that must tell "cut off, then 404"
// from a plain 404 (comment delete) read jiraCallResult.afterTransport.

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// jiraRetryBudget bounds the whole call, attempts and waits together, so a long outage costs a
// jira run a few seconds beyond one attempt rather than a hang. No attempt starts once the next
// wait would cross it.
var jiraRetryBudget = 20 * time.Second

// jiraRetryBase is the first wait; it doubles per attempt and is jittered to 50-100%, so the
// bursts of resets seen on the affected path do not line every retry up on the same instant.
var jiraRetryBase = 500 * time.Millisecond

// jiraRetryMax caps the attempts regardless of budget.
const jiraRetryMax = 4

// jiraRetryAfterCap caps a server's Retry-After: a longer ask ends the retrying instead.
const jiraRetryAfterCap = 5 * time.Second

type jiraCallResult struct {
	body   []byte
	status int
	err    error
	// afterTransport is set when an earlier attempt failed in transport after the request may
	// have reached Jira (reset, EOF, timeout; not DNS / refused / TLS, not a 429 or 5xx): it may
	// have been applied although its answer was lost.
	afterTransport bool
}

func jiraIdempotent(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete:
		return true
	}
	return false
}

// jiraTransportClass names what went wrong in a few words, never quoting the error text (it
// carries URLs and addresses, and must never carry a credential).
func jiraTransportClass(err error) string {
	var dns *net.DNSError
	var rec tls.RecordHeaderError
	var unk x509.UnknownAuthorityError
	var inv x509.CertificateInvalidError
	var host x509.HostnameError
	var ne net.Error
	switch {
	case errors.Is(err, syscall.ECONNRESET):
		return "connection reset"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection refused"
	case errors.Is(err, syscall.EPIPE):
		return "connection reset (broken pipe)"
	case errors.As(err, &dns):
		return "DNS lookup failed"
	case errors.As(err, &rec), errors.As(err, &unk), errors.As(err, &inv), errors.As(err, &host),
		strings.Contains(err.Error(), "tls:"), strings.Contains(err.Error(), "x509:"):
		return "TLS error"
	case errors.Is(err, os.ErrDeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return "timeout"
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "connection closed early (EOF)"
	}
	return "network error"
}

// jiraMaybeApplied is false for failures that happen before the request leaves this host
// (DNS, dial failures including a dial timeout, TLS setup): the server never saw it, so a later 404 means the
// resource was never there.
func jiraMaybeApplied(err error) bool {
	var op *net.OpError
	if errors.As(err, &op) && (op.Op == "dial" || op.Op == "resolve") {
		// A dial that timed out never completed the handshake, so nothing was sent.
		return false
	}
	switch jiraTransportClass(err) {
	case "DNS lookup failed", "connection refused", "TLS error":
		return false
	}
	return true
}

func jiraTransportError(err error) error {
	return fmt.Errorf("could not reach Jira (%s)", jiraTransportClass(err))
}

func jiraRetryableStatus(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500
}

// jiraRetryWait is the jittered wait before attempt n+1 (n counts from 1).
func jiraRetryWait(n int) time.Duration {
	d := jiraRetryBase << (n - 1)
	return d/2 + time.Duration(rand.Int63n(int64(d/2)+1))
}

func jiraRetryAfter(h http.Header) time.Duration {
	s, err := strconv.Atoi(strings.TrimSpace(h.Get("Retry-After")))
	if err != nil || s < 0 {
		return 0
	}
	return time.Duration(s) * time.Second
}

// jiraDo performs one authenticated call, re-sending an idempotent method on a transport error,
// 429 or 5xx within jiraRetryBudget.
func jiraDo(c *secrets.JiraCreds, method, u string, payload []byte) jiraCallResult {
	idem := jiraIdempotent(method)
	start := time.Now()
	var res jiraCallResult
	for n := 1; ; n++ {
		var retryAfter time.Duration
		body, status, hdr, err := jiraOnce(c, method, u, payload, idem)
		transport := err != nil
		if transport && jiraMaybeApplied(err) {
			res.afterTransport = true
		}
		res.body, res.status, res.err = body, status, err
		if err != nil {
			res.err = jiraTransportError(err)
		}
		if !idem || n >= jiraRetryMax || (!transport && !jiraRetryableStatus(status)) {
			return res
		}
		if hdr != nil {
			retryAfter = jiraRetryAfter(hdr)
		}
		wait := jiraRetryWait(n)
		if retryAfter > jiraRetryAfterCap {
			return res
		}
		if retryAfter > wait {
			wait = retryAfter
		}
		if time.Since(start)+wait > jiraRetryBudget {
			return res
		}
		time.Sleep(wait)
	}
}

// jiraOnce is a single attempt. Reading the body can fail after the status arrived (a reset
// mid-response); for an idempotent method that is a transport error to retry, for a POST the
// status is all the caller can act on, so the truncated body is passed on as it always was.
func jiraOnce(c *secrets.JiraCreds, method, u string, payload []byte, idem bool) ([]byte, int, http.Header, error) {
	var rdr io.Reader
	if payload != nil {
		rdr = strings.NewReader(string(payload))
	}
	req, err := http.NewRequest(method, u, rdr)
	if err != nil {
		return nil, 0, nil, err
	}
	req.Header.Set("Authorization", jiraAuthHeader(c))
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := jiraHTTPClient.Do(req)
	if err != nil {
		return nil, 0, nil, err
	}
	defer resp.Body.Close()
	body, rerr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if rerr != nil && idem {
		return nil, resp.StatusCode, resp.Header, rerr
	}
	return body, resp.StatusCode, resp.Header, nil
}
