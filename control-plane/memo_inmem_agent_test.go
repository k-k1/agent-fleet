package main

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// An in-memory Agent for the memo queue tests. testing/synctest advances its virtual clock only
// while every goroutine of the bubble is durably blocked, and a goroutine waiting on loopback
// network I/O never is. So a fake Agent that a test reaches through httptest.Server keeps the
// clock at real time. This transport calls the fake's handler directly, with no socket, so a
// test can run in a bubble and a 30 s budget costs no wall time.
//
// Only the CP's Agent clients are routed (inMemAgentHost); any other host still goes to the
// network.

// inMemAgentHost is the host suffix of every in-memory Agent; the CP never resolves it.
const inMemAgentHost = ".agent.test"

var (
	inMemAgents sync.Map // host -> *inMemAgent
	inMemSeq    atomic.Int64
)

// inMemTransport serves requests for inMemAgentHost hosts from the registered handler and
// passes every other request to next.
type inMemTransport struct{ next http.RoundTripper }

func init() {
	// The answerTransport wrapper stays outermost, so the CP's liveness bookkeeping still sees
	// the in-memory answers.
	for _, c := range []*http.Client{agentHTTPClient, agentLongCallClient, agentRelayClient, healthzClient, agentStatsClient} {
		if at, ok := c.Transport.(answerTransport); ok {
			at.base = inMemTransport{next: at.base}
			c.Transport = at
		}
	}
}

// inMemAgent is one registered Agent: its handler and the handler calls still running. A caller
// that gives up leaves its handler running, as a real server does; the close function waits for
// those, because a bubble cannot end while one of them is still blocked.
type inMemAgent struct {
	h       http.Handler
	running sync.WaitGroup
}

// registerInMemAgent returns the base URL of a fresh in-memory Agent served by h and a function
// that removes it once its handler calls have returned.
func registerInMemAgent(h http.Handler) (string, func()) {
	host := "a" + strconv.FormatInt(inMemSeq.Add(1), 10) + inMemAgentHost
	a := &inMemAgent{h: h}
	inMemAgents.Store(host, a)
	return "http://" + host, func() {
		a.running.Wait()
		inMemAgents.Delete(host)
	}
}

func (t inMemTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !strings.HasSuffix(req.URL.Hostname(), inMemAgentHost) {
		return t.next.RoundTrip(req)
	}
	v, ok := inMemAgents.Load(req.URL.Host)
	if !ok {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
	}
	a := v.(*inMemAgent)
	// A real server reads the body on its own goroutine and hands the request a context that ends
	// with the connection; mirror that so a handler that blocks does not outlive its caller.
	var body []byte
	if req.Body != nil {
		var err error
		if body, err = io.ReadAll(req.Body); err != nil {
			return nil, err
		}
		_ = req.Body.Close()
	}
	sreq := req.Clone(req.Context())
	sreq.Body = io.NopCloser(bytes.NewReader(body))
	sreq.RequestURI = req.URL.RequestURI()
	sreq.RemoteAddr = "127.0.0.1:1"
	rec := &inMemWriter{header: http.Header{}}
	done := make(chan struct{})
	a.running.Add(1)
	go func() {
		defer a.running.Done()
		defer close(done)
		a.h.ServeHTTP(rec, sreq)
	}()
	select {
	case <-done:
	case <-req.Context().Done():
		return nil, req.Context().Err()
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.hijacked {
		// A handler that closed the connection under the client: the caller sees a reset.
		return nil, io.ErrUnexpectedEOF
	}
	status := rec.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		Status:        http.StatusText(status),
		StatusCode:    status,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        rec.header.Clone(),
		Body:          io.NopCloser(bytes.NewReader(rec.body.Bytes())),
		ContentLength: int64(rec.body.Len()),
		Request:       req,
	}, nil
}

// inMemWriter is a ResponseWriter that keeps the answer in memory. It also implements
// http.Hijacker, because a fake Agent drops a connection by hijacking and closing it.
type inMemWriter struct {
	mu       sync.Mutex
	header   http.Header
	status   int
	body     bytes.Buffer
	hijacked bool
}

func (w *inMemWriter) Header() http.Header { return w.header }

func (w *inMemWriter) WriteHeader(code int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status == 0 {
		w.status = code
	}
}

func (w *inMemWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(p)
}

func (w *inMemWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	w.mu.Lock()
	w.hijacked = true
	w.mu.Unlock()
	c1, c2 := net.Pipe()
	_ = c2.Close()
	return c1, bufio.NewReadWriter(bufio.NewReader(c1), bufio.NewWriter(c1)), nil
}

// inMemAgentSrv is what a fake Agent exposes of its server: the base URL.
type inMemAgentSrv struct{ URL string }
