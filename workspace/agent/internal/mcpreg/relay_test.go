package mcpreg

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

const relayTestID = "mcp-relaytest01"

// seen is one request as the upstream server received it.
type seen struct {
	Method, URI string
	Header      http.Header
	Body        string
}

// upstream is the registered MCP server. handler decides each answer; every request is recorded
// before it runs.
type upstream struct {
	srv     *httptest.Server
	mu      sync.Mutex
	reqs    []seen
	handler func(w http.ResponseWriter, r *http.Request, n int)
}

func newUpstream(t *testing.T, h func(w http.ResponseWriter, r *http.Request, n int)) *upstream {
	t.Helper()
	u := &upstream{handler: h}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		u.reqs = append(u.reqs, seen{r.Method, r.RequestURI, r.Header.Clone(), string(b)})
		n := len(u.reqs) - 1
		u.mu.Unlock()
		u.handler(w, r, n)
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *upstream) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.reqs)
}

func (u *upstream) at(i int) seen {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.reqs[i]
}

// okFor answers 200 for the named bearer tokens and 401 (with a challenge a CLI would act on)
// for everything else.
func okFor(tokens ...string) func(http.ResponseWriter, *http.Request, int) {
	return func(w http.ResponseWriter, r *http.Request, _ int) {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		for _, tk := range tokens {
			if got == tk {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Mcp-Session-Id", "sess-1")
				_, _ = io.WriteString(w, `{"ok":true}`)
				return
			}
		}
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="http://127.0.0.1:1/prm", scope="mcp:read"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}
}

type relayEnv struct {
	t    *testing.T
	up   *upstream
	as   *asFake
	base string // http://127.0.0.1:<port>
	key  string
	stop func()
}

// newRelayEnv signs a user server in (the initial access token is "tok-initial"; the fake
// authorization server's refreshes answer at-0, at-1, …) and starts the relay.
func newRelayEnv(t *testing.T, up *upstream) *relayEnv {
	t.Helper()
	withTempCLIHomes(t)
	resetOAuthState(t)
	as := newASFake(t, nil)
	now := time.Now().Unix()
	err := secrets.Update(func(s *secrets.Data) error {
		s.MCP = []ServerDef{{
			ID: relayTestID, Name: "hosted", Origin: OriginUser, Transport: TransportHTTP,
			URL: up.srv.URL + "/mcp?tenant=a", Auth: AuthOAuth, Enabled: true,
			Targets: Targets{Session: true, Assistant: true},
		}}
		s.MCPOAuth = []secrets.MCPOAuthGrant{{
			ServerID: relayTestID, Issuer: as.url(""), Registration: OAuthRegDCR, ClientID: "c",
			TokenAuth: "none", TokenEndpoint: as.url("/token"), Resource: up.srv.URL + "/mcp?tenant=a",
			Scope: "mcp:read", AccessToken: "tok-initial", RefreshToken: "rt-initial",
			ExpiresAt: now + 3600, ConnectedAt: now,
		}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	stop, err := StartRelay()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	base, key, _ := relayEndpoint()
	return &relayEnv{t: t, up: up, as: as, base: strings.TrimSuffix(base, relayPathPrefix), key: key, stop: stop}
}

// do sends a request to the relay as a CLI would: the relay key, plus whatever hdr adds.
func (e *relayEnv) do(method, path string, body io.Reader, hdr map[string]string) *http.Response {
	e.t.Helper()
	req, err := http.NewRequest(method, e.base+path, body)
	if err != nil {
		e.t.Fatal(err)
	}
	req.Header.Set(RelayKeyHeader, e.key)
	for k, v := range hdr {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	return resp
}

func readAll(t *testing.T, r *http.Response) string {
	t.Helper()
	defer r.Body.Close()
	b, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}

func relayPath() string { return relayPathPrefix + relayTestID }

func TestRelayRequiresTheKey(t *testing.T) {
	up := newUpstream(t, okFor("tok-initial"))
	e := newRelayEnv(t, up)
	for name, hdr := range map[string]string{"missing": "", "wrong": "0123456789abcdef0123456789abcdef0123456789abcdef", "prefix of the real one": e.key[:len(e.key)-1]} {
		req, _ := http.NewRequest(http.MethodPost, e.base+relayPath(), strings.NewReader("{}"))
		if hdr != "" {
			req.Header.Set(RelayKeyHeader, hdr)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s key: status %d, want 403", name, resp.StatusCode)
		}
		if resp.Header.Get("WWW-Authenticate") != "" {
			t.Errorf("%s key: a challenge was sent", name)
		}
		resp.Body.Close()
	}
	if up.count() != 0 {
		t.Fatalf("a request without the key reached the server (%d)", up.count())
	}
}

func TestRelayRefusesANonLoopbackHost(t *testing.T) {
	up := newUpstream(t, okFor("tok-initial"))
	e := newRelayEnv(t, up)
	for _, host := range []string{"evil.example", "evil.example:80", "localhost.evil.example", "127.0.0.1.evil.example", "10.0.0.5:7000"} {
		resp := e.do(http.MethodPost, relayPath(), strings.NewReader("{}"), map[string]string{"Host": host})
		// The right key, but a name a rebinding page would use.
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("Host %q: status %d, want 403", host, resp.StatusCode)
		}
		resp.Body.Close()
	}
	if up.count() != 0 {
		t.Fatalf("a rebinding Host reached the server (%d)", up.count())
	}
	for _, host := range []string{"127.0.0.1:9", "localhost:9", "[::1]:9", "127.0.0.1"} {
		if !relayHostOK(host) {
			t.Errorf("Host %q must be accepted", host)
		}
	}
}

func TestRelaySetsTheTokenAndForwardsToExactlyTheRegisteredURL(t *testing.T) {
	up := newUpstream(t, okFor("tok-initial"))
	e := newRelayEnv(t, up)
	resp := e.do(http.MethodPost, relayPath()+"?evil=1", strings.NewReader(`{"jsonrpc":"2.0"}`), map[string]string{
		"Content-Type":         "application/json",
		"Accept":               "application/json, text/event-stream",
		"Authorization":        "Bearer cli-own-token",
		"Cookie":               "a=b",
		"Origin":               "http://evil.example",
		"X-Forwarded-For":      "1.2.3.4",
		"Mcp-Method":           "tools/call",
		"Mcp-Name":             "t",
		"MCP-Protocol-Version": "2026-07-28",
		"Mcp-Session-Id":       "legacy-1",
	})
	if got := readAll(t, resp); resp.StatusCode != 200 || got != `{"ok":true}` {
		t.Fatalf("status %d body %q", resp.StatusCode, got)
	}
	if resp.Header.Get("Mcp-Session-Id") != "sess-1" {
		t.Errorf("the server's Mcp-Session-Id was not passed back: %v", resp.Header)
	}
	got := up.at(0)
	if got.Method != "POST" || got.URI != "/mcp?tenant=a" {
		t.Errorf("forwarded to %s %s, want exactly the registered URL", got.Method, got.URI)
	}
	if got.Body != `{"jsonrpc":"2.0"}` {
		t.Errorf("body = %q", got.Body)
	}
	if a := got.Header.Values("Authorization"); len(a) != 1 || a[0] != "Bearer tok-initial" {
		t.Errorf("Authorization = %v; the CLI's own must be replaced by the grant's token", a)
	}
	for _, h := range []string{"Cookie", "Origin", "X-Forwarded-For", RelayKeyHeader} {
		if v := got.Header.Get(h); v != "" {
			t.Errorf("%s reached the server: %q", h, v)
		}
	}
	for h, want := range map[string]string{"Mcp-Method": "tools/call", "Mcp-Name": "t", "Mcp-Protocol-Version": "2026-07-28",
		"Mcp-Session-Id": "legacy-1", "Content-Type": "application/json", "Accept": "application/json, text/event-stream"} {
		if got.Header.Get(h) != want {
			t.Errorf("%s = %q, want %q passed through", h, got.Header.Get(h), want)
		}
	}
}

func TestRelayServesOnlyItsOwnPaths(t *testing.T) {
	up := newUpstream(t, okFor("tok-initial"))
	e := newRelayEnv(t, up)
	for path, want := range map[string]int{
		relayPath() + "/extra": 404, // nothing is appended to the target
		"/mcp/":                404,
		"/other":               404,
		"/mcp/nope":            404, // unknown server
		relayPathPrefix + "mcp-x%0d%0aX-Evil:%20yes":   404, // header injection through the id
		relayPathPrefix + "..%2f..%2fetc":              404,
		relayPathPrefix + relayTestID + "%0d%0aX: y":   404,
		relayPathPrefix + relayTestID + "/../" + "mcp": 404,
	} {
		resp := e.do(http.MethodPost, path, strings.NewReader("{}"), nil)
		if resp.StatusCode != want {
			t.Errorf("%s: status %d, want %d", path, resp.StatusCode, want)
		}
		resp.Body.Close()
	}
	resp := e.do(http.MethodPut, relayPath(), nil, nil)
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("PUT: status %d, want 405", resp.StatusCode)
	}
	resp.Body.Close()
	if up.count() != 0 {
		t.Fatalf("%d request(s) reached the server", up.count())
	}
	if _, ok := relayDef(ServerDef{ID: "x\r\nHost: y", Auth: AuthOAuth}); ok {
		t.Error("a server id that is not path-safe was written into a config")
	}
}

func TestRelayForwardsGetAndDelete(t *testing.T) {
	up := newUpstream(t, okFor("tok-initial"))
	e := newRelayEnv(t, up)
	for _, m := range []string{http.MethodGet, http.MethodDelete} {
		resp := e.do(m, relayPath(), nil, map[string]string{"Mcp-Session-Id": "s9", "Last-Event-ID": "7"})
		if readAll(t, resp); resp.StatusCode != 200 {
			t.Fatalf("%s: %d", m, resp.StatusCode)
		}
	}
	if a, b := up.at(0), up.at(1); a.Method != "GET" || b.Method != "DELETE" || a.URI != "/mcp?tenant=a" ||
		a.Header.Get("Last-Event-Id") != "7" || b.Header.Get("Mcp-Session-Id") != "s9" {
		t.Errorf("forwarded %+v and %+v", a, b)
	}
}

func TestRelayFlushesStreamsAsTheyArrive(t *testing.T) {
	release := make(chan struct{})
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request, _ int) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: one\n\n")
		w.(http.Flusher).Flush()
		select { // the second event waits until the test has read the first
		case <-release:
		case <-r.Context().Done():
			return
		}
		_, _ = io.WriteString(w, "data: two\n\n")
	})
	e := newRelayEnv(t, up)
	defer close(release)
	resp := e.do(http.MethodGet, relayPath(), nil, map[string]string{"Accept": "text/event-stream"})
	defer resp.Body.Close()
	first := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(resp.Body).ReadString('\n')
		first <- line
	}()
	select {
	case line := <-first:
		if line != "data: one\n" {
			t.Fatalf("first line = %q", line)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the first event did not arrive while the stream was still open: the relay buffers")
	}
}

func TestRelayRefreshesOnceOnA401AndRetriesWithTheSameBody(t *testing.T) {
	up := newUpstream(t, okFor("at-0"))
	e := newRelayEnv(t, up)
	resp := e.do(http.MethodPost, relayPath(), strings.NewReader(`{"id":42}`), nil)
	if got := readAll(t, resp); resp.StatusCode != 200 || got != `{"ok":true}` {
		t.Fatalf("status %d body %q", resp.StatusCode, got)
	}
	if up.count() != 2 || e.as.tokenCount() != 1 {
		t.Fatalf("upstream requests = %d, refreshes = %d; want 2 and 1", up.count(), e.as.tokenCount())
	}
	first, second := up.at(0), up.at(1)
	if first.Header.Get("Authorization") != "Bearer tok-initial" || second.Header.Get("Authorization") != "Bearer at-0" {
		t.Errorf("tokens = %q then %q", first.Header.Get("Authorization"), second.Header.Get("Authorization"))
	}
	if first.Body != `{"id":42}` || second.Body != first.Body {
		t.Errorf("the retried request did not carry the original body: %q / %q", first.Body, second.Body)
	}
	if f := e.as.tokens[0]; f.Get("grant_type") != "refresh_token" || f.Get("refresh_token") != "rt-initial" {
		t.Errorf("refresh form = %v", f)
	}
}

func TestRelayNeverLoopsOnA401(t *testing.T) {
	up := newUpstream(t, okFor( /* no token is good enough */ ))
	e := newRelayEnv(t, up)
	resp := e.do(http.MethodPost, relayPath(), strings.NewReader("{}"), nil)
	body := readAll(t, resp)
	if resp.StatusCode != http.StatusBadGateway || !strings.Contains(body, "Console") {
		t.Fatalf("status %d body %q; want a 502 naming the Console", resp.StatusCode, body)
	}
	if resp.Header.Get("WWW-Authenticate") != "" {
		t.Error("the upstream challenge reached the CLI")
	}
	if up.count() != 2 || e.as.tokenCount() != 1 {
		t.Errorf("upstream requests = %d, refreshes = %d; want one retry after one refresh and then stop", up.count(), e.as.tokenCount())
	}
}

func TestRelayNeverPassesTheChallengeOnAGrantThatCannotBeRefreshed(t *testing.T) {
	up := newUpstream(t, okFor( /* nothing */ ))
	e := newRelayEnv(t, up)
	e.as.tokenReply = func(int, url.Values) (int, string) { return 400, `{"error":"invalid_grant"}` }
	changed := make(chan struct{}, 1)
	OnGrantChange = func() { changed <- struct{}{} }
	t.Cleanup(func() { OnGrantChange = nil })

	resp := e.do(http.MethodPost, relayPath(), strings.NewReader("{}"), nil)
	body := readAll(t, resp)
	if resp.StatusCode != http.StatusBadGateway || !strings.Contains(body, "Console") || !strings.Contains(body, "sign in") {
		t.Fatalf("status %d body %q", resp.StatusCode, body)
	}
	if resp.Header.Get("WWW-Authenticate") != "" || strings.Contains(body, "resource_metadata") {
		t.Error("the upstream challenge reached the CLI")
	}
	if st, _ := OAuthStatusOf(relayTestID); st.State != OAuthStateSignInAgain {
		t.Errorf("state = %q, want sign_in_again", st.State)
	}
	select {
	case <-changed:
	case <-time.After(2 * time.Second):
		t.Error("nobody was told the grant ended, so the configs would keep naming the relay path")
	}
	// The next request does not even reach the server.
	n := up.count()
	resp = e.do(http.MethodPost, relayPath(), strings.NewReader("{}"), nil)
	if readAll(t, resp); resp.StatusCode != http.StatusBadGateway || up.count() != n {
		t.Errorf("a grant marked sign-in-again still reached the server (status %d)", resp.StatusCode)
	}
}

func TestRelayAfterDisconnectAnswersWithTheConsoleHint(t *testing.T) {
	up := newUpstream(t, okFor("tok-initial"))
	e := newRelayEnv(t, up)
	if _, err := DisconnectOAuth(context.Background(), relayTestID); err != nil {
		t.Fatal(err)
	}
	resp := e.do(http.MethodPost, relayPath(), strings.NewReader("{}"), nil) // a CLI still holding the URL
	body := readAll(t, resp)
	if resp.StatusCode != http.StatusBadGateway || !strings.Contains(body, "Console") {
		t.Fatalf("status %d body %q", resp.StatusCode, body)
	}
	if up.count() != 0 {
		t.Error("a request without a grant reached the server")
	}
}

func TestRelayRecordsInsufficientScopeAndPassesA403WithoutTheChallenge(t *testing.T) {
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request, _ int) {
		w.Header().Set("WWW-Authenticate", `Bearer error="insufficient_scope", scope="mcp:read mcp:write", resource_metadata="http://127.0.0.1:1/prm"`)
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, "needs more")
	})
	e := newRelayEnv(t, up)
	resp := e.do(http.MethodPost, relayPath(), strings.NewReader("{}"), nil)
	body := readAll(t, resp)
	if resp.StatusCode != http.StatusForbidden || body != "needs more" {
		t.Fatalf("status %d body %q; a 403 and the server's own body pass through", resp.StatusCode, body)
	}
	if resp.Header.Get("WWW-Authenticate") != "" {
		t.Error("the challenge reached the CLI, which would start its own step-up")
	}
	st, _ := OAuthStatusOf(relayTestID)
	if st.State != OAuthStateNeedsMoreOAu || st.StepUpScope != "mcp:read mcp:write" {
		t.Errorf("status = %+v", st)
	}
	if e.as.tokenCount() != 0 {
		t.Error("a 403 must not refresh")
	}
}

func TestRelayDoesNotRecordAPlain403(t *testing.T) {
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request, _ int) { w.WriteHeader(http.StatusForbidden) })
	e := newRelayEnv(t, up)
	resp := e.do(http.MethodPost, relayPath(), strings.NewReader("{}"), nil)
	resp.Body.Close()
	if st, _ := OAuthStatusOf(relayTestID); st.State != OAuthStateConnected || st.StepUpScope != "" {
		t.Errorf("status = %+v", st)
	}
}

func TestRelayDoesNotFollowRedirectsAndNeverSendsTheTokenElsewhere(t *testing.T) {
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhere.Add(1)
	}))
	defer other.Close()
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request, _ int) {
		http.Redirect(w, r, other.URL+"/steal", http.StatusTemporaryRedirect)
	})
	e := newRelayEnv(t, up)
	resp := e.do(http.MethodPost, relayPath(), strings.NewReader("{}"), nil)
	readAll(t, resp)
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status %d, want 502", resp.StatusCode)
	}
	if resp.Header.Get("Location") != "" {
		t.Error("the redirect target reached the CLI, which would follow it with the relay key")
	}
	if elsewhere.Load() != 0 {
		t.Fatal("the relay followed a redirect")
	}
}

func TestConcurrentSessionsShareOneRefresh(t *testing.T) {
	up := newUpstream(t, okFor("at-0"))
	e := newRelayEnv(t, up)
	e.as.tokenDelay = 150 * time.Millisecond
	var wg sync.WaitGroup
	codes := make([]int, 10)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp := e.do(http.MethodPost, relayPath(), strings.NewReader(fmt.Sprintf(`{"id":%d}`, i)), nil)
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			codes[i] = resp.StatusCode
		}()
	}
	wg.Wait()
	for i, c := range codes {
		if c != 200 {
			t.Errorf("request %d: %d", i, c)
		}
	}
	if n := e.as.tokenCount(); n != 1 {
		t.Fatalf("refresh requests = %d, want 1", n)
	}
}

func TestRelayAbortsWhenTheUpstreamStreamBreaks(t *testing.T) {
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request, _ int) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: one\n\n")
		w.(http.Flusher).Flush()
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			conn.Close() // mid-chunked-body, no terminating chunk
		}
	})
	e := newRelayEnv(t, up)
	resp := e.do(http.MethodGet, relayPath(), nil, nil)
	defer resp.Body.Close()
	_, err := io.ReadAll(resp.Body)
	if err == nil {
		t.Fatal("a broken upstream stream ended as a clean, complete response")
	}
}

func TestRelayCapsTheRequestBody(t *testing.T) {
	up := newUpstream(t, okFor("tok-initial"))
	e := newRelayEnv(t, up)
	old := relayMaxBody
	relayMaxBody = 1024
	t.Cleanup(func() { relayMaxBody = old })
	resp := e.do(http.MethodPost, relayPath(), bytes.NewReader(make([]byte, 4096)), nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge || up.count() != 0 {
		t.Errorf("status %d, upstream requests %d", resp.StatusCode, up.count())
	}
}

func TestRelayUsesTheWorkspaceEgress(t *testing.T) {
	tr, ok := relayHTTP.Transport.(*http.Transport)
	if !ok || tr.Proxy == nil {
		t.Fatal("the relay's transport does not consult the proxy settings")
	}
	def := http.DefaultTransport.(*http.Transport)
	if reflect.ValueOf(tr.Proxy).Pointer() != reflect.ValueOf(def.Proxy).Pointer() {
		t.Error("the relay's proxy policy is not the process's own (the egress allowlist is applied there)")
	}
	if relayHTTP.CheckRedirect == nil {
		t.Error("redirects are not refused")
	}

	// And the transport really is the path: with a proxy on it, the upstream request goes there.
	var viaProxy atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		viaProxy.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer proxy.Close()
	pu, _ := url.Parse(proxy.URL)
	old := relayHTTP
	nt := relayTransport()
	nt.Proxy = http.ProxyURL(pu)
	relayHTTP = &http.Client{Transport: nt, CheckRedirect: old.CheckRedirect}
	t.Cleanup(func() { relayHTTP = old })

	up := newUpstream(t, okFor("tok-initial"))
	e := newRelayEnv(t, up)
	resp := e.do(http.MethodPost, relayPath(), strings.NewReader("{}"), nil)
	readAll(t, resp)
	if viaProxy.Load() != 1 || up.count() != 0 {
		t.Errorf("proxy saw %d, upstream %d: relay traffic bypassed the egress transport", viaProxy.Load(), up.count())
	}
}

func TestRelayNeverLogsTheKeyOrATokenAndTheRegistryNeverHoldsIt(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request, n int) {
		if n == 0 {
			hj, _ := w.(http.Hijacker)
			c, _, _ := hj.Hijack()
			c.Close() // the connection dies: the relay logs the failure
			return
		}
		okFor("at-0")(w, r, n)
	})
	e := newRelayEnv(t, up)
	e.do(http.MethodPost, relayPath(), strings.NewReader("{}"), nil).Body.Close()
	req, _ := http.NewRequest(http.MethodPost, e.base+relayPath(), nil)
	req.Header.Set(RelayKeyHeader, "wrong")
	if resp, err := http.DefaultClient.Do(req); err == nil {
		resp.Body.Close()
	}
	e.as.tokenReply = func(int, url.Values) (int, string) { return 500, `{"error":"server_error"}` }
	up.handler = okFor()
	e.do(http.MethodPost, relayPath(), strings.NewReader("{}"), nil).Body.Close()

	for _, secret := range []string{e.key, "tok-initial", "rt-initial", "at-0", "wrong"} {
		if strings.Contains(buf.String(), secret) {
			t.Errorf("the log contains a secret: %q", secret)
		}
	}
	if buf.Len() == 0 {
		t.Error("nothing was logged; the check proves nothing")
	}

	// What the Console and the probe read is the registry, which never holds the key.
	reg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(reg)
	st, _ := OAuthStatusOf(relayTestID)
	sb, _ := json.Marshal(st)
	for _, doc := range [][]byte{b, sb} {
		if bytes.Contains(doc, []byte(e.key)) || bytes.Contains(doc, []byte(RelayKeyHeader)) {
			t.Errorf("the relay key is in %s", doc)
		}
	}
}

// --- port and key lifecycle ---------------------------------------------------

func TestRelayKeepsItsKeyAndPortAcrossRestarts(t *testing.T) {
	withTempCLIHomes(t)
	stop, err := StartRelay()
	if err != nil {
		t.Fatal(err)
	}
	base1, key1, _ := relayEndpoint()
	if fi, err := os.Stat(relayConfigPath()); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("relay config: %v %v; the key's file must be 0600", fi, err)
	}
	stop()
	if _, _, ok := relayEndpoint(); ok {
		t.Fatal("a stopped relay still reports an endpoint, so configs would name a dead port")
	}
	stop, err = StartRelay()
	if err != nil {
		t.Fatal(err)
	}
	base2, key2, _ := relayEndpoint()
	stop()
	if base1 != base2 || key1 != key2 {
		t.Fatalf("restart moved the relay: %s -> %s (key kept: %v)", base1, base2, key1 == key2)
	}
}

func TestRelayChoosesAnotherPortWhenItsOwnIsTaken(t *testing.T) {
	withTempCLIHomes(t)
	stop, err := StartRelay()
	if err != nil {
		t.Fatal(err)
	}
	base1, key1, _ := relayEndpoint()
	stop()
	port := strings.TrimSuffix(strings.TrimPrefix(base1, "http://127.0.0.1:"), relayPathPrefix)
	squat, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		t.Skipf("the port was taken before the test could take it: %v", err)
	}
	defer squat.Close()
	stop, err = StartRelay()
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	base2, key2, ok := relayEndpoint()
	if !ok || base2 == base1 || key2 != key1 {
		t.Fatalf("base %s -> %s, key kept %v", base1, base2, key1 == key2)
	}
	if cfg := loadRelayConfig(); !strings.Contains(base2, fmt.Sprint(cfg.Port)) {
		t.Errorf("the new port was not persisted: %+v", cfg)
	}
}

// --- materialization ----------------------------------------------------------

func TestForSessionWritesAnOAuthServerOnlyWhileAGrantIsLive(t *testing.T) {
	up := newUpstream(t, okFor("tok-initial"))
	e := newRelayEnv(t, up)
	// A static-header server beside it stays exactly as registered.
	if err := secrets.Update(func(s *secrets.Data) error {
		s.MCP = append(s.MCP, ServerDef{ID: "mcp-static", Name: "plain", Origin: OriginUser, Transport: TransportHTTP,
			URL: "https://mcp.example.com/mcp", Headers: map[string]string{"Authorization": "Bearer s"},
			Enabled: true, Targets: Targets{Session: true, Assistant: true}})
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	find := func(defs []ServerDef, name string) *ServerDef {
		for i := range defs {
			if defs[i].Name == name {
				return &defs[i]
			}
		}
		return nil
	}
	for _, kind := range append(append([]string(nil), MaterializedKinds...), session.KindMuse, session.KindLcpp) {
		defs, err := ForSession(kind)
		if err != nil {
			t.Fatal(err)
		}
		h := find(defs, "hosted")
		if h == nil {
			t.Fatalf("%s: a live grant must put the server into the config", kind)
		}
		if h.URL != e.base+relayPath() || !reflect.DeepEqual(h.Headers, map[string]string{RelayKeyHeader: e.key}) || h.Auth != "" {
			t.Errorf("%s: written as %+v, want the relay URL and the key header only", kind, h)
		}
		if strings.Contains(h.URL, up.srv.URL) {
			t.Errorf("%s: the real URL leaked into the config", kind)
		}
		if p := find(defs, "plain"); p == nil || p.URL != "https://mcp.example.com/mcp" || p.Headers["Authorization"] != "Bearer s" {
			t.Errorf("%s: a static-header server was touched: %+v", kind, p)
		}
	}
	if m, _ := ForAssistant(""); m[relayTestID].URL != e.base+relayPath() || m["mcp-static"].URL != "https://mcp.example.com/mcp" {
		t.Errorf("assistant chat: %+v", m)
	}

	// Not while the grant needs a new sign-in, not while it is gone.
	markSignInAgain(relayTestID, e.as.url(""), "")
	if defs, _ := ForSession(session.KindClaude); find(defs, "hosted") != nil {
		t.Error("a sign-in-again grant is written")
	}
	if m, _ := ForAssistant(""); m[relayTestID].ID != "" {
		t.Error("assistant chat got a sign-in-again server")
	}
	if _, err := DisconnectOAuth(context.Background(), relayTestID); err != nil {
		t.Fatal(err)
	}
	if defs, _ := ForSession(session.KindClaude); find(defs, "hosted") != nil || find(defs, "plain") == nil {
		t.Error("a disconnected server is still written, or the static one went with it")
	}
}

func TestForSessionLeavesOAuthServersOutWhileTheRelayIsDown(t *testing.T) {
	up := newUpstream(t, okFor("tok-initial"))
	e := newRelayEnv(t, up)
	e.stop()
	defs, err := ForSession(session.KindClaude)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range defs {
		if d.Name == "hosted" {
			t.Fatal("written with no listener behind it")
		}
	}
}

func TestMaterializeFollowsTheGrantAndKeepsTheKeyPrivate(t *testing.T) {
	up := newUpstream(t, okFor("tok-initial"))
	e := newRelayEnv(t, up)
	claudeJSON := os.Getenv("CLAUDE_CONFIG_DIR") + "/.claude.json"

	if r := Materialize(session.KindClaude); r.Err != "" || !r.Changed {
		t.Fatalf("%+v", r)
	}
	b, err := os.ReadFile(claudeJSON)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), e.base+relayPath()) || !strings.Contains(string(b), e.key) {
		t.Fatalf("the relay URL and key are not in claude's config:\n%s", b)
	}
	if strings.Contains(string(b), up.srv.URL) || strings.Contains(string(b), "tok-initial") {
		t.Fatalf("the real URL or a token reached claude's config:\n%s", b)
	}
	if fi, _ := os.Stat(claudeJSON); fi.Mode().Perm() != 0o600 {
		t.Errorf("claude config mode = %v", fi.Mode().Perm())
	}

	if _, err := DisconnectOAuth(context.Background(), relayTestID); err != nil {
		t.Fatal(err)
	}
	if r := Materialize(session.KindClaude); r.Err != "" || !r.Changed {
		t.Fatalf("%+v", r)
	}
	b, _ = os.ReadFile(claudeJSON)
	if strings.Contains(string(b), relayTestID) || strings.Contains(string(b), e.key) {
		t.Fatalf("a disconnected server (or the key) is still in claude's config:\n%s", b)
	}
}
