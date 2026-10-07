package main

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The folder zip (workspace/agent/fs_zip.go) rides the ordinary REST relay. These pin what the
// download depends on there: the explicit allow-list entry, the archive's headers and exact
// bytes, the query surviving URL-encoding, a cut upstream not passing for a finished download,
// and a vanished browser reaching the Agent so it can release its one build slot.

func TestFSDownloadZipProxyRouteRegistered(t *testing.T) {
	_, mux := smokeEnv(t)
	req := httptest.NewRequest(http.MethodGet, "/api/fs/download-zip?path=x", nil)
	if _, pattern := mux.Handler(req); pattern != "GET /api/fs/download-zip" {
		t.Fatalf("route pattern=%q", pattern)
	}
}

func relayServer(t *testing.T, agent http.Handler) *httptest.Server {
	t.Helper()
	proxy, res, _, cleanup := newFSProxyTest(t, agent)
	t.Cleanup(cleanup)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { proxy.rest(w, r, res) }))
	t.Cleanup(srv.Close)
	return srv
}

func TestFSDownloadZipRelaysHeadersBodyAndQuery(t *testing.T) {
	const path = "repos/日本語 dir/a&b=c#d%2F?x"
	archive := "PK\x03\x04" + strings.Repeat("z", 1000)
	srv := relayServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/fs/download-zip" || r.URL.Query().Get("path") != path || r.URL.Query().Get("check") != "" {
			t.Errorf("upstream saw %s", r.URL.String())
		}
		if r.Header.Get("Authorization") != "Bearer agent-token" || r.Header.Get("X-AF-Relay") != "cp" {
			t.Errorf("upstream auth = %q relay = %q", r.Header.Get("Authorization"), r.Header.Get("X-AF-Relay"))
		}
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", `attachment; filename="x.zip"; filename*=UTF-8''x.zip`)
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("Content-Length", "1004")
		_, _ = io.WriteString(w, archive)
	}))
	resp, err := http.Get(srv.URL + "/api/fs/download-zip?path=" + url.QueryEscape(path))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || string(body) != archive {
		t.Fatalf("body mismatch: %v, %d bytes", err, len(body))
	}
	for k, want := range map[string]string{
		"Content-Type": "application/zip", "Content-Length": "1004", "Cache-Control": "private, no-store",
		"Content-Disposition": `attachment; filename="x.zip"; filename*=UTF-8''x.zip`,
	} {
		if got := resp.Header.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

func TestFSDownloadZipRelaysRefusalsAsJSONNotAnArchive(t *testing.T) {
	for _, status := range []int{http.StatusRequestEntityTooLarge, http.StatusServiceUnavailable, http.StatusNotFound} {
		srv := relayServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"error":{"code":"zip_too_large","message":"too big"}}`)
		}))
		resp, err := http.Get(srv.URL + "/api/fs/download-zip?path=x")
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != status || !strings.Contains(string(b), "zip_too_large") || strings.Contains(resp.Header.Get("Content-Type"), "zip") {
			t.Errorf("status %d: got %d %q %q", status, resp.StatusCode, resp.Header.Get("Content-Type"), b)
		}
	}
}

// An Agent that dies mid-body must not look like a finished download. The Content-Length the
// Agent declared is relayed, so the browser sees a short body and fails the download.
func TestFSDownloadZipCutUpstreamIsAnErrorNotAShortArchive(t *testing.T) {
	srv := relayServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Type: application/zip\r\nContent-Length: 100000\r\n\r\n" + strings.Repeat("p", 500))
		_ = buf.Flush()
	}))
	resp, err := http.Get(srv.URL + "/api/fs/download-zip?path=x")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	n, err := io.Copy(io.Discard, resp.Body)
	if err == nil {
		t.Fatalf("a cut archive read as complete (%d of 100000 bytes, no error)", n)
	}
}

func TestFSDownloadZipBrowserDisconnectReachesTheAgent(t *testing.T) {
	started := make(chan struct{})
	var cancelled atomic.Bool
	done := make(chan struct{})
	srv := relayServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Length", "100000000")
		w.WriteHeader(200)
		// More than the relay's write buffer, so the browser has the status line and is
		// genuinely mid-download when it leaves.
		_, _ = w.Write([]byte(strings.Repeat("P", 64<<10)))
		w.(http.Flusher).Flush()
		close(started)
		select {
		case <-r.Context().Done():
			cancelled.Store(true)
		case <-time.After(5 * time.Second):
		}
	}))
	c, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(c, "GET /api/fs/download-zip?path=x HTTP/1.1\r\nHost: x\r\n\r\n")
	if _, err := bufio.NewReader(c).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	<-started
	c.Close()
	<-done
	if !cancelled.Load() {
		t.Fatal("the Agent was never told the browser left; its build slot would stay held")
	}
}

// Auth and tenant selection are the same withResolved wrapper as every other /api/fs route:
// no identity is 401, and a tenant the caller has no membership in never reaches the Agent.
func TestFSDownloadZipRequiresIdentityAndATenantTheCallerBelongsTo(t *testing.T) {
	cfg, mux := smokeEnv(t)
	cfg.mgr.authMode = "proxy"
	cfg.mgr.emailHeader = "X-Auth-Email"

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/fs/download-zip?path=x", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no identity: %d %s", rec.Code, rec.Body.String())
	}

	for _, sel := range []func(*http.Request){
		func(r *http.Request) { r.URL.RawQuery += "&tenant=not-my-tenant" },
		func(r *http.Request) { r.Header.Set("X-AF-Tenant", "not-my-tenant") },
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/fs/download-zip?path=x", nil)
		req.Header.Set("X-Auth-Email", "someone@example.com")
		sel(req)
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden && rec.Code != http.StatusNotFound {
			t.Fatalf("foreign tenant: %d %s", rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Header().Get("Content-Type"), "zip") {
			t.Fatalf("foreign tenant got an archive")
		}
	}
}
