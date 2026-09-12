package main

// engine_gateway_remote_test.go — the gateway half of borrowing another fleet's engines
// (ADR 0079 decisions 4, 5 and 6).
//
// Every test here drives a FAKE far gateway over httptest, which is the point: P0 has to be
// provable without deploying anything to the deployment that owns the engines, without a
// credential, and without buying a GPU.
//
// What is pinned is the four things a borrowed row does differently, each of which fails
// silently if it is wrong:
//
//   - it is never health-probed (a probe on a row whose URL carries the engine path records
//     demand over there and buys a box);
//   - its upstream URL is the far side's own base path with no local provider prefix on top
//     (twice is /v1/v1/chat/completions);
//   - its bearer is a far session token bought per request, not the row's fixed apiKey, and
//     X-AF-Model travels with it;
//   - a LOCAL expiry is `engine_waking`, not `engine_unavailable` — the image providers retry
//     the first for sixteen minutes and the second not at all.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// --- the fake far deployment ----------------------------------------------------

// farGateway is another Agent Fleet's Control Plane as this one sees it: the token exchange, and
// the engine route under whatever base path it states for ITSELF. It records every path it was
// asked for, because two of the tests below are about a request that must never happen.
type farGateway struct {
	mu sync.Mutex
	// paths is every path asked for, in order. A health probe shows up here and nowhere else.
	paths []string
	// lastHeader is the engine request's headers — the bearer and X-AF-Model are read off it.
	lastHeader http.Header
	minted     int
	// baseURL is what the token answer states as the far side's own prefix for this engine. Set
	// to "" by one test, which is the far side declining to say and this side refusing to guess.
	baseURL string
	engine  http.HandlerFunc
	// stop releases a handler that is deliberately saying nothing. Without it, httptest's Close
	// waits on the blocked connection and the whole package's test run stalls rather than fails.
	stop chan struct{}
}

func (f *farGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.paths = append(f.paths, r.URL.Path)
	f.mu.Unlock()
	if r.URL.Path == "/internal/engine/token" {
		f.mu.Lock()
		f.minted++
		tok := "afe_far-token"
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      tok,
			"expires_at": time.Now().Add(30 * 24 * time.Hour).UTC().Format(time.RFC3339),
			"base_url":   f.baseURL,
		})
		return
	}
	f.mu.Lock()
	f.lastHeader = r.Header.Clone()
	f.mu.Unlock()
	if f.engine != nil {
		f.engine(w, r)
	}
}

// seen reports how many times a path was asked for.
func (f *farGateway) seen(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, p := range f.paths {
		if p == path {
			n++
		}
	}
	return n
}

func (f *farGateway) allPaths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.paths...)
}

func (f *farGateway) header(name string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastHeader.Get(name)
}

// newFarGateway stands one up. The base path it states is the shape a real CP states
// (engine_gateway.go's issueSessionToken), and the tests never compose it themselves.
func newFarGateway(t *testing.T, key string, engine http.HandlerFunc) (*farGateway, *httptest.Server) {
	t.Helper()
	f := &farGateway{baseURL: "/engine/" + key + "/v1", engine: engine, stop: make(chan struct{})}
	srv := httptest.NewServer(f)
	// LIFO, and that order is the point: the handlers are released first, and only then is the
	// server closed.
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(f.stop) })
	return f, srv
}

// silentFar is a far deployment whose engine is still coming up: it accepts the request and says
// nothing at all, which is what puts the LOCAL hold in charge of the answer. `entered` is closed
// once the request has arrived, for the test that then hangs the caller up.
func silentFar(t *testing.T, key string, entered chan struct{}) (*farGateway, *httptest.Server) {
	t.Helper()
	f, srv := newFarGateway(t, key, nil)
	f.engine = func(w http.ResponseWriter, r *http.Request) {
		if entered != nil {
			close(entered)
		}
		select {
		case <-r.Context().Done():
		case <-f.stop:
		}
	}
	return f, srv
}

// newTestRemoteEngine is the row the catalogue poll adopts: the far fleet's bare base as the URL,
// `remote` as the lifecycle, and a handle on the far deployment. The apiKey is deliberately set —
// it is what a borrowed request must NOT present.
func newTestRemoteEngine(t *testing.T, base, key string) *engineRuntimeState {
	t.Helper()
	remotes := &engineRemotes{base: strings.TrimRight(base, "/"), token: "afei_borrower",
		byKey: map[string]*engineRemote{}}
	rem := remotes.forKey(key)
	rem.rows = []store.EngineModel{{Role: key, ID: "m-1", Enabled: true}}
	rem.loaded = true
	def := engineDef{
		Key: key, API: engineAPIChat, Provider: "llamacpp",
		URL: strings.TrimRight(base, "/"), Lifecycle: engineLifecycleRemote,
	}
	return &engineRuntimeState{
		def: def, remote: rem, catalog: newEngineCatalog(nil, key),
		apiKey: "local-key-that-must-not-travel",
	}
}

func remoteGatewayFor(e *engineRuntimeState) engineGateway {
	return engineGateway{reg: &engineRegistry{byKey: map[string]*engineRuntimeState{e.def.Key: e}}}
}

func remoteChatRequest() *http.Request {
	r := httptest.NewRequest("POST", "/engine/llm/v1/chat/completions",
		strings.NewReader(`{"model":"m-1"}`))
	r.Header.Set("X-AF-Model", "m-1")
	r.SetPathValue("key", "llm")
	r.SetPathValue("path", "chat/completions")
	return r
}

func remoteClaims() engineSessionClaims {
	return engineSessionClaims{MembershipID: "M-1", Session: "local-session-7", Key: "llm"}
}

// --- decision 5: never probed, never started ------------------------------------

// The probe that must not happen. `engineHealthy` would ask the row's URL plus /health, and on
// the shape an operator reaches for first — a URL that already carries the engine path — that
// lands on the far GATEWAY, records demand and buys a $1.26/hour box. Five seconds of it, on
// every request, for an answer that can only ever be "no".
func TestRemoteEngineIsNeverHealthProbed(t *testing.T) {
	far, srv := newFarGateway(t, "llm", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	e := newTestRemoteEngine(t, srv.URL, "llm")
	g := remoteGatewayFor(e)

	// ensureReady on its own: no error, nothing dialled, and no wait — this row has no ECS
	// adapter, so the external lane's answer here would be a refusal naming a health URL.
	started := time.Now()
	if err := g.ensureReady(context.Background(), e); err != nil {
		t.Fatalf("ensureReady on a borrowed row = %v, want nil (there is nothing to start)", err)
	}
	if took := time.Since(started); took > time.Second {
		t.Errorf("ensureReady took %s — something waited for a start that cannot happen", took)
	}
	if n := len(far.allPaths()); n != 0 {
		t.Fatalf("ensureReady asked the far deployment for %v — a borrowed row is never probed",
			far.allPaths())
	}

	// And through a whole request: the far side sees the token exchange and the relay, and no
	// health path of any kind.
	rec := httptest.NewRecorder()
	g.plain(rec, remoteChatRequest(), e, remoteClaims(), testMembership(), []byte(`{"model":"m-1"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	for _, probe := range []string{"/health", "/engine/llm/v1/health", "/system_stats"} {
		if n := far.seen(probe); n != 0 {
			t.Errorf("the far deployment was asked for %s %d time(s): %v", probe, n, far.allPaths())
		}
	}
	if got, want := far.allPaths(), []string{"/internal/engine/token", "/engine/llm/v1/chat/completions"}; !sameStrings(got, want) {
		t.Errorf("the far deployment saw %v, want exactly %v", got, want)
	}
}

// --- decision 4: the URL, the bearer and X-AF-Model ------------------------------

// The warm case, and everything that rides on it. The path is the far side's own base_url plus
// the path after the local /engine/<key>/v1/ — with no local provider prefix on top, which is
// what would make it /engine/llm/v1/v1/chat/completions.
func TestRemoteEngineDialUsesTheFarBasePathAndItsOwnToken(t *testing.T) {
	far, srv := newFarGateway(t, "llm", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"hi"}}]}`))
	})
	e := newTestRemoteEngine(t, srv.URL, "llm")
	g := remoteGatewayFor(e)

	rec := httptest.NewRecorder()
	r := remoteChatRequest()
	r.URL.RawQuery = "stream=false"
	g.plain(rec, r, e, remoteClaims(), testMembership(), []byte(`{"model":"m-1"}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"content":"hi"`) {
		t.Errorf("body = %s, want the far answer relayed unchanged", rec.Body.String())
	}
	if n := far.seen("/engine/llm/v1/chat/completions"); n != 1 {
		t.Fatalf("the far deployment saw %v, want one /engine/llm/v1/chat/completions", far.allPaths())
	}
	if n := far.seen("/engine/llm/v1/v1/chat/completions"); n != 0 {
		t.Errorf("the local provider prefix was applied on top of the far one: %v", far.allPaths())
	}
	// Decision 4's second half: the far side reads this to know which catalogue row the request
	// is for — its usage accounting, and the pending guard that answers `engine_waking` while a
	// model is still syncing.
	if got := far.header("X-AF-Model"); got != "m-1" {
		t.Errorf("X-AF-Model upstream = %q, want m-1", got)
	}
	// The bearer is bought per (engine, session), so it cannot be the row's fixed apiKey.
	if got, want := far.header("Authorization"), "Bearer afe_far-token"; got != want {
		t.Errorf("Authorization = %q, want %q", got, want)
	}
	if strings.Contains(far.header("Authorization"), "local-key") {
		t.Error("the row's own apiKey was presented to the far deployment")
	}
	if far.minted != 1 {
		t.Errorf("%d tokens minted, want one bought and cached", far.minted)
	}
	// The session the token was bought for is the LOCAL one: that name is the only way an
	// operator over there tells one borrower's spending from another's.
	if tok, err := e.remote.sessionToken(context.Background(), "local-session-7"); err != nil ||
		tok != "afe_far-token" || far.minted != 1 {
		t.Errorf("second read = %q/%v after %d mints, want the cached token", tok, err, far.minted)
	}
}

// The far side states its own route layout; this side never composes one. Until it has said so
// there is no URL to build, and inventing `/engine/<key>/v1` would post a generation request at
// whatever happens to live there.
func TestRemoteEngineRefusesToGuessTheFarBasePath(t *testing.T) {
	far, srv := newFarGateway(t, "llm", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[]}`))
	})
	far.baseURL = "" // the far side answered a token and no base_url
	e := newTestRemoteEngine(t, srv.URL, "llm")
	g := remoteGatewayFor(e)

	rec := httptest.NewRecorder()
	g.plain(rec, remoteChatRequest(), e, remoteClaims(), testMembership(), []byte(`{"model":"m-1"}`))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if code := engineErrCode(t, rec.Body.Bytes()); code != "engine_unavailable" {
		t.Errorf("code = %q, want engine_unavailable (waiting adds no base_url)", code)
	}
	if !strings.Contains(rec.Body.String(), "base_url") {
		t.Errorf("message = %s, want it to name what the far side did not say", rec.Body.String())
	}
	if got := far.allPaths(); !sameStrings(got, []string{"/internal/engine/token"}) {
		t.Errorf("the far deployment saw %v, want nothing past the token exchange", got)
	}
}

// --- decision 6: the far refusal, and the local one ------------------------------

// The cold case. The far side holds for its own 45 seconds and then answers `engine_waking` with
// a Retry-After; `plain` copies status, headers and body through, so the provider's sixteen
// minutes of retries reach the box that is coming up over there. This is the mechanism the whole
// decision rests on, and what this test guards is that nothing in the borrowing lane rewrites it.
func TestRemoteEngineRelaysTheFarWakingRefusal(t *testing.T) {
	_, srv := newFarGateway(t, "llm", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "6")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"code":"engine_waking","message":"the fleet's own inference engine is starting; retry"}}`))
	})
	e := newTestRemoteEngine(t, srv.URL, "llm")
	g := remoteGatewayFor(e)

	rec := httptest.NewRecorder()
	g.plain(rec, remoteChatRequest(), e, remoteClaims(), testMembership(), []byte(`{"model":"m-1"}`))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if code := engineErrCode(t, rec.Body.Bytes()); code != "engine_waking" {
		t.Errorf("code = %q, want the far side's own engine_waking", code)
	}
	if got := rec.Header().Get("Retry-After"); got != "6" {
		t.Errorf("Retry-After = %q, want the far side's 6 seconds intact", got)
	}
}

// 🔥 The load-bearing one. When the LOCAL hold expires first — which it does whenever the far
// operator raised their own knob — the deadline fires inside engineClient.Do, and without the
// mapping `plain` reports `engine_unavailable`. Both image providers retry `engine_waking` for
// sixteen minutes and `engine_unavailable` not at all (sdcppRetryable, shared with comfy), so a
// GPU on its way up over there would be a permanent failure over here.
func TestRemoteEngineLocalExpiryIsWakingNotUnavailable(t *testing.T) {
	t.Setenv("AF_ENGINE_PLAIN_HOLD", "1") // the mechanism, not the wall clock
	_, srv := silentFar(t, "llm", nil)    // the far side is still starting its box
	e := newTestRemoteEngine(t, srv.URL, "llm")
	g := remoteGatewayFor(e)

	rec := httptest.NewRecorder()
	g.plain(rec, remoteChatRequest(), e, remoteClaims(), testMembership(), []byte(`{"model":"m-1"}`))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if code := engineErrCode(t, rec.Body.Bytes()); code != "engine_waking" {
		t.Errorf("code = %q, want engine_waking — engine_unavailable is never retried by an image provider", code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("no Retry-After on a refusal whose whole point is that the caller comes back")
	}
}

// The other half of the same mapping, and the one it is easy to get wrong: a caller that HUNG UP
// looks like a deadline on the same context. It is not a wake, and answering as though somebody
// were still waiting hides that nobody is.
func TestRemoteEngineClientHangupIsNotWaking(t *testing.T) {
	t.Setenv("AF_ENGINE_PLAIN_HOLD", "30") // long enough that only the cancellation can end it
	entered := make(chan struct{})
	_, srv := silentFar(t, "llm", entered)
	e := newTestRemoteEngine(t, srv.URL, "llm")
	g := remoteGatewayFor(e)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := remoteChatRequest().WithContext(ctx)
	go func() {
		<-entered
		cancel()
	}()

	rec := httptest.NewRecorder()
	g.plain(rec, r, e, remoteClaims(), testMembership(), []byte(`{"model":"m-1"}`))

	if code := engineErrCode(t, rec.Body.Bytes()); code != "engine_unavailable" {
		t.Errorf("code = %q for a caller that hung up, want engine_unavailable "+
			"(engine_waking is for a request somebody is still waiting on)", code)
	}
}

// --- decision 6: the hold is raised per row --------------------------------------

// 75 seconds as a global default would raise MANAGED rows above the ecs-ec2 ingress ALB's
// 60-second idle timeout (deploy/aws/ecs/cfn/30-ingress.yaml), which is the exact 504 the 45 s
// exists to avoid. So the raise is per row, and the knob still governs both.
func TestEnginePlainHoldIsPerRow(t *testing.T) {
	const albIdleTimeout = 60 * time.Second
	remote := newTestRemoteEngine(t, "http://192.0.2.30", "llm")
	managed := &engineRuntimeState{def: engineDef{Key: "llm", Service: "af-llm", URL: "http://192.0.2.31"}}

	if got, want := enginePlainHoldFor(remote), engineRemotePlainHoldSeconds*time.Second; got != want {
		t.Errorf("borrowed hold = %s, want %s", got, want)
	}
	if got, want := enginePlainHoldFor(managed), 45*time.Second; got != want {
		t.Errorf("managed hold = %s, want %s", got, want)
	}
	if got := enginePlainHoldFor(managed); got >= albIdleTimeout {
		t.Errorf("managed hold = %s, want less than the ingress idle timeout %s", got, albIdleTimeout)
	}
	// The operator's override still reaches both — a deployment behind a stricter proxy has to be
	// able to say so about every row it serves.
	t.Setenv("AF_ENGINE_PLAIN_HOLD", "20")
	for _, c := range []struct {
		what string
		eng  *engineRuntimeState
	}{{"borrowed", remote}, {"managed", managed}} {
		if got, want := enginePlainHoldFor(c.eng), 20*time.Second; got != want {
			t.Errorf("%s hold = %s under an override, want %s", c.what, got, want)
		}
	}
	// And it never extends a wake timeout that was deliberately made shorter.
	t.Setenv("AF_ENGINE_PLAIN_HOLD", "75")
	t.Setenv("AF_ENGINE_WAKE_TIMEOUT", "5")
	if got, want := enginePlainHoldFor(remote), 5*time.Second; got != want {
		t.Errorf("borrowed hold = %s with a 5 s wake timeout, want %s", got, want)
	}
}

// --- the streamed path -----------------------------------------------------------

// The far side flushes its 200 at once but its first BODY byte is its own heartbeat, ten seconds
// in — and dial blocks on that byte. What covers the gap is this side writing heartbeats of its
// own, which is why the streaming half of decision 6 needs no code. Then the bytes pass through
// untouched.
func TestRemoteEngineStreamsThroughWithLocalHeartbeats(t *testing.T) {
	engineHeartbeatInterval = 20 * time.Millisecond
	t.Cleanup(func() { engineHeartbeatInterval = 10 * time.Second })

	_, srv := newFarGateway(t, "llm", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		time.Sleep(150 * time.Millisecond) // the far side's own wait, saying nothing
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"))
		w.(http.Flusher).Flush()
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	})
	e := newTestRemoteEngine(t, srv.URL, "llm")
	g := remoteGatewayFor(e)

	rec := httptest.NewRecorder()
	body := []byte(`{"model":"m-1","stream":true}`)
	g.streamed(rec, remoteChatRequest(), e, remoteClaims(), testMembership(), body)

	out := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, out)
	}
	if !strings.Contains(out, string(engineHeartbeatLine)) {
		t.Errorf("body = %q, want the local heartbeat to have covered the far side's silence", out)
	}
	if !strings.Contains(out, `"content":"hi"`) || !strings.Contains(out, "data: [DONE]") {
		t.Errorf("body = %q, want the far stream relayed byte for byte", out)
	}
	// The heartbeats come FIRST: a heartbeat written after the answer would mean the gap was
	// never covered and the test passed on the wrong evidence.
	if i, j := strings.Index(out, string(engineHeartbeatLine)), strings.Index(out, `"content":"hi"`); i > j {
		t.Errorf("the first heartbeat is at %d, after the answer at %d", i, j)
	}
}

// --- review R11: the log line names the row --------------------------------------

// A borrowed row has no ECS adapter, and eng.ecs.logKey() answers the bare word "engine" for
// every one of them — so two roles borrowed from the same fleet are indistinguishable in the one
// place an operator learns that one of them is failing.
func TestEngineLogKeyNamesARowWithNoECSAdapter(t *testing.T) {
	e := newTestRemoteEngine(t, "http://192.0.2.30", "image")
	if got, want := e.logKey(), "engine image"; got != want {
		t.Errorf("logKey() = %q, want %q", got, want)
	}
	if got := (*engineRuntimeState)(nil).logKey(); got != "engine" {
		t.Errorf("nil logKey() = %q, want the anonymous fallback", got)
	}
}

// --- helpers ----------------------------------------------------------------------

// sameStrings compares two path traces. Order matters: "the token exchange, then the relay, and
// nothing else" is the claim.
func sameStrings(a, b []string) bool {
	return strings.Join(a, " ") == strings.Join(b, " ")
}
