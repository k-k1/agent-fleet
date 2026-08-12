package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func etagDo(h http.Handler, inm string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	if inm != "" {
		req.Header.Set("If-None-Match", inm)
	}
	rec := httptest.NewRecorder()
	etagJSON(h).ServeHTTP(rec, req)
	return rec
}

func TestEtagJSONTagAnd304(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"sessions":[1,2,3]}`)
	})
	first := etagDo(h, "")
	tag := first.Header().Get("ETag")
	if tag == "" || first.Code != http.StatusOK || first.Body.String() != `{"sessions":[1,2,3]}` {
		t.Fatalf("first GET: code=%d etag=%q body=%q", first.Code, tag, first.Body.String())
	}
	second := etagDo(h, tag)
	if second.Code != http.StatusNotModified {
		t.Fatalf("matching If-None-Match: code=%d, want 304", second.Code)
	}
	if second.Body.Len() != 0 {
		t.Fatalf("304 must have no body, got %q", second.Body.String())
	}
	if got := second.Header().Get("ETag"); got != tag {
		t.Fatalf("304 must re-state the ETag, got %q want %q", got, tag)
	}
}

func TestEtagJSONChangedBodyIs200(t *testing.T) {
	body := `{"v":1}`
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	})
	tag := etagDo(h, "").Header().Get("ETag")
	body = `{"v":2}`
	rec := etagDo(h, tag)
	if rec.Code != http.StatusOK || rec.Body.String() != `{"v":2}` {
		t.Fatalf("changed body must be a fresh 200, got code=%d body=%q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("ETag") == tag {
		t.Fatal("changed body must get a new ETag")
	}
}

func TestEtagJSONSkipsNonJSONAndErrors(t *testing.T) {
	html := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "<html>")
	})
	if rec := etagDo(html, ""); rec.Header().Get("ETag") != "" {
		t.Fatalf("text/html must not be tagged, got %q", rec.Header().Get("ETag"))
	}
	errh := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `{"error":{}}`)
	})
	rec := etagDo(errh, "")
	if rec.Header().Get("ETag") != "" || rec.Code != http.StatusBadGateway || rec.Body.String() != `{"error":{}}` {
		t.Fatalf("non-200 must pass through untagged: code=%d etag=%q body=%q", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
}

func TestEtagJSONSkipsNonGET(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	})
	req := httptest.NewRequest(http.MethodPost, "/api/x", nil)
	rec := httptest.NewRecorder()
	etagJSON(h).ServeHTTP(rec, req)
	if rec.Header().Get("ETag") != "" {
		t.Fatalf("POST must not be tagged, got %q", rec.Header().Get("ETag"))
	}
}

// The production chain is gzipMiddleware(etagJSON(...)): a fresh body must come
// back gzipped WITH the ETag, and a matching If-None-Match must yield a bare 304
// (no body, no Content-Encoding).
func TestEtagInsideGzip(t *testing.T) {
	h := gzipMiddleware(etagJSON(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"big":"`+string(make([]byte, 512))+`"}`)
	})))
	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	tag := rec.Header().Get("ETag")
	if rec.Code != http.StatusOK || tag == "" || rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("fresh GET: code=%d etag=%q enc=%q", rec.Code, tag, rec.Header().Get("Content-Encoding"))
	}
	req2 := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req2.Header.Set("Accept-Encoding", "gzip")
	req2.Header.Set("If-None-Match", tag)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusNotModified || rec2.Body.Len() != 0 || rec2.Header().Get("Content-Encoding") != "" {
		t.Fatalf("304 through gzip: code=%d len=%d enc=%q", rec2.Code, rec2.Body.Len(), rec2.Header().Get("Content-Encoding"))
	}
}

func TestEtagJSONFlushAbandonsBuffering(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"part":1}`)
		w.(http.Flusher).Flush()
		_, _ = io.WriteString(w, `{"part":2}`)
	})
	rec := etagDo(h, "")
	if !rec.Flushed {
		t.Fatal("Flush did not reach the underlying writer")
	}
	if rec.Header().Get("ETag") != "" {
		t.Fatalf("flushed (streaming) response must not be tagged, got %q", rec.Header().Get("ETag"))
	}
	if rec.Body.String() != `{"part":1}{"part":2}` {
		t.Fatalf("streamed body mangled: %q", rec.Body.String())
	}
}

func TestEtagJSONSkipsNoStore(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "private, no-store")
		_, _ = io.WriteString(w, `{"secret":"transcript"}`)
	})
	rec := etagDo(h, "")
	if rec.Header().Get("ETag") != "" {
		t.Fatalf("no-store response must not be tagged, got %q", rec.Header().Get("ETag"))
	}
}
