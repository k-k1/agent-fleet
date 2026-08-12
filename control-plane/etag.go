package main

import (
	"bytes"
	"fmt"
	"hash/fnv"
	"net/http"
	"strconv"
	"strings"
)

// etagJSON gives every JSON GET response a weak ETag (length + FNV-1a of the body)
// and answers If-None-Match hits with an empty 304 — the polling floor (sessions /
// notifications / workspace / stats, all unchanged most ticks) then costs headers
// instead of bodies. Proxied Agent responses are hashed as received (possibly
// gzip bytes — deterministic for an unchanged body, which is all a validator
// needs). Sits INSIDE gzipMiddleware so CP-local bodies are hashed uncompressed
// and 304s (non-200) bypass compression.
//
// Only 200 + application/json is buffered; anything else — file downloads, SSE
// (text/event-stream), HTML — streams through untouched, as does a body that
// outgrows etagMaxBuf or a handler that Flushes mid-response.
func etagJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Upgrade") != "" {
			next.ServeHTTP(w, r)
			return
		}
		ew := &etagWriter{ResponseWriter: w, inm: r.Header.Get("If-None-Match")}
		next.ServeHTTP(ew, r)
		ew.finish()
	})
}

const etagMaxBuf = 4 << 20 // mirror tail windows cap at ~1MiB text; 4MiB is ample headroom

type etagWriter struct {
	http.ResponseWriter
	inm         string
	buf         *bytes.Buffer // non-nil while buffering for a hash
	status      int
	decided     bool // WriteHeader reached (real or deferred)
	passthrough bool
}

func (w *etagWriter) WriteHeader(status int) {
	if w.decided {
		return
	}
	w.decided = true
	w.status = status
	if status == http.StatusOK && strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") &&
		!strings.Contains(strings.ToLower(w.Header().Get("Cache-Control")), "no-store") {
		w.buf = &bytes.Buffer{} // defer the real WriteHeader until finish()
		return
	}
	w.passthrough = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *etagWriter) Write(p []byte) (int, error) {
	if !w.decided {
		w.WriteHeader(http.StatusOK)
	}
	if w.buf != nil && w.buf.Len()+len(p) > etagMaxBuf {
		w.abandonBuffer()
	}
	if w.buf != nil {
		return w.buf.Write(p)
	}
	return w.ResponseWriter.Write(p)
}

// abandonBuffer switches to passthrough mid-body: emit what we held, hash nothing.
func (w *etagWriter) abandonBuffer() {
	b := w.buf
	w.buf = nil
	w.passthrough = true
	w.ResponseWriter.WriteHeader(w.status)
	if b.Len() > 0 {
		_, _ = w.ResponseWriter.Write(b.Bytes())
	}
}

// Flush from a handler means streaming intent — buffering would defeat it.
func (w *etagWriter) Flush() {
	if w.buf != nil {
		w.abandonBuffer()
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *etagWriter) finish() {
	if w.buf == nil {
		return // passthrough (or the handler wrote nothing at all)
	}
	h := fnv.New64a()
	_, _ = h.Write(w.buf.Bytes())
	tag := fmt.Sprintf(`W/"%d-%016x"`, w.buf.Len(), h.Sum64())
	w.Header().Set("ETag", tag)
	if w.inm != "" && strings.Contains(w.inm, tag) {
		w.Header().Del("Content-Type")
		w.Header().Del("Content-Length")
		w.Header().Del("Content-Encoding")
		w.ResponseWriter.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Length", strconv.Itoa(w.buf.Len()))
	w.ResponseWriter.WriteHeader(w.status)
	_, _ = w.ResponseWriter.Write(w.buf.Bytes())
}
