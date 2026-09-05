package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestPreviewForwardsPublicHostHeaders pins that the public name the CP attached reaches
// the previewed app.
//
// Why it is needed: in Rewrite mode httputil.ReverseProxy DELETES Forwarded /
// X-Forwarded-For / -Host / -Proto from Out before calling Rewrite (measured). "Anything
// you do not touch passes through" is false; the only header that was passing through was
// X-Forwarded-Prefix, which is not on that list.
//
// What happens when this breaks: Next.js checks Origin against x-forwarded-host for
// Server Actions and answers 403. With the headers gone, every Server Action through the
// preview is a 403 — and it looks like the app is broken rather than the proxy
// (docs/log/81 §2.5 (c)).
func TestPreviewForwardsPublicHostHeaders(t *testing.T) {
	var got http.Header
	var sawHost string
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		sawHost = r.Host
	}))
	defer app.Close()

	port := app.Listener.Addr().(interface{ String() string }).String()
	// httptest listens on 127.0.0.1:<port> and handlePreview dials 127.0.0.1:{port}, so
	// pass it the port number alone.
	p := port[len("127.0.0.1:"):]

	mux := http.NewServeMux()
	mux.HandleFunc("/proxy/{port}/{rest...}", handlePreview)
	req := httptest.NewRequest("GET", "/proxy/"+p+"/some/path", nil)
	req.Header.Set("X-Forwarded-Host", "abcdefghij0123456789-3000.pv.example.com")
	req.Header.Set("X-Forwarded-Proto", "https")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if v := got.Get("X-Forwarded-Host"); v != "abcdefghij0123456789-3000.pv.example.com" {
		t.Errorf("X-Forwarded-Host = %q, want the public preview host", v)
	}
	if v := got.Get("X-Forwarded-Proto"); v != "https" {
		t.Errorf("X-Forwarded-Proto = %q, want https", v)
	}
	// Host is forwarded as the internal address (decision 9). That is the only way to
	// pass a dev server's host check (Vite's allowedHosts, Next's allowedDevOrigins)
	// without making the user configure it; the public name rides X-Forwarded-Host.
	if sawHost != "127.0.0.1:"+p {
		t.Errorf("upstream Host = %q, want 127.0.0.1:%s", sawHost, p)
	}
	// The CP↔Agent bearer is never shown to the app.
	if v := got.Get("Authorization"); v != "" {
		t.Errorf("Authorization leaked to the previewed app: %q", v)
	}
}
