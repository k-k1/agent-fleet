package httpx

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func gzipGet(t *testing.T, h http.Handler, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	Gzip(h).ServeHTTP(rec, req)
	return rec
}

func TestGzipCompressesJSON(t *testing.T) {
	body := strings.Repeat(`{"k":"v"}`, 200)
	rec := gzipGet(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}), nil)
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	if rec.Body.Len() >= len(body) {
		t.Fatalf("compressed body (%d) not smaller than plain (%d)", rec.Body.Len(), len(body))
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	if string(plain) != body {
		t.Fatalf("round-trip mismatch: %d bytes", len(plain))
	}
}

func TestGzipSkipsSSE(t *testing.T) {
	rec := gzipGet(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: hi\n\n")
	}), nil)
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("SSE must not be compressed, got Content-Encoding=%q", got)
	}
	if rec.Body.String() != "data: hi\n\n" {
		t.Fatalf("SSE body altered: %q", rec.Body.String())
	}
}

func TestGzipSkipsAlreadyEncoded(t *testing.T) {
	rec := gzipGet(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip") // proxied Agent response, already compressed
		_, _ = w.Write([]byte("pretend-gzip-bytes"))
	}), nil)
	if rec.Body.String() != "pretend-gzip-bytes" {
		t.Fatalf("already-encoded body must pass through verbatim, got %q", rec.Body.String())
	}
}

func TestGzipSkipsUpgradeAndNonAccepting(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	})
	rec := gzipGet(t, h, map[string]string{"Upgrade": "websocket"})
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("Upgrade request must bypass gzip, got %q", got)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil) // no Accept-Encoding
	rec2 := httptest.NewRecorder()
	Gzip(h).ServeHTTP(rec2, req)
	if got := rec2.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("client without Accept-Encoding must get identity, got %q", got)
	}
}

func TestGzipSkipsNon200AndNonText(t *testing.T) {
	rec := gzipGet(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNoContent)
	}), nil)
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("204 must not be compressed, got %q", got)
	}
	rec = gzipGet(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte{0x89, 0x50})
	}), nil)
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("image/png must not be compressed, got %q", got)
	}
}

func TestGzipFlushStreamsIncrementally(t *testing.T) {
	// A flushed compressible response must reach the client incrementally
	// (gzip.Flush emits a sync block); verify the recorder was flushed.
	rec := gzipGet(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "chunk1")
		w.(http.Flusher).Flush()
	}), nil)
	if !rec.Flushed {
		t.Fatal("Flush did not propagate to the underlying writer")
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	plain, _ := io.ReadAll(zr)
	if string(plain) != "chunk1" {
		t.Fatalf("flushed body mismatch: %q", plain)
	}
}
