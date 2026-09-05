// main_test.go — the small pure functions in main.go (env resolution).
package main

import (
	"bufio"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// "0 disables it" has to actually disable it. parseDurationOr treats every non-positive
// value as "not set" and hands back the default, which is right for a timeout (0 means
// "no timeout configured") and wrong for a sweep interval (0 means "do not sweep").
func TestIntervalOffHonoursAnExplicitZero(t *testing.T) {
	cases := []struct {
		in   string
		def  time.Duration
		want time.Duration
	}{
		{"", time.Minute, time.Minute},           // unset → default
		{"0", time.Minute, 0},                    // explicitly off
		{"0s", time.Minute, 0},                   // same, spelled out
		{" 30s ", time.Minute, 30 * time.Second}, // set
		{"never", time.Minute, time.Minute},      // garbage must NOT turn a sweep off
	}
	for _, tc := range cases {
		if got := intervalOff(tc.in, tc.def); got != tc.want {
			t.Errorf("intervalOff(%q, %s) = %s, want %s", tc.in, tc.def, got, tc.want)
		}
	}
}

// The access log has to answer "was that probe refused?" — a public deployment is found
// by scanners within hours (172 probes for /actuator/heapdump, /.env and friends in the
// first 9 hours of the dev deployment), and every line used to look the same whether the CP
// returned 401 or 200.
func TestAccessLogRecordsTheStatus(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
		want    int
	}{
		{"explicit status", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}, http.StatusUnauthorized},
		{"body without WriteHeader is a 200", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("ok"))
		}, http.StatusOK},
		{"handler that writes nothing at all", func(http.ResponseWriter, *http.Request) {}, http.StatusOK},
		{"only the FIRST status counts", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			w.WriteHeader(http.StatusOK) // net/http ignores this; so must the log
		}, http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sw := &statusWriter{ResponseWriter: httptest.NewRecorder()}
			tc.handler(sw, httptest.NewRequest("GET", "/x", nil))
			if got := sw.code(); got != tc.want {
				t.Errorf("logged status = %d, want %d", got, tc.want)
			}
		})
	}
}

// The wrapper sits in front of every request, so losing an optional interface here does
// not look like a logging bug: SSE stops streaming and terminals stop connecting.
func TestAccessLogWrapperKeepsFlusherAndHijacker(t *testing.T) {
	var flushed bool
	base := &fakeFlushHijack{ResponseWriter: httptest.NewRecorder(), onFlush: func() { flushed = true }}
	sw := &statusWriter{ResponseWriter: base}

	var w http.ResponseWriter = sw
	f, ok := w.(http.Flusher)
	if !ok {
		t.Fatal("the wrapper hid http.Flusher — SSE would buffer until the handler returns")
	}
	f.Flush()
	if !flushed {
		t.Error("Flush did not reach the underlying writer")
	}
	if _, ok := w.(http.Hijacker); !ok {
		t.Fatal("the wrapper hid http.Hijacker — the terminal WebSocket upgrade would fail")
	}
	if _, _, err := w.(http.Hijacker).Hijack(); err != nil {
		t.Fatalf("Hijack: %v", err)
	}
	if got := sw.code(); got != http.StatusSwitchingProtocols {
		t.Errorf("a hijacked (upgraded) connection logged as %d, want 101", got)
	}
}

type fakeFlushHijack struct {
	http.ResponseWriter
	onFlush func()
}

func (f *fakeFlushHijack) Flush() { f.onFlush() }
func (f *fakeFlushHijack) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	c, _ := net.Pipe()
	return c, bufio.NewReadWriter(bufio.NewReader(c), bufio.NewWriter(c)), nil
}
