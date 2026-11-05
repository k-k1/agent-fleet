package routegw

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/awsx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

var _ agents.RouteChecker = (*Gateway)(nil)

const (
	testSecret = "af-test-fixture-routegw0secret0key"
	testModel  = "anthropic.claude-sonnet-4-5-20250929-v1%3A0"
)

// env is a gateway with every dependency faked: a session store, a Settings list, an approval
// switch, credentials that count their resolutions, and a Bedrock that records what it was sent.
type env struct {
	t   *testing.T
	g   *Gateway
	now time.Time

	mu         sync.Mutex
	metas      map[string]session.Meta
	settings   map[string]awsx.Profile
	settingsOK bool
	held       bool
	asked      int
	credN      int
	credErr    error
	credExp    time.Time
	rows       []usagex.Record

	up     *httptest.Server
	upReqs []*http.Request
	upBody [][]byte
	upFn   func(w http.ResponseWriter, r *http.Request)
	gw     *httptest.Server
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, now: time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC), metas: map[string]session.Meta{}, settingsOK: true, held: true}
	e.settings = map[string]awsx.Profile{"bedrock-invoke": {ID: "p1", Name: "bedrock-invoke", Label: "Bedrock invoke", AccountID: "123456789012",
		RoleName: "BedrockInvoke", Region: "us-east-1", StartURL: "https://example.awsapps.com/start", SSORegion: "us-east-1", ModelRoute: true}}
	e.up = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		e.mu.Lock()
		e.upReqs = append(e.upReqs, r)
		e.upBody = append(e.upBody, b)
		fn := e.upFn
		e.mu.Unlock()
		if fn != nil {
			fn(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Amzn-Bedrock-Input-Token-Count", "11")
		w.Header().Set("X-Amzn-Bedrock-Output-Token-Count", "7")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"hi"}]}`))
	}))
	t.Cleanup(e.up.Close)
	e.g = New(Deps{
		Now: func() time.Time { e.mu.Lock(); defer e.mu.Unlock(); return e.now },
		ReadMeta: func(n string) (session.Meta, bool) {
			e.mu.Lock()
			defer e.mu.Unlock()
			m, ok := e.metas[n]
			return m, ok
		},
		Settings: func() (map[string]awsx.Profile, bool) {
			e.mu.Lock()
			defer e.mu.Unlock()
			return e.settings, e.settingsOK
		},
		Held: func(map[string]awsx.Profile, string, session.Meta, time.Time) bool {
			e.mu.Lock()
			defer e.mu.Unlock()
			return e.held
		},
		Ask: func(map[string]awsx.Profile, string, string, string) error {
			e.mu.Lock()
			e.asked++
			e.mu.Unlock()
			return fmt.Errorf("%w: waiting", awsx.ErrConfirmRequired)
		},
		Credentials: func(p awsx.Profile, region string, _ map[string]awsx.Profile) (awsx.RouteCredentials, error) {
			e.mu.Lock()
			defer e.mu.Unlock()
			e.credN++
			if e.credErr != nil {
				return awsx.RouteCredentials{}, e.credErr
			}
			return awsx.RouteCredentials{AccessKeyID: "ASIATESTKEY", SecretAccessKey: testSecret, SessionToken: "SESSIONTOKEN", Region: region, Expires: e.credExp}, nil
		},
		Append:   func(r []usagex.Record) error { e.mu.Lock(); e.rows = append(e.rows, r...); e.mu.Unlock(); return nil },
		Endpoint: func(string) string { return e.up.URL },
		BaseURL:  func() string { return "http://127.0.0.1:1" },
	})
	e.gw = httptest.NewServer(e.g)
	t.Cleanup(e.gw.Close)
	return e
}

// session registers a session on the bedrock route and returns its meta.
func (e *env) session(name, profileID, region string) session.Meta {
	m := session.Meta{Name: name, Dir: "/work/" + name, CreatedAt: "2026-11-05T08:00:00Z", Kind: session.KindClaude,
		Route: &routes.Selection{Route: RouteBedrock, Connection: routes.AWSConnection(profileID, region)}}
	e.mu.Lock()
	e.metas[name] = m
	e.mu.Unlock()
	return m
}

func (e *env) issue(m session.Meta) string {
	e.t.Helper()
	is, ref := e.g.Issue(m)
	if ref != nil {
		e.t.Fatalf("Issue refused: %+v", ref)
	}
	return is.Token
}

func (e *env) advance(d time.Duration) { e.mu.Lock(); e.now = e.now.Add(d); e.mu.Unlock() }

func (e *env) call(token, path string, body string) (*http.Response, string) {
	e.t.Helper()
	resp, b, err := e.callErr(token, path, body)
	if err != nil {
		e.t.Fatal(err)
	}
	return resp, b
}

// callErr is call without t.Fatal, for goroutines.
func (e *env) callErr(token, path string, body string) (*http.Response, string, error) {
	req, _ := http.NewRequest("POST", e.gw.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b), nil
}

func (e *env) upstreamCalls() int { e.mu.Lock(); defer e.mu.Unlock(); return len(e.upReqs) }

// waitRows waits for the gateway to write n ledger rows: it writes them after the response has
// been forwarded, so the caller can be done a moment before the row exists.
func (e *env) waitRows(n int) []usagex.Record {
	e.t.Helper()
	for i := 0; i < 200; i++ {
		e.mu.Lock()
		if len(e.rows) >= n {
			defer e.mu.Unlock()
			return append([]usagex.Record(nil), e.rows...)
		}
		e.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	e.t.Fatalf("ledger rows never reached %d", n)
	return nil
}

func (e *env) counts() (asked, credN int) { e.mu.Lock(); defer e.mu.Unlock(); return e.asked, e.credN }

func invokePath() string { return "/model/" + testModel + "/invoke" }

func codeOf(t *testing.T, body string) string {
	t.Helper()
	var v struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("not a refusal body: %q", body)
	}
	return v.Code
}

// --- the happy path, and what leaves the Agent -----------------------------------------------

func TestInvokeIsSignedForwardedAndRecorded(t *testing.T) {
	e := newEnv(t)
	tok := e.issue(e.session("s1", "p1", ""))
	resp, body := e.call(tok, invokePath(), `{"max_tokens":10}`)
	if resp.StatusCode != 200 || !strings.Contains(body, `"hi"`) {
		t.Fatalf("answer = %d %s", resp.StatusCode, body)
	}
	if e.upstreamCalls() != 1 {
		t.Fatalf("upstream calls = %d", e.upstreamCalls())
	}
	e.mu.Lock()
	up, upBody := e.upReqs[0], e.upBody[0]
	e.mu.Unlock()
	if up.URL.EscapedPath() != invokePath() {
		t.Fatalf("upstream path = %s", up.URL.EscapedPath())
	}
	auth := up.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=ASIATESTKEY/") || !strings.Contains(auth, "/us-east-1/bedrock/aws4_request") {
		t.Fatalf("upstream Authorization = %q", auth)
	}
	if up.Header.Get("X-Amz-Security-Token") != "SESSIONTOKEN" {
		t.Fatal("the session token was not sent")
	}
	// The route token must never go upstream, in any header.
	for k, v := range up.Header {
		if strings.Contains(strings.Join(v, ","), tok) {
			t.Fatalf("the route token went upstream in %s", k)
		}
	}
	if string(upBody) != `{"max_tokens":10}` {
		t.Fatalf("upstream body = %q", upBody)
	}
	rows := e.waitRows(1)
	r := rows[0]
	if r.Route != usagex.RouteBedrock || r.Billing != usagex.BillingMetered || r.Feature != usagex.FeatureRouteGateway ||
		r.Ref != "s1" || r.In != 11 || r.Out != 7 || r.Measured != usagex.MeasuredExact || !r.OK || r.Kind != session.KindClaude {
		t.Fatalf("ledger row = %+v", r)
	}
}

// The credential reaches Bedrock and nothing else: not the response, not the refusals, not the log.
func TestNoCredentialOrTokenInAnswersOrLog(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	defer log.SetOutput(io.Discard)

	e := newEnv(t)
	tok := e.issue(e.session("s1", "p1", ""))
	e.upFn = func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		_, _ = w.Write([]byte(`{"message":"denied"}`))
	}
	_, ok := e.call(tok, invokePath(), `{}`)
	e.mu.Lock()
	e.credErr = errors.New("could not get credentials: sso expired")
	e.mu.Unlock()
	e.advance(10 * time.Minute) // past the cached credentials
	_, bad := e.call(tok, invokePath(), `{}`)
	_, noTok := e.call("", invokePath(), `{}`)
	for _, s := range []string{ok, bad, noTok, logs.String()} {
		if strings.Contains(s, testSecret) || strings.Contains(s, "SESSIONTOKEN") || strings.Contains(s, tok) {
			t.Fatalf("a secret leaked into: %q", s)
		}
	}
}

func TestOnlyTheFourModelOperationsPass(t *testing.T) {
	e := newEnv(t)
	tok := e.issue(e.session("s1", "p1", ""))
	for _, p := range []string{"/model/" + testModel + "/invoke", "/model/" + testModel + "/invoke-with-response-stream",
		"/model/" + testModel + "/converse", "/model/" + testModel + "/converse-stream"} {
		if resp, b := e.call(tok, p, `{}`); resp.StatusCode != 200 {
			t.Fatalf("%s = %d %s", p, resp.StatusCode, b)
		}
	}
	for _, p := range []string{"/foundation-models", "/model/" + testModel + "/count-tokens", "/model/x/invoke/extra", "/", "/model//invoke",
		"/guardrails", "/model/" + testModel + "/async-invoke"} {
		if resp, _ := e.call(tok, p, `{}`); resp.StatusCode != 404 {
			t.Fatalf("%s = %d, want 404", p, resp.StatusCode)
		}
	}
	if resp, _ := e.call(tok, "/model/a%20b/invoke", `{}`); resp.StatusCode != 400 {
		t.Fatalf("a model id with a space = %d", resp.StatusCode)
	}
	if resp, _ := e.call(tok, "/model/%2E%2E%2Fx/invoke", `{}`); resp.StatusCode != 400 {
		t.Fatalf("a dot-dot model id = %d", resp.StatusCode)
	}
	req, _ := http.NewRequest("GET", e.gw.URL+invokePath(), nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	if resp, _ := http.DefaultClient.Do(req); resp.StatusCode != 404 {
		t.Fatalf("GET = %d", resp.StatusCode)
	}
	if e.upstreamCalls() != 4 {
		t.Fatalf("upstream calls = %d, want only the four allowed ones", e.upstreamCalls())
	}
}

// --- streaming -------------------------------------------------------------------------------

func esFrame(eventType string, payload []byte) []byte {
	var h bytes.Buffer
	for _, kv := range [][2]string{{":event-type", eventType}, {":content-type", "application/json"}, {":message-type", "event"}} {
		h.WriteByte(byte(len(kv[0])))
		h.WriteString(kv[0])
		h.WriteByte(7)
		_ = binary.Write(&h, binary.BigEndian, uint16(len(kv[1])))
		h.WriteString(kv[1])
	}
	total := 12 + h.Len() + len(payload) + 4
	var b bytes.Buffer
	_ = binary.Write(&b, binary.BigEndian, uint32(total))
	_ = binary.Write(&b, binary.BigEndian, uint32(h.Len()))
	_ = binary.Write(&b, binary.BigEndian, crc32.ChecksumIEEE(b.Bytes()))
	b.Write(h.Bytes())
	b.Write(payload)
	_ = binary.Write(&b, binary.BigEndian, crc32.ChecksumIEEE(b.Bytes()))
	return b.Bytes()
}

func chunk(model string) []byte {
	p, _ := json.Marshal(map[string]string{"bytes": base64.StdEncoding.EncodeToString([]byte(model))})
	return esFrame("chunk", p)
}

// A streamed answer reaches the caller while Bedrock is still sending, and its usage is read from
// the closing invocation metrics.
func TestStreamingIsForwardedWhileItArrives(t *testing.T) {
	e := newEnv(t)
	tok := e.issue(e.session("s1", "p1", ""))
	release := make(chan struct{})
	first := chunk(`{"type":"message_start","message":{"usage":{"input_tokens":5,"cache_read_input_tokens":2}}}`)
	last := chunk(`{"type":"message_stop","amazon-bedrock-invocationMetrics":{"inputTokenCount":42,"outputTokenCount":9}}`)
	e.upFn = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		_, _ = w.Write(first)
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-time.After(5 * time.Second): // a gateway that buffers must fail the test, not hang it
		}
		// The closing frame arrives split in two, as a network may deliver it.
		_, _ = w.Write(last[:20])
		w.(http.Flusher).Flush()
		_, _ = w.Write(last[20:])
	}
	// Headers and the first event must both arrive while the upstream is still open.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stall := time.AfterFunc(3*time.Second, cancel)
	req, _ := http.NewRequestWithContext(ctx, "POST", e.gw.URL+"/model/"+testModel+"/invoke-with-response-stream", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("no response while the upstream was still open: the gateway buffers (%v)", err)
	}
	defer resp.Body.Close()
	got := make([]byte, len(first))
	if _, err := io.ReadFull(resp.Body, got); err != nil || !bytes.Equal(got, first) {
		t.Fatalf("the first event did not reach the caller while the upstream was still open: the gateway buffers (%v)", err)
	}
	stall.Stop()
	close(release)
	rest, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(rest, last) {
		t.Fatalf("rest = %d bytes, want %d", len(rest), len(last))
	}
	rows := e.waitRows(1)
	if len(rows) != 1 || rows[0].In != 42 || rows[0].Out != 9 || rows[0].CacheRead != 2 || rows[0].Measured != usagex.MeasuredExact {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestConverseStreamUsageAndUnknownUsage(t *testing.T) {
	o := newUsageObserver("converse-stream")
	_, _ = o.Write(esFrame("metadata", []byte(`{"usage":{"inputTokens":3,"outputTokens":4,"cacheReadInputTokens":1}}`)))
	if u := o.Finish("converse-stream", http.Header{}); u != (Usage{In: 3, Out: 4, CacheRead: 1, Known: true}) {
		t.Fatalf("usage = %+v", u)
	}
	// Garbage is forwarded and read as "not measured", never as zero.
	o = newUsageObserver("invoke-with-response-stream")
	_, _ = o.Write([]byte("not an event stream at all, long enough to carry a prelude"))
	if u := o.Finish("invoke-with-response-stream", http.Header{}); u.Known {
		t.Fatalf("usage of garbage = %+v", u)
	}
	// Without the closing metrics, Anthropic's own start and delta events are the fallback.
	o = newUsageObserver("invoke-with-response-stream")
	_, _ = o.Write(chunk(`{"type":"message_start","message":{"usage":{"input_tokens":8}}}`))
	_, _ = o.Write(chunk(`{"type":"message_delta","usage":{"output_tokens":6}}`))
	if u := o.Finish("invoke-with-response-stream", http.Header{}); u != (Usage{In: 8, Out: 6, Known: true}) {
		t.Fatalf("fallback usage = %+v", u)
	}
}

// --- the token contract: bound, revoked, never long-lived -------------------------------------

func TestTokenDiesWithItsSession(t *testing.T) {
	e := newEnv(t)
	tok := e.issue(e.session("s1", "p1", ""))
	if resp, _ := e.call(tok, invokePath(), `{}`); resp.StatusCode != 200 {
		t.Fatalf("before = %d", resp.StatusCode)
	}
	e.mu.Lock()
	delete(e.metas, "s1")
	e.mu.Unlock()
	resp, body := e.call(tok, invokePath(), `{}`)
	if resp.StatusCode != 401 || codeOf(t, body) != "route_session_ended" {
		t.Fatalf("after the session ended = %d %s", resp.StatusCode, body)
	}
	if _, ok := e.g.Registry().Lookup(tok); ok {
		t.Fatal("the token is still registered after its session ended")
	}
	// A session of the same name started afterwards is another incarnation: even a token that
	// somehow survived could not be used by it.
	tok2 := e.issue(e.session("s1", "p1", ""))
	e.mu.Lock()
	m := e.metas["s1"]
	m.CreatedAt = "2026-11-05T09:30:00Z"
	e.metas["s1"] = m
	e.mu.Unlock()
	if resp, body := e.call(tok2, invokePath(), `{}`); resp.StatusCode != 401 || codeOf(t, body) != "route_session_ended" {
		t.Fatalf("a recreated session used the old incarnation's token: %d %s", resp.StatusCode, body)
	}
}

func TestTokenDiesWhenTheSelectionChanges(t *testing.T) {
	e := newEnv(t)
	for name, change := range map[string]func(m *session.Meta){
		"route cleared":      func(m *session.Meta) { m.Route = nil },
		"another route":      func(m *session.Meta) { m.Route.Route = "anthropic-api" },
		"another connection": func(m *session.Meta) { m.Route.Connection = routes.AWSConnection("p1", "eu-west-1") },
	} {
		t.Run(name, func(t *testing.T) {
			tok := e.issue(e.session("sel", "p1", ""))
			e.mu.Lock()
			m := e.metas["sel"]
			m.Route = m.Route.Clone()
			change(&m)
			e.metas["sel"] = m
			e.mu.Unlock()
			resp, body := e.call(tok, invokePath(), `{}`)
			if resp.StatusCode != 401 || codeOf(t, body) != "route_selection_changed" {
				t.Fatalf("%d %s", resp.StatusCode, body)
			}
			if _, ok := e.g.Registry().Lookup(tok); ok {
				t.Fatal("the token survived a selection change")
			}
		})
	}
}

func TestTokenDiesWhenTheConnectionChangesOrIsRevoked(t *testing.T) {
	cases := map[string]struct {
		change func(e *env)
		status int
		code   string
	}{
		"unmarked": {func(e *env) {
			p := e.settings["bedrock-invoke"]
			p.ModelRoute = false
			e.settings["bedrock-invoke"] = p
		}, 403, routes.CodeRouteForbidden},
		"deleted": {func(e *env) { delete(e.settings, "bedrock-invoke") }, 403, routes.CodeRouteForbidden},
		"repointed (role edited)": {func(e *env) {
			p := e.settings["bedrock-invoke"]
			p.RoleName = "Admin"
			e.settings["bedrock-invoke"] = p
		}, 401, "route_connection_changed"},
		"replaced by another row of the same name": {func(e *env) {
			p := e.settings["bedrock-invoke"]
			p.ID = "p2"
			e.settings["bedrock-invoke"] = p
		}, 403, routes.CodeRouteForbidden},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			tok := e.issue(e.session("s1", "p1", ""))
			if resp, _ := e.call(tok, invokePath(), `{}`); resp.StatusCode != 200 {
				t.Fatalf("before = %d", resp.StatusCode)
			}
			e.mu.Lock()
			c.change(e)
			e.mu.Unlock()
			resp, body := e.call(tok, invokePath(), `{}`)
			if resp.StatusCode != c.status || codeOf(t, body) != c.code {
				t.Fatalf("after = %d %s", resp.StatusCode, body)
			}
			if _, ok := e.g.Registry().Lookup(tok); ok {
				t.Fatal("the token survived")
			}
			if e.upstreamCalls() != 1 {
				t.Fatalf("a call went upstream after the connection changed (%d calls)", e.upstreamCalls())
			}
		})
	}
}

func TestTokenNeverOutlivesEightHours(t *testing.T) {
	e := newEnv(t)
	tok := e.issue(e.session("s1", "p1", ""))
	e.advance(MaxTTL - time.Minute)
	if resp, _ := e.call(tok, invokePath(), `{}`); resp.StatusCode != 200 {
		t.Fatalf("just before the limit = %d", resp.StatusCode)
	}
	e.advance(2 * time.Minute)
	if resp, body := e.call(tok, invokePath(), `{}`); resp.StatusCode != 401 || codeOf(t, body) != "route_token_invalid" {
		t.Fatalf("past the limit = %d %s", resp.StatusCode, body)
	}
	r := NewRegistry(nil)
	_, exp, err := r.Mint(Binding{Session: "s", Incarnation: "i", Route: "bedrock", ProfileID: "p"}, 1000*time.Hour)
	if err != nil || time.Until(exp) > MaxTTL+time.Minute {
		t.Fatalf("a long ttl was not clamped: %v %v", exp, err)
	}
}

func TestSettingsUnreadableRefusesWithoutRevoking(t *testing.T) {
	e := newEnv(t)
	tok := e.issue(e.session("s1", "p1", ""))
	e.mu.Lock()
	e.settingsOK = false
	e.mu.Unlock()
	if resp, _ := e.call(tok, invokePath(), `{}`); resp.StatusCode != 503 {
		t.Fatalf("settings unreadable = %d", resp.StatusCode)
	}
	e.mu.Lock()
	e.settingsOK = true
	e.mu.Unlock()
	if resp, _ := e.call(tok, invokePath(), `{}`); resp.StatusCode != 200 {
		t.Fatalf("after settings came back = %d", resp.StatusCode)
	}
}

func TestMintReplacesEarlierTokensOfTheSession(t *testing.T) {
	e := newEnv(t)
	m := e.session("s1", "p1", "")
	old := e.issue(m)
	cur := e.issue(m)
	if resp, _ := e.call(old, invokePath(), `{}`); resp.StatusCode != 401 {
		t.Fatalf("an earlier token still works = %d", resp.StatusCode)
	}
	if resp, _ := e.call(cur, invokePath(), `{}`); resp.StatusCode != 200 {
		t.Fatalf("the current token = %d", resp.StatusCode)
	}
}

func TestSweepEndsTokensOfGoneSessions(t *testing.T) {
	e := newEnv(t)
	tok := e.issue(e.session("gone", "p1", ""))
	e.issue(e.session("kept", "p1", ""))
	e.mu.Lock()
	delete(e.metas, "gone")
	e.mu.Unlock()
	if n := e.g.Sweep(); n != 1 {
		t.Fatalf("swept %d", n)
	}
	if _, ok := e.g.Registry().Lookup(tok); ok || e.g.Registry().Len() != 1 {
		t.Fatalf("registry after sweep: len %d", e.g.Registry().Len())
	}
}

// --- approval (ADR 0126), re-checked on every call ---------------------------------------------

func TestApprovalIsCheckedOnEveryCall(t *testing.T) {
	e := newEnv(t)
	tok := e.issue(e.session("s1", "p1", ""))
	if resp, _ := e.call(tok, invokePath(), `{}`); resp.StatusCode != 200 {
		t.Fatalf("approved = %d", resp.StatusCode)
	}
	e.mu.Lock()
	e.held = false // the grant ended, or the member removed it
	e.mu.Unlock()
	resp, body := e.call(tok, invokePath(), `{}`)
	if resp.StatusCode != 403 || codeOf(t, body) != "route_confirmation_required" {
		t.Fatalf("without approval = %d %s", resp.StatusCode, body)
	}
	if e.upstreamCalls() != 1 {
		t.Fatalf("a call went upstream without the approval (%d)", e.upstreamCalls())
	}
	// A retry inside the window does not file the request again.
	e.call(tok, invokePath(), `{}`)
	if asked, _ := e.counts(); asked != 1 {
		t.Fatalf("requests filed = %d, want 1 in the window", asked)
	}
	e.advance(askEvery + time.Second)
	e.call(tok, invokePath(), `{}`)
	if asked, _ := e.counts(); asked != 2 {
		t.Fatalf("requests filed after the window = %d, want 2", asked)
	}
	e.mu.Lock()
	e.held = true
	e.mu.Unlock()
	if resp, _ := e.call(tok, invokePath(), `{}`); resp.StatusCode != 200 {
		t.Fatalf("approved again = %d: the token should survive a missing approval", resp.StatusCode)
	}
}

// --- permission is use, not ownership ----------------------------------------------------------

func TestIssueRefusals(t *testing.T) {
	e := newEnv(t)
	bad := func(m session.Meta, wantCode string) {
		t.Helper()
		_, ref := e.g.Issue(m)
		if ref == nil || ref.Code != wantCode {
			t.Fatalf("Issue(%+v) = %+v, want %s", m.Route, ref, wantCode)
		}
	}
	// A profile that is not in this member's Settings (another member's, or deleted).
	bad(e.session("a", "someone-elses", ""), routes.CodeRouteForbidden)
	// Not marked: visibly refused, naming the setting.
	p := e.settings["bedrock-invoke"]
	p.ModelRoute = false
	e.settings["bedrock-invoke"] = p
	_, ref := e.g.Issue(e.session("b", "p1", ""))
	if ref == nil || ref.Code != routes.CodeRouteForbidden || !strings.Contains(ref.Message, "not marked as a model route") {
		t.Fatalf("unmarked = %+v", ref)
	}
	p.ModelRoute = true
	e.settings["bedrock-invoke"] = p
	// No selection, another route, a malformed connection, a region that is not a region.
	bad(session.Meta{Name: "c", Dir: "/w", CreatedAt: "x"}, routes.CodeBadRoute)
	bad(session.Meta{Name: "d", Dir: "/w", CreatedAt: "x", Route: &routes.Selection{Route: "anthropic-api"}}, routes.CodeBadRoute)
	bad(session.Meta{Name: "e", Dir: "/w", CreatedAt: "x", Route: &routes.Selection{Route: RouteBedrock, Connection: "p1"}}, routes.CodeRouteUnavailable)
	bad(e.session("f", "p1", "us-east-1.evil.example"), routes.CodeRouteUnavailable)
	bad(e.session("g", "p1", "cn-north-1"), routes.CodeRouteUnavailable)
	p.Region = ""
	e.settings["bedrock-invoke"] = p
	bad(e.session("h", "p1", ""), routes.CodeRouteUnavailable)
	if e.g.Registry().Len() != 0 {
		t.Fatal("a refused issue left a token behind")
	}
}

// A token belongs to one session: it cannot be exchanged for another's, and a token for a session
// whose profile the member removed does not keep another session's profile alive.
func TestTokensAreSessionBound(t *testing.T) {
	e := newEnv(t)
	e.settings["other"] = awsx.Profile{ID: "p2", Name: "other", AccountID: "210987654321", RoleName: "Invoke", Region: "us-west-2", ModelRoute: true,
		StartURL: "https://example.awsapps.com/start", SSORegion: "us-east-1"}
	a := e.issue(e.session("a", "p1", ""))
	b := e.issue(e.session("b", "p2", ""))
	e.call(a, invokePath(), `{}`)
	e.call(b, invokePath(), `{}`)
	rows := e.waitRows(2)
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.upReqs) != 2 {
		t.Fatalf("upstream calls = %d", len(e.upReqs))
	}
	if !strings.Contains(e.upReqs[0].Header.Get("Authorization"), "/us-east-1/bedrock/") ||
		!strings.Contains(e.upReqs[1].Header.Get("Authorization"), "/us-west-2/bedrock/") {
		t.Fatal("each session's calls must go to its own connection's region")
	}
	if rows[0].Ref != "a" || rows[1].Ref != "b" {
		t.Fatalf("ledger refs = %q %q", rows[0].Ref, rows[1].Ref)
	}
}

func TestMissingOrForeignBearerCallsNothing(t *testing.T) {
	e := newEnv(t)
	for _, tok := range []string{"", "afrt_" + strings.Repeat("0", 64), "not-a-route-token", "Bearer"} {
		if resp, _ := e.call(tok, invokePath(), `{}`); resp.StatusCode != 401 {
			t.Fatalf("token %q = %d", tok, resp.StatusCode)
		}
	}
	if _, credN := e.counts(); credN != 0 || e.upstreamCalls() != 0 {
		t.Fatalf("credentials resolved %d times, upstream called %d times for an unauthenticated caller", credN, e.upstreamCalls())
	}
}

// --- credentials --------------------------------------------------------------------------------

func TestCredentialsAreCachedUntilShortlyBeforeTheyExpire(t *testing.T) {
	e := newEnv(t)
	e.credExp = e.now.Add(time.Hour)
	tok := e.issue(e.session("s1", "p1", ""))
	e.call(tok, invokePath(), `{}`)
	e.call(tok, invokePath(), `{}`)
	if _, n := e.counts(); n != 1 {
		t.Fatalf("resolutions = %d, want 1", n)
	}
	e.advance(56 * time.Minute) // inside the reserve
	e.call(tok, invokePath(), `{}`)
	if _, n := e.counts(); n != 2 {
		t.Fatalf("resolutions after the reserve = %d, want 2", n)
	}
}

func TestLoginRequiredIsRefusedVisibly(t *testing.T) {
	e := newEnv(t)
	e.credErr = fmt.Errorf("%w for profile: expired", awsx.ErrLoginRequired)
	tok := e.issue(e.session("s1", "p1", ""))
	resp, body := e.call(tok, invokePath(), `{}`)
	if resp.StatusCode != 403 || codeOf(t, body) != "route_login_required" || !strings.Contains(body, "bedrock-invoke") {
		t.Fatalf("login required = %d %s", resp.StatusCode, body)
	}
	if e.upstreamCalls() != 0 {
		t.Fatal("a call went upstream without credentials")
	}
}

func TestUpstreamErrorStatusIsForwardedAndRecordedAsFailed(t *testing.T) {
	e := newEnv(t)
	e.upFn = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Amzn-Errortype", "ThrottlingException")
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"message":"slow down"}`))
	}
	tok := e.issue(e.session("s1", "p1", ""))
	resp, body := e.call(tok, invokePath(), `{}`)
	if resp.StatusCode != 429 || !strings.Contains(body, "slow down") || resp.Header.Get("X-Amzn-Errortype") != "ThrottlingException" {
		t.Fatalf("answer = %d %s", resp.StatusCode, body)
	}
	rows := e.waitRows(1)
	if len(rows) != 1 || rows[0].OK || rows[0].Measured != usagex.MeasuredNone {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestRequestTooLargeIsRefusedBeforeAnyCredential(t *testing.T) {
	e := newEnv(t)
	tok := e.issue(e.session("s1", "p1", ""))
	big := strings.NewReader(strings.Repeat("a", maxRequestBody+1))
	req, _ := http.NewRequest("POST", e.gw.URL+invokePath(), big)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if _, n := e.counts(); resp.StatusCode != 413 || n != 0 {
		t.Fatalf("status %d, credentials resolved %d", resp.StatusCode, n)
	}
}

// A slow credential resolution for one profile does not hold up another profile's calls, and
// concurrent callers of one profile share a single resolution.
func TestSlowCredentialResolutionDoesNotBlockOtherProfiles(t *testing.T) {
	e := newEnv(t)
	e.settings["other"] = awsx.Profile{ID: "p2", Name: "other", AccountID: "210987654321", RoleName: "Invoke", Region: "us-west-2", ModelRoute: true,
		StartURL: "https://example.awsapps.com/start", SSORegion: "us-east-1"}
	a := e.issue(e.session("a", "p1", ""))
	b := e.issue(e.session("b", "p2", ""))
	release := make(chan struct{})
	var once sync.Once
	releaseAll := func() { once.Do(func() { close(release) }) }
	defer releaseAll()
	var mu sync.Mutex
	resolved := map[string]int{}
	e.g.d.Credentials = func(p awsx.Profile, region string, _ map[string]awsx.Profile) (awsx.RouteCredentials, error) {
		mu.Lock()
		resolved[p.ID]++
		mu.Unlock()
		if p.ID == "p1" {
			<-release
		}
		return awsx.RouteCredentials{AccessKeyID: "ASIA", SecretAccessKey: testSecret, SessionToken: "T", Region: region}, nil
	}
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := e.callErr(a, invokePath(), `{}`); err != nil {
				errs <- err
			}
		}()
	}
	// Wait until p1's resolution is in progress.
	for i := 0; i < 200; i++ {
		mu.Lock()
		n := resolved["p1"]
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	done := make(chan int, 1)
	go func() {
		resp, _, err := e.callErr(b, invokePath(), `{}`)
		if err != nil {
			errs <- err
			done <- 0
			return
		}
		done <- resp.StatusCode
	}()
	select {
	case code := <-done:
		if code != 200 {
			t.Fatalf("other profile = %d", code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("another profile's call was blocked behind a slow credential resolution")
	}
	releaseAll()
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if resolved["p1"] != 1 {
		t.Fatalf("p1 resolved %d times for concurrent callers, want 1", resolved["p1"])
	}
}

// Bedrock failing after it started answering: the caller gets what was sent, and the ledger row is
// a failure that keeps the usage read so far.
func TestStreamCutMidwayIsRecordedAsFailed(t *testing.T) {
	e := newEnv(t)
	tok := e.issue(e.session("s1", "p1", ""))
	first := chunk(`{"type":"message_start","message":{"usage":{"input_tokens":5}}}`)
	e.upFn = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		w.Header().Set("Content-Length", "100000") // promise more than is sent, then cut
		_, _ = w.Write(first)
	}
	req, _ := http.NewRequest("POST", e.gw.URL+"/model/"+testModel+"/invoke-with-response-stream", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		_, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
	}
	rows := e.waitRows(1)
	if rows[0].OK || rows[0].Route != usagex.RouteBedrock || rows[0].In != 5 {
		t.Fatalf("row = %+v", rows[0])
	}
}

// The caller going away ends the upstream request too.
func TestClientDisconnectCancelsTheUpstreamRequest(t *testing.T) {
	e := newEnv(t)
	tok := e.issue(e.session("s1", "p1", ""))
	started, cancelled := make(chan struct{}), make(chan struct{})
	e.upFn = func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		close(started)
		select {
		case <-r.Context().Done():
			close(cancelled)
		case <-time.After(5 * time.Second):
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "POST", e.gw.URL+invokePath(), strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	<-started
	cancel()
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("the upstream request was not cancelled when the caller disconnected")
	}
}
