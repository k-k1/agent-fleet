package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

// gatewayHeaders are what an oauth2-proxy or an ALB OIDC front end can add under names no
// denylist lists. None may reach the Agent or a previewed app.
var gatewayHeaders = map[string]string{
	"X-Forwarded-Access-Token":       "synthetic-access-token",
	"X-Forwarded-Groups":             "synthetic-group",
	"X-Forwarded-Preferred-Username": "synthetic-user",
	"X-Auth-Request-Groups":          "synthetic-group",
	"X-Amzn-Oidc-Data":               "synthetic-claims",
	"X-Amzn-Oidc-Accesstoken":        "synthetic-access-token",
	"X-Amzn-Oidc-Identity":           "synthetic-subject",
}

// neededHeaders are what the Agent and the apps behind a preview use today; each must survive.
var neededHeaders = map[string]string{
	"Accept": "text/html", "Accept-Encoding": "gzip", "Accept-Language": "ja",
	"Range": "bytes=0-9", "If-None-Match": `"etag"`, "If-Modified-Since": "Wed, 21 Oct 2015 07:28:00 GMT",
	"Content-Type": "application/json", "Origin": "http://example.com", "Referer": "https://cp.example/x",
	"User-Agent": "test-agent", "Last-Event-Id": "7", "Sec-Fetch-Site": "same-origin",
	"Next-Action": "abc", "X-Requested-With": "XMLHttpRequest", "X-Csrf-Token": "synthetic-csrf",
}

func putHeaders(r *http.Request, m map[string]string) {
	for k, v := range m {
		r.Header.Set(k, v)
	}
}

func assertRelayed(t *testing.T, who string, h http.Header) {
	t.Helper()
	for k := range gatewayHeaders {
		if v := h.Get(k); v != "" {
			t.Errorf("%s: gateway header %s reached the receiver (%q)", who, k, v)
		}
	}
	for k, v := range neededHeaders {
		if got := h.Get(k); got != v {
			t.Errorf("%s: %s = %q, want %q", who, k, got, v)
		}
	}
}

// The REST and SSE relays to the Agent keep what the Agent reads and nothing a gateway added;
// the signing mark and bearer, set after the strip, are still there.
func TestAgentRelayAllowListDropsGatewayHeaders(t *testing.T) {
	const idh = "X-AF-Test-Email"
	var got []http.Header
	proxy, res, _, cleanup := newFSProxyTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Clone())
		_, _ = w.Write([]byte(`{}`))
	}))
	defer cleanup()
	proxy.mgr.emailHeader = idh
	req := httptest.NewRequest(http.MethodPost, "/api/agents/memory/entries/pin", strings.NewReader(`{}`))
	browserCredentials(req, idh)
	putHeaders(req, gatewayHeaders)
	putHeaders(req, neededHeaders)
	rec := httptest.NewRecorder()
	proxy.rest(rec, req, res)
	if rec.Code != http.StatusOK {
		t.Fatalf("rest: %d %s", rec.Code, rec.Body)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/sessions/s1/events", nil)
	browserCredentials(req, idh)
	putHeaders(req, gatewayHeaders)
	putHeaders(req, neededHeaders)
	proxy.stream(httptest.NewRecorder(), req, res)
	if len(got) != 2 {
		t.Fatalf("agent saw %d requests", len(got))
	}
	for i, h := range got {
		assertRelayed(t, []string{"rest", "stream"}[i], h)
		if h.Get("Authorization") != "Bearer agent-token" {
			t.Errorf("relay %d lost the Agent bearer: %q", i, h.Get("Authorization"))
		}
	}
	if got[0].Get(memberOriginHeader) == "" {
		t.Errorf("the signed route lost its member-origin mark")
	}
}

// A preview request keeps content negotiation, range / conditional headers, the app's own
// cookies, the headers the CP sets itself (X-Forwarded-*, the Agent bearer) and drops what a
// gateway added.
func TestPreviewRelayAllowList(t *testing.T) {
	const idh = "X-AF-Test-Email"
	var got http.Header
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
	}))
	defer agent.Close()
	rt := stubRuntime{endpoint: agent.URL, token: "agent-token"}
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	browserCredentials(req, idh)
	putHeaders(req, gatewayHeaders)
	putHeaders(req, neededHeaders)
	relayPreview(httptest.NewRecorder(), req, rt, previewRelayOptions{
		port: 5173, path: "/app", publicHost: "cp.example", proto: "https",
		prefix: "/preview/5173", identityHeader: idh,
	})
	if got == nil {
		t.Fatal("the Agent saw no request")
	}
	assertRelayed(t, "preview", got)
	if got.Get("X-Forwarded-Host") != "cp.example" || got.Get("X-Forwarded-Proto") != "https" ||
		got.Get("X-Forwarded-Prefix") != "/preview/5173" || got.Get("Authorization") != "Bearer agent-token" {
		t.Errorf("CP-set headers: %v", got)
	}
	if c := got.Get("Cookie"); c != "theme=dark" {
		t.Errorf("cookie = %q, want the app cookie only", c)
	}
}

// A WebSocket handshake through the preview relay still upgrades (Sec-WebSocket-* and the
// subprotocol survive) and frames flow, while a gateway header on the handshake does not
// reach the app.
func TestPreviewRelayWebSocketUpgrade(t *testing.T) {
	var seen http.Header
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }, Subprotocols: []string{"vite-hmr"}}
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		mt, msg, err := c.ReadMessage()
		if err == nil {
			_ = c.WriteMessage(mt, msg)
		}
	}))
	defer agent.Close()
	rt := stubRuntime{endpoint: agent.URL, token: "agent-token"}
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		relayPreview(w, r, rt, previewRelayOptions{port: 5173, path: "/", publicHost: "p-5173.example", proto: "https", identityHeader: "X-AF-Test-Email"})
	}))
	defer front.Close()

	h := http.Header{}
	for k, v := range gatewayHeaders {
		h.Set(k, v)
	}
	h.Set("Origin", "https://p-5173.example")
	d := websocket.Dialer{Subprotocols: []string{"vite-hmr"}}
	c, resp, err := d.Dial("ws"+strings.TrimPrefix(front.URL, "http"), h)
	if err != nil {
		t.Fatalf("handshake through the relay failed: %v (resp %v)", err, resp)
	}
	defer c.Close()
	if c.Subprotocol() != "vite-hmr" {
		t.Errorf("subprotocol = %q", c.Subprotocol())
	}
	if err := c.WriteMessage(websocket.TextMessage, []byte("ping")); err != nil {
		t.Fatal(err)
	}
	if _, msg, err := c.ReadMessage(); err != nil || string(msg) != "ping" {
		t.Fatalf("echo = %q, %v", msg, err)
	}
	for k := range gatewayHeaders {
		if seen.Get(k) != "" {
			t.Errorf("gateway header %s reached the app on the handshake", k)
		}
	}
	if seen.Get("Origin") != "https://p-5173.example" || seen.Get("X-Forwarded-Host") != "p-5173.example" {
		t.Errorf("handshake headers: %v", seen)
	}
}

// AF_RELAY_EXTRA_HEADERS adds names and prefixes without widening the built-in list.
func TestRelayExtraHeaders(t *testing.T) {
	old := relayExtraHeaders
	t.Cleanup(func() { relayExtraHeaders = old })
	relayExtraHeaders = parseRelayExtraHeaders(" x-app-token ,X-Tenant-*")
	for _, n := range []string{"X-App-Token", "X-Tenant-Id", "X-Tenant-"} {
		if !relayHeaderAllowed(n) {
			t.Errorf("%s should be allowed", n)
		}
	}
	for _, n := range []string{"X-Forwarded-Access-Token", "X-Other"} {
		if relayHeaderAllowed(n) {
			t.Errorf("%s should not be allowed", n)
		}
	}
}
