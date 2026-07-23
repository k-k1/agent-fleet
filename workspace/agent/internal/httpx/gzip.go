package httpx

import (
	"compress/gzip"
	"net/http"
	"strings"
	"sync"
)

// gzPool reuses gzip writers across requests; BestSpeed because the payloads are
// small JSON polled every few seconds — latency and CPU beat ratio here.
var gzPool = sync.Pool{New: func() any {
	w, _ := gzip.NewWriterLevel(nil, gzip.BestSpeed)
	return w
}}

// Gzip transparently compresses responses for clients that accept it. It skips
// WebSocket upgrades (the wrapper would hide http.Hijacker), SSE (token deltas
// must flush per chunk, and the CP proxy re-streams them verbatim), responses
// that are already encoded (the CP proxy passes the Agent's compressed body
// through), and non-200 / non-text payloads.
func Gzip(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "" || !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}
		gw := &gzipWriter{ResponseWriter: w}
		defer gw.close()
		next.ServeHTTP(gw, r)
	})
}

type gzipWriter struct {
	http.ResponseWriter
	gz          *gzip.Writer
	wroteHeader bool
}

func compressible(ct string) bool {
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	switch ct = strings.ToLower(strings.TrimSpace(ct)); {
	case ct == "text/event-stream":
		return false
	case strings.HasPrefix(ct, "text/"),
		ct == "application/json",
		ct == "application/javascript",
		ct == "application/manifest+json",
		ct == "image/svg+xml",
		ct == "application/wasm":
		return true
	}
	return false
}

func (w *gzipWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	h := w.Header()
	if status == http.StatusOK && h.Get("Content-Encoding") == "" && h.Get("Content-Range") == "" && compressible(h.Get("Content-Type")) {
		h.Del("Content-Length")
		h.Set("Content-Encoding", "gzip")
		h.Add("Vary", "Accept-Encoding")
		gz := gzPool.Get().(*gzip.Writer)
		gz.Reset(w.ResponseWriter)
		w.gz = gz
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *gzipWriter) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if w.gz != nil {
		return w.gz.Write(p)
	}
	return w.ResponseWriter.Write(p)
}

func (w *gzipWriter) Flush() {
	if w.gz != nil {
		_ = w.gz.Flush()
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *gzipWriter) close() {
	if w.gz != nil {
		_ = w.gz.Close()
		gzPool.Put(w.gz)
		w.gz = nil
	}
}
