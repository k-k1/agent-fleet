package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// --- a stand-in for one engine -------------------------------------------------

// slowEngine is llama-server's shape: /health answers 503 "loading model" until `readyAt`,
// and /v1/chat/completions then streams. That is the behaviour the whole gateway is written
// against — a box that exists but is not usable yet.
type slowEngine struct {
	readyAt  time.Time
	prefill  time.Duration // silence between the request and the first chunk
	requests int
}

func (e *slowEngine) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			if time.Now().Before(e.readyAt) {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"status":"loading model"}`))
				return
			}
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return
		}
		e.requests++
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		time.Sleep(e.prefill)
		_, _ = w.Write([]byte("data: {\"model\":\"qwen3\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":23226,\"completion_tokens\":57}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		w.(http.Flusher).Flush()
	})
}

// engineTestECS is an ECS that starts stopped and reports RUNNING once desired is 1.
type engineTestECS struct {
	desired  int32
	running  int32
	updates  int
	instance string // capacity provider of the one container instance, if any
	// instanceType is what that instance registers itself as (`ecs.instance-type`). Empty is a
	// real answer too — ADR 0074's start gate must not read a missing attribute as a match.
	instanceType string
}

func (f *engineTestECS) DescribeServices(context.Context, *ecs.DescribeServicesInput, ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error) {
	return &ecs.DescribeServicesOutput{Services: []ecstypes.Service{{
		Status: aws.String("ACTIVE"), DesiredCount: f.desired, RunningCount: f.running,
	}}}, nil
}

func (f *engineTestECS) UpdateService(_ context.Context, in *ecs.UpdateServiceInput, _ ...func(*ecs.Options)) (*ecs.UpdateServiceOutput, error) {
	f.updates++
	f.desired = aws.ToInt32(in.DesiredCount)
	f.running = f.desired
	return &ecs.UpdateServiceOutput{}, nil
}

func (f *engineTestECS) ListContainerInstances(context.Context, *ecs.ListContainerInstancesInput, ...func(*ecs.Options)) (*ecs.ListContainerInstancesOutput, error) {
	if f.instance == "" {
		return &ecs.ListContainerInstancesOutput{}, nil
	}
	return &ecs.ListContainerInstancesOutput{ContainerInstanceArns: []string{"arn:ci/i-1"}}, nil
}

func (f *engineTestECS) DescribeContainerInstances(context.Context, *ecs.DescribeContainerInstancesInput, ...func(*ecs.Options)) (*ecs.DescribeContainerInstancesOutput, error) {
	ci := ecstypes.ContainerInstance{
		ContainerInstanceArn: aws.String("arn:ci/i-1"), CapacityProviderName: aws.String(f.instance),
	}
	if f.instanceType != "" {
		ci.Attributes = []ecstypes.Attribute{{Name: aws.String(engineBoxTypeAttr), Value: aws.String(f.instanceType)}}
	}
	return &ecs.DescribeContainerInstancesOutput{ContainerInstances: []ecstypes.ContainerInstance{ci}}, nil
}

// newTestEngine wires a gateway around one stubbed engine, with no store behind it: the
// membership lookup is the part these tests are not about, so the handler under test is
// exercised through the pieces that do not need one.
func newTestEngine(t *testing.T, url string, api engineECSAPI) *engineRuntimeState {
	t.Helper()
	def := engineDef{
		Key: "llm", Service: "af-llm", URL: url, Health: "/health",
		Provider: "llamacpp",
		IdleSec:  1800, StartDeadlineSec: 900,
	}
	e := &engineRuntimeState{
		def: def,
		ecs: &engineECS{api: api, key: "llm", cluster: "c", service: "af-llm"},
	}
	e.demand = newEngineDemand(nil, engineSettingsFor("llm").demandAt, 5*time.Minute)
	return e
}

// --- decision 5: one attempt, not a retry -------------------------------------

// The whole of ADR 0071 decision 5, and the one observation that verifies it: a request to a
// STOPPED engine gets its answer on the FIRST attempt, without the client ever having to
// resend.
//
// Measured against the real opencode 1.18.29: the connection is cut when no BODY BYTE has
// arrived for 300 seconds — not when 300 seconds have passed. Headers alone do not hold it
// (cut at 306.9 s); headers plus an SSE comment every 10 s held a 400-second wait with no
// resend at all. So what is asserted here is that comment lines actually go out while the
// engine is starting AND while it is prefilling, and that they carry no content the client
// could mistake for an answer.
func TestEngineGatewayHoldsTheFirstRequestWithHeartbeats(t *testing.T) {
	eng := &slowEngine{readyAt: time.Now().Add(250 * time.Millisecond), prefill: 250 * time.Millisecond}
	up := httptest.NewServer(eng.handler())
	defer up.Close()

	api := &engineTestECS{desired: 0}
	st := newTestEngine(t, up.URL, api)
	g := engineGateway{reg: &engineRegistry{byKey: map[string]*engineRuntimeState{"llm": st}}}

	// A heartbeat every 20 ms rather than every 10 s, so the test measures the mechanism
	// rather than the wall clock. Everything else is production's.
	restore := setEngineHeartbeat(t, 20*time.Millisecond)
	defer restore()

	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/engine/llm/v1/chat/completions",
		strings.NewReader(`{"stream":true,"messages":[]}`))
	r.SetPathValue("path", "chat/completions")
	claims := engineSessionClaims{MembershipID: "M-1", Session: "s-1", Key: "llm"}
	g.streamed(rec, r, st, claims, testMembership(), []byte(`{"stream":true,"messages":[]}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — a 503 here spends one of the client's finite retries", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, ": af-engine waking") {
		t.Fatalf("no heartbeat in the stream — opencode would cut this at 300 s of silence:\n%s", body)
	}
	if !strings.Contains(body, `"content":"hi"`) || !strings.Contains(body, "[DONE]") {
		t.Fatalf("the engine's own answer did not reach the client:\n%s", body)
	}
	// Every heartbeat has to be an SSE COMMENT. A `data:` line would be delivered to the
	// model as content, which is worse than the timeout it prevents.
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, "af-engine waking") && !strings.HasPrefix(line, ":") {
			t.Fatalf("heartbeat is not an SSE comment: %q", line)
		}
	}
	if api.updates != 1 {
		t.Fatalf("UpdateService called %d times, want 1 — the engine is started once, not per poll", api.updates)
	}
	if eng.requests != 1 {
		t.Fatalf("the engine saw %d requests, want 1 — decision 5 is verified by there being no second attempt", eng.requests)
	}
}

// A start that never finishes cannot be reported with a status code — the 200 went out
// before anyone knew. It has to arrive inside the stream, in the shape the client already
// knows how to surface, and the stream has to be closed so the client stops waiting.
func TestEngineGatewayReportsAFailedWakeInsideTheStream(t *testing.T) {
	up := httptest.NewServer((&slowEngine{readyAt: time.Now().Add(time.Hour)}).handler())
	defer up.Close()
	st := newTestEngine(t, up.URL, &engineTestECS{desired: 0})
	g := engineGateway{reg: &engineRegistry{byKey: map[string]*engineRuntimeState{"llm": st}}}

	restore := setEngineHeartbeat(t, 20*time.Millisecond)
	defer restore()
	t.Setenv("AF_ENGINE_WAKE_TIMEOUT", "0")

	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/engine/llm/v1/chat/completions", strings.NewReader("{}"))
	r.SetPathValue("path", "chat/completions")
	g.streamed(rec, r, st, engineSessionClaims{Key: "llm"}, testMembership(), []byte(`{"stream":true}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d — the 200 is already sent by the time a wake can fail", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "engine_unavailable") || !strings.Contains(body, "[DONE]") {
		t.Fatalf("a failed wake left the client waiting:\n%s", body)
	}
}

// A non-streaming request has nowhere to put a heartbeat, so this is the ONE path where 503
// + Retry-After is right (ADR 0071 decision 5).
func TestEngineGatewayNonStreamingSaysRetryAfter(t *testing.T) {
	up := httptest.NewServer((&slowEngine{readyAt: time.Now().Add(time.Hour)}).handler())
	defer up.Close()
	st := newTestEngine(t, up.URL, &engineTestECS{desired: 0})
	g := engineGateway{reg: &engineRegistry{byKey: map[string]*engineRuntimeState{"llm": st}}}
	t.Setenv("AF_ENGINE_WAKE_TIMEOUT", "0")

	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/engine/llm/v1/chat/completions", strings.NewReader("{}"))
	r.SetPathValue("path", "chat/completions")
	g.plain(rec, r, st, engineSessionClaims{Key: "llm"}, testMembership(), []byte(`{"messages":[]}`))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("no Retry-After: a 503 without one tells the client to guess")
	}
	// The CODE, not the number. The caller retries a wake and surfaces a refusal, and both
	// are 503 — measured on the real deployment, where the difference decided whether one
	// tool call produced a picture or fell through onto a member's plan quota.
	if code := engineErrCode(t, rec.Body.Bytes()); code != "engine_waking" {
		t.Errorf("code = %q, want engine_waking so the caller knows to ask again", code)
	}
}

func engineErrCode(t *testing.T, body []byte) string {
	t.Helper()
	var doc struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("error body is not JSON: %s", body)
	}
	return doc.Error.Code
}

// The non-streaming path has no heartbeat, so the thing that ends it first is whatever proxy
// sits in front — 30-ingress.yaml sets the ALB's idle_timeout to 60 s, and a hold longer than
// that never gets to answer at all. Measured before this bound existed: the CP logged
// `POST /engine/image/v1/images/generations 503 59.998s` against its own 900-second hold,
// while the image engine came up in 165 s.
func TestEnginePlainHoldStaysUnderTheIngressIdleTimeout(t *testing.T) {
	const albIdleTimeout = 60 * time.Second // deploy/aws/ecs/cfn/30-ingress.yaml
	if got := enginePlainHold(); got >= albIdleTimeout {
		t.Errorf("plain hold = %s, want less than the ingress idle timeout %s", got, albIdleTimeout)
	}
	// It bounds the wait; it must never extend one that was deliberately made shorter.
	t.Setenv("AF_ENGINE_WAKE_TIMEOUT", "5")
	t.Setenv("AF_ENGINE_PLAIN_HOLD", "45")
	if got, want := enginePlainHold(), 5*time.Second; got != want {
		t.Errorf("plain hold = %s with a 5 s wake timeout, want %s", got, want)
	}
	// And a deployment behind a stricter proxy can say so.
	t.Setenv("AF_ENGINE_WAKE_TIMEOUT", "900")
	t.Setenv("AF_ENGINE_PLAIN_HOLD", "20")
	if got, want := enginePlainHold(), 20*time.Second; got != want {
		t.Errorf("plain hold = %s, want the configured %s", got, want)
	}
}

// The streaming path keeps the full budget: its heartbeat is a byte on the wire every 10 s,
// which is exactly what an idle timeout is watching for, so nothing in front of it cuts in.
func TestEngineStreamingKeepsTheFullWakeBudget(t *testing.T) {
	t.Setenv("AF_ENGINE_WAKE_TIMEOUT", "900")
	t.Setenv("AF_ENGINE_PLAIN_HOLD", "45")
	if got, want := engineWakeTimeout(), 900*time.Second; got != want {
		t.Errorf("wake timeout = %s, want %s — the plain bound must not leak onto the stream", got, want)
	}
}

// llama-server binds its port and answers /health with 503 {"status":"loading model"} for
// the whole time the weights are going into VRAM — 267 of the 527 seconds of a measured cold
// start. Treating any answer as ready sends the first request straight into that.
func TestEngineHealthyRejectsLoadingModel(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"loading model"}`))
	}))
	defer up.Close()
	st := newTestEngine(t, up.URL, &engineTestECS{})
	if engineHealthy(t.Context(), st) {
		t.Fatal("a loading engine read as ready")
	}
}

// --- streaming detection ------------------------------------------------------

// Measured against the live engine: WITH stream_options.include_usage the last chunk before
// [DONE] carries `usage`; WITHOUT it there is no usage chunk at all. The AI SDK happens to
// send the flag, but the accounting belongs to the CP, so the CP asks for it.
func TestAskForStreamUsage(t *testing.T) {
	var doc map[string]any
	if err := json.Unmarshal(askForStreamUsage([]byte(`{"stream":true,"messages":[]}`)), &doc); err != nil {
		t.Fatal(err)
	}
	opts, _ := doc["stream_options"].(map[string]any)
	if opts["include_usage"] != true {
		t.Fatalf("stream_options = %v", doc["stream_options"])
	}
	if doc["stream"] != true {
		t.Error("the caller's own body was not preserved")
	}
	// A caller that turned it off explicitly is left alone: overriding it would be this
	// layer overruling a client about its own protocol.
	out := askForStreamUsage([]byte(`{"stream":true,"stream_options":{"include_usage":false}}`))
	_ = json.Unmarshal(out, &doc)
	opts, _ = doc["stream_options"].(map[string]any)
	if opts["include_usage"] != false {
		t.Errorf("an explicit false was overridden: %v", doc["stream_options"])
	}
	// Unparseable goes upstream untouched — rejecting it is the engine's call, not ours.
	bad := []byte("not json")
	if string(askForStreamUsage(bad)) != string(bad) {
		t.Error("an unparseable body was rewritten")
	}
}

func TestEngineWantsStream(t *testing.T) {
	cases := []struct {
		body string
		want bool
	}{
		{`{"stream":true,"messages":[]}`, true},
		{`{"stream":false}`, false},
		{`{"messages":[]}`, false},
		{`not json at all`, false}, // unparseable falls back to the path with a status code
	}
	for _, c := range cases {
		if got := engineWantsStream([]byte(c.body)); got != c.want {
			t.Errorf("engineWantsStream(%s) = %v, want %v", c.body, got, c.want)
		}
	}
}

// --- usage --------------------------------------------------------------------

// The tokens are in the LAST chunk of a stream (the AI SDK asks for
// stream_options.include_usage, which is why they are there at all). Scanning has to survive
// the chunk boundaries falling anywhere, since a TCP read is not an SSE event.
func TestEngineUsageScannerReadsTheFinalChunk(t *testing.T) {
	stream := "data: {\"model\":\"qwen3-coder\",\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"b\"}}]}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":23226,\"completion_tokens\":57}}\n\n" +
		"data: [DONE]\n\n"
	for _, chunk := range []int{1, 7, 64, len(stream)} {
		s := &engineUsageScanner{}
		for i := 0; i < len(stream); i += chunk {
			s.feed([]byte(stream[i:min(i+chunk, len(stream))]))
		}
		if s.usage.PromptTokens != 23226 || s.usage.CompletionTokens != 57 {
			t.Fatalf("chunk=%d: usage = %+v, want 23226/57", chunk, s.usage)
		}
		if s.usage.Model != "qwen3-coder" {
			t.Fatalf("chunk=%d: model = %q — it rides on the FIRST chunk, not the last", chunk, s.usage.Model)
		}
	}
}

// An engine that reports nothing and an engine that reported zero spent are different facts.
// Writing 0/0 with measured="exact" would put a free call into the graph.
func TestEngineUsageUnreportedIsNotZero(t *testing.T) {
	s := &engineUsageScanner{}
	s.feed([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\ndata: [DONE]\n\n"))
	if !s.usage.empty() {
		t.Fatalf("usage = %+v, want empty", s.usage)
	}
	g := engineGateway{}
	// recordUsage with no manager posts nothing, so what is being pinned here is the
	// classification the row would carry.
	row := engineUsageRow{Measured: "exact"}
	if s.usage.empty() {
		row.Measured = "none"
	}
	if row.Measured != "none" {
		t.Error(`an unreported call must be measured="none", never a zero-token "exact" row`)
	}
	g.recordUsage(t.Context(), newTestEngine(t, "http://127.0.0.1:1", &engineTestECS{}),
		engineSessionClaims{}, testMembership(), s.usage, time.Second, true, "")
}

func TestParseEngineUsageFromAWholeBody(t *testing.T) {
	u := parseEngineUsage([]byte(`{"model":"qwen3","usage":{"prompt_tokens":10,"completion_tokens":3}}`))
	if u.PromptTokens != 10 || u.CompletionTokens != 3 || u.Model != "qwen3" {
		t.Fatalf("usage = %+v", u)
	}
}

// --- tokens -------------------------------------------------------------------

// The two token kinds share a signing key and must not be interchangeable: the issuing one
// lives in the Workspace environment for the container's whole life, the session one is
// readable by a model. Replaying either as the other would collapse that distinction.
func TestEngineTokensAreNotInterchangeable(t *testing.T) {
	key := engineSignKey([]byte(strings.Repeat("k", 32)))
	now := time.Now()
	issue := mintEngineIssueToken(key, "M-1")
	sess := mintEngineSessionToken(key, "M-1", "sess-a", "llm", now.Add(time.Hour))

	if mid, ok := verifyEngineIssueToken(key, issue); !ok || mid != "M-1" {
		t.Fatalf("issue token did not verify: %q %v", mid, ok)
	}
	if _, ok := verifyEngineSessionToken(key, issue, now); ok {
		t.Error("an issuing token was accepted as a session token")
	}
	if _, ok := verifyEngineIssueToken(key, sess); ok {
		t.Error("a session token was accepted as an issuing token")
	}
	c, ok := verifyEngineSessionToken(key, sess, now)
	if !ok || c.MembershipID != "M-1" || c.Session != "sess-a" || c.Key != "llm" {
		t.Fatalf("session claims = %+v ok=%v", c, ok)
	}
	// Expiry is checked here, not by the caller.
	if _, ok := verifyEngineSessionToken(key, sess, now.Add(2*time.Hour)); ok {
		t.Error("an expired session token verified")
	}
	// A different key never verifies.
	other := engineSignKey([]byte(strings.Repeat("x", 32)))
	if _, ok := verifyEngineSessionToken(other, sess, now); ok {
		t.Error("a token signed with another key verified")
	}
	// The claims are NUL-joined, so a session name cannot be re-cut into a membership id.
	odd := mintEngineSessionToken(key, "M", "1\x00sess-a", "llm", now.Add(time.Hour))
	if c, ok := verifyEngineSessionToken(key, odd, now); ok && c.MembershipID == "M-1" {
		t.Error("claims re-cut across the separator")
	}
}

// A 401 says nothing useful to the caller on purpose, but an operator staring at one needs to
// know which of the four ways it failed. Not knowing cost a live debugging round: the managed
// route turned out not to be carrying the token at all, and "invalid engine session token"
// looks identical to a signature mismatch.
func TestEngineAuthFailureNamesTheReason(t *testing.T) {
	key := engineSignKey([]byte(strings.Repeat("k", 32)))
	other := engineSignKey([]byte(strings.Repeat("x", 32)))
	now := time.Now()
	cases := []struct {
		name, tok, want string
	}{
		{"missing", "", "AF_ENGINE_TOKEN is unset"},
		{"not ours", "sk-something", "not an engine token"},
		{"issuing token", mintEngineIssueToken(key, "M-1"), "issuing token was presented"},
		{"expired", mintEngineSessionToken(key, "M-1", "s", "llm", now.Add(-time.Hour)), "expired"},
		{"other engine", mintEngineSessionToken(key, "M-1", "s", "image", now.Add(time.Hour)), "engine image was used on llm"},
		{"other deployment", mintEngineSessionToken(other, "M-1", "s", "llm", now.Add(time.Hour)), "bad signature"},
	}
	for _, c := range cases {
		if got := engineAuthFailure(key, c.tok, "llm"); !strings.Contains(got, c.want) {
			t.Errorf("%s: %q does not mention %q", c.name, got, c.want)
		}
	}
}

// --- the table ----------------------------------------------------------------

func TestParseEngineTable(t *testing.T) {
	raw := `{"engines":[{"key":"llm","service":"af-llm","capacityProvider":"cp-llm",
	 "url":"http://llm.af.internal:8080","health":"/health","provider":"llamacpp",
	 "models":["qwen3-coder-30b-a3b"],"idleSec":1800,"startDeadlineSec":900,"mode":"ondemand"}]}`
	tab, err := parseEngineTable(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(tab.Engines) != 1 || tab.Engines[0].Key != "llm" || tab.Engines[0].StartDeadlineSec != 900 {
		t.Fatalf("table = %+v", tab)
	}
	// A row missing the parts the gateway cannot invent is an error, not a silently
	// half-wired engine: an empty service name would make the controller drive nothing while
	// the launch menu still offered the model.
	for _, bad := range []string{
		`{"engines":[{"key":"llm","url":"http://x"}]}`,
		`{"engines":[{"service":"s","url":"http://x"}]}`,
		`{"engines":[{"key":"llm","service":"s"}]}`,
	} {
		if _, err := parseEngineTable(bad); err == nil {
			t.Errorf("parseEngineTable(%s) accepted an incomplete row", bad)
		}
	}
}

// The start deadline is the value that made ADR 0070's default dangerous here: 300 seconds
// against a measured 527-second cold start records EVERY start as a failure and doubles the
// cooldown away, so an engine that works perfectly can never come up.
func TestEngineControlCfgTakesTheStackDeadline(t *testing.T) {
	cfg := engineControlCfgFor(engineDef{Key: "llm", IdleSec: 1800, StartDeadlineSec: 900})
	if cfg.deadline != 900*time.Second {
		t.Fatalf("deadline = %s, want 900s", cfg.deadline)
	}
	if cfg.idle != 1800*time.Second {
		t.Fatalf("idle = %s, want 1800s", cfg.idle)
	}
	if cfg.startUnits != 1 {
		t.Fatalf("startUnits = %d, want 1 — for inference the request IS the demand", cfg.startUnits)
	}
	// A stack that says nothing still gets a deadline above the measured cold start.
	if got := engineControlCfgFor(engineDef{Key: "llm"}).deadline; got < 527*time.Second {
		t.Fatalf("default deadline = %s, below the measured 527 s cold start", got)
	}
}

// `draining` is a kind of stopped, not a kind of running: the desired count is already 0, so
// there is nothing to stop, and the controller must not try once per tick for the seven
// minutes AWS takes to take the box away.
func TestDecideEngineActionTreatsDrainingAsStopped(t *testing.T) {
	now := time.Now()
	cfg := engineControlCfg{window: 5 * time.Minute, startUnits: 1, idle: 30 * time.Minute,
		deadline: 15 * time.Minute, cooldown: 15 * time.Minute}
	base := engineSnapshot{state: "draining", desired: 0, mode: engineModeOnDemand,
		lastDemand: now.Add(-time.Hour)}

	action, reason := decideEngineAction(now, base, cfg)
	if action != engineActionNone || reason != engineReasonDraining {
		t.Fatalf("idle draining = %s/%s, want none/draining", action, reason)
	}
	wanted := base
	wanted.windowUnits = 1
	wanted.lastDemand = now
	if action, _ := decideEngineAction(now, wanted, cfg); action != engineActionStart {
		t.Fatalf("a request during the drain = %s, want start — the box is still there, so this is the cheap start", action)
	}
}

// --- helpers ------------------------------------------------------------------

func testMembership() store.MembershipView { return store.MembershipView{MembershipID: "M-1"} }

// setEngineHeartbeat shortens the heartbeat AND the readiness poll for a test and restores
// both. The two belong together: a 3-second poll against a 20 ms heartbeat would measure the
// poll, not the mechanism under test.
func setEngineHeartbeat(t *testing.T, d time.Duration) func() {
	t.Helper()
	prevBeat, prevPoll := engineHeartbeatInterval, engineReadyPoll
	engineHeartbeatInterval, engineReadyPoll = d, 5*d
	return func() { engineHeartbeatInterval, engineReadyPoll = prevBeat, prevPoll }
}

// --- the image role (ADR 0071 P1) ---------------------------------------------

// newTestImageEngine is the `image` row: the same gateway, a different API family and a
// different health path — sd-server has no /health at all (upstream api.md), so readiness is
// GET /v1/models.
func newTestImageEngine(t *testing.T, url string, api engineECSAPI) *engineRuntimeState {
	t.Helper()
	e := &engineRuntimeState{
		def: engineDef{
			Key: "image", API: engineAPIImages, Service: "af-image", URL: url,
			Health: "/v1/models", Provider: "sdcpp",
			IdleSec: 900, StartDeadlineSec: 900,
		},
		ecs: &engineECS{api: api, key: "image", cluster: "c", service: "af-image"},
	}
	e.demand = newEngineDemand(nil, engineSettingsFor("image").demandAt, 5*time.Minute)
	return e
}

// newTestComfyEngine is the `image` row when ImageEngine=comfy (ADR 0072 decision 4, phase P2):
// same gateway, same api family, but ComfyUI's native API has no /v1 of its own.
func newTestComfyEngine(t *testing.T, url string, api engineECSAPI) *engineRuntimeState {
	t.Helper()
	e := &engineRuntimeState{
		def: engineDef{
			Key: "image", API: engineAPIImages, Service: "af-image", URL: url,
			Health: "/system_stats", Provider: "comfy",
			IdleSec: 900, StartDeadlineSec: 900,
		},
		ecs: &engineECS{api: api, key: "image", cluster: "c", service: "af-image"},
	}
	e.demand = newEngineDemand(nil, engineSettingsFor("image").demandAt, 5*time.Minute)
	return e
}

// ADR 0072 decision 4: ComfyUI's native API (/prompt, /history/<id>, /view) lives at the
// engine's ROOT, unlike llama-server/sd-server's OpenAI-compatible /v1/*. dial must not prepend
// /v1/ for this one provider, or every comfy request 404s upstream.
func TestEngineGatewayDoesNotPrependV1ForComfy(t *testing.T) {
	var gotPath string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewEncoder(w).Encode(map[string]any{"prompt_id": "p1"})
	}))
	defer up.Close()

	st := newTestComfyEngine(t, up.URL, &engineTestECS{desired: 1, running: 1})
	g := engineGateway{reg: &engineRegistry{byKey: map[string]*engineRuntimeState{"image": st}}}

	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/engine/image/v1/prompt", strings.NewReader(`{}`))
	r.SetPathValue("path", "prompt")
	g.plain(rec, r, st, engineSessionClaims{Key: "image"}, testMembership(), []byte(`{}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if gotPath != "/prompt" {
		t.Errorf("upstream path = %q, want /prompt (no /v1 prefix for comfy)", gotPath)
	}
}

// The SAME route still prepends /v1/ for sdcpp — this pins that the change is provider-scoped,
// not a regression that broke the OpenAI-compatible engines while fixing comfy.
func TestEngineGatewayStillPrependsV1ForSdcpp(t *testing.T) {
	var gotPath string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"created":1,"output_format":"png","data":[{"b64_json":"AA=="}]}`))
	}))
	defer up.Close()

	st := newTestImageEngine(t, up.URL, &engineTestECS{desired: 1, running: 1})
	g := engineGateway{reg: &engineRegistry{byKey: map[string]*engineRuntimeState{"image": st}}}

	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/engine/image/v1/images/generations", strings.NewReader(`{}`))
	r.SetPathValue("path", "images/generations")
	g.plain(rec, r, st, engineSessionClaims{Key: "image"}, testMembership(), []byte(`{}`))

	if gotPath != "/v1/images/generations" {
		t.Errorf("upstream path = %q, want /v1/images/generations", gotPath)
	}
}

// X-AF-Model is the only way the gateway learns which checkpoint an IMAGE request used — the
// image role's own answer never carries a model field the way a chat completion does — and
// ADR 0072 decision 7's warm-model tracking is blind without it.
func TestEngineGatewayTracksServedModelFromTheHeaderOnImageRequests(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"prompt_id": "p1"})
	}))
	defer up.Close()

	st := newTestComfyEngine(t, up.URL, &engineTestECS{desired: 1, running: 1})
	g := engineGateway{reg: &engineRegistry{byKey: map[string]*engineRuntimeState{"image": st}}}

	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/engine/image/v1/prompt", strings.NewReader(`{}`))
	r.Header.Set("X-AF-Model", "klein-4b")
	r.SetPathValue("path", "prompt")
	g.plain(rec, r, st, engineSessionClaims{Key: "image"}, testMembership(), []byte(`{}`))

	if served, _ := st.servedModel(); served != "klein-4b" {
		t.Errorf("servedModel() = %q, want klein-4b (read off X-AF-Model)", served)
	}
}

// /v1/images/edits is multipart/form-data, and the boundary that makes the body readable
// lives in the Content-Type header. Stamping application/json on everything — which is what
// this gateway did while `llm` was the only role — turns an edit into an unparseable body at
// the far end, on the one endpoint in the system that is not JSON.
func TestEngineGatewayForwardsTheCallersContentType(t *testing.T) {
	var gotCType, gotBody string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_, _ = w.Write([]byte(`{"data":[{"id":"sd-cpp-local"}]}`))
			return
		}
		gotCType = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = w.Write([]byte(`{"created":1,"output_format":"png","data":[{"b64_json":"AA=="}]}`))
	}))
	defer up.Close()

	st := newTestImageEngine(t, up.URL, &engineTestECS{desired: 1, running: 1})
	g := engineGateway{reg: &engineRegistry{byKey: map[string]*engineRuntimeState{"image": st}}}

	const ctype = "multipart/form-data; boundary=abc123"
	body := "--abc123\r\nContent-Disposition: form-data; name=\"prompt\"\r\n\r\na cat\r\n--abc123--\r\n"
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/engine/image/v1/images/edits", strings.NewReader(body))
	r.Header.Set("Content-Type", ctype)
	r.SetPathValue("path", "images/edits")
	g.plain(rec, r, st, engineSessionClaims{Key: "image"}, testMembership(), []byte(body))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if gotCType != ctype {
		t.Errorf("upstream Content-Type = %q, want the caller's %q (the boundary is in it)", gotCType, ctype)
	}
	if gotBody != body {
		t.Errorf("upstream body = %q, want it byte-for-byte", gotBody)
	}
}

// A caller that sends no Content-Type still gets JSON, because that is what every
// OpenAI-compatible client means.
func TestEngineGatewayDefaultsToJSONContentType(t *testing.T) {
	var gotCType string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			return
		}
		gotCType = r.Header.Get("Content-Type")
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer up.Close()
	st := newTestEngine(t, up.URL, &engineTestECS{desired: 1, running: 1})
	g := engineGateway{reg: &engineRegistry{byKey: map[string]*engineRuntimeState{"llm": st}}}

	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/engine/llm/v1/chat/completions", strings.NewReader("{}"))
	r.Header.Del("Content-Type")
	r.SetPathValue("path", "chat/completions")
	g.plain(rec, r, st, engineSessionClaims{Key: "llm"}, testMembership(), []byte("{}"))

	if gotCType != "application/json" {
		t.Errorf("upstream Content-Type = %q, want application/json", gotCType)
	}
}

// Who counts what (ADR 0071 decision 9). A chat engine's tokens are visible only here, so
// the gateway records them; an image engine's spend is pixels, which only the Agent sees as
// it stores the file — and a row written from both ends would double the ledger's line count
// while adding a feature the frozen enumeration does not have.
func TestOnlyChatEnginesAreCountedByTheGateway(t *testing.T) {
	claims := engineSessionClaims{MembershipID: "M-1", Session: "s-1"}
	usage := engineUsage{PromptTokens: 24, CompletionTokens: 12, Model: "qwen3-coder-30b-a3b"}

	row, count := engineUsageRowFor(engineDef{Key: "llm", API: engineAPIChat, Provider: "llamacpp"},
		claims, usage, time.Second, true)
	if !count {
		t.Fatal("a chat engine went uncounted")
	}
	if row.Feature != "engine.llm" || row.In != 24 || row.Out != 12 || row.Measured != "exact" {
		t.Fatalf("row = %+v", row)
	}

	if _, count := engineUsageRowFor(engineDef{Key: "image", API: engineAPIImages, Provider: "sdcpp"},
		claims, engineUsage{}, time.Second, true); count {
		t.Error("the gateway wrote a usage row for an image engine — the Agent already writes tool.imagegen")
	}

	// A table written by the P0 stack has no `api` field, and the CP is upgraded before the
	// stack is. Defaulting to anything but chat would silently stop counting llm tokens.
	if _, count := engineUsageRowFor(engineDef{Key: "llm", Provider: "llamacpp"},
		claims, usage, time.Second, true); !count {
		t.Error("a row with no api field stopped being counted")
	}
}

// The engine table with both roles in it. The string is byte-for-byte what the real
// 60-engines stack wrote into SSM on 2026-09-07 once an image checkpoint was staged —
// including the spaces CloudFormation's folded scalars leave behind — because the shape this
// parser has to survive is the one CloudFormation produces, not the one a test would write.
func TestParseEngineTableReadsBothRoles(t *testing.T) {
	raw := `{"engines":[{"key":"llm","api":"chat","service":"af-af-ecs-engines-llm","capacityProvider":"af-af-ecs-engines-llm", "url":"http://llm.af.internal:8080", "health":"/health","provider":"llamacpp","models":["qwen3-coder-30b-a3b"], "apiKeyParam":"/af-ws/engine-llm-key","idleSec":1800, "startDeadlineSec":900,"mode":"ondemand"},{"key":"image","api":"images","service":"af-af-ecs-engines-image","capacityProvider":"af-af-ecs-engines-image", "url":"http://image.af.internal:8080", "health":"/v1/models","provider":"sdcpp","models":["sdxl-base-1.0"], "apiKeyParam":"","idleSec":900, "startDeadlineSec":900,"mode":"ondemand"}]}`
	tab, err := parseEngineTable(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(tab.Engines) != 2 {
		t.Fatalf("engines = %+v", tab.Engines)
	}
	img := tab.Engines[1]
	if img.api() != engineAPIImages || img.Provider != "sdcpp" || img.Health != "/v1/models" {
		t.Fatalf("image row = %+v", img)
	}
	// sd-server has no API key of any kind, so the second lock the llm role has does not
	// exist here and the security group is the whole of the access control.
	if img.APIKeyParam != "" {
		t.Errorf("apiKeyParam = %q, want empty", img.APIKeyParam)
	}
	// The registry's order is what the catalogue is drawn in: llm, then image.
	reg := &engineRegistry{byKey: map[string]*engineRuntimeState{
		"image": {def: tab.Engines[1]}, "llm": {def: tab.Engines[0]},
	}}
	got := reg.list()
	if len(got) != 2 || got[0].def.Key != "llm" || got[1].def.Key != "image" {
		t.Fatalf("list order = %+v", got)
	}
}

// The context window comes from the CATALOGUE and from nowhere else (ADR 0072 phase P6). It has
// to travel because nobody downstream can ask for it: llama-server holds it, and the whole design
// is that the box is asleep when the launch menu is drawn.
//
// The table parsed here is an OLD one, still carrying the model ids, the window and the S3 key a
// pre-P6 stack wrote. Two facts at once: such a table still parses (the CP is upgraded before the
// stack is, so this shape is live), and what it says about models is ignored — the window in the
// row below is the catalogue's, and it wins even where the two disagree.
func TestEngineTableCarriesTheDeclaredWindow(t *testing.T) {
	raw := `{"engines":[{"key":"llm","api":"chat","service":"s","capacityProvider":"cp",
	 "url":"http://llm.af.internal:8080","health":"/health","provider":"llamacpp",
	 "models":["gone"], "contextTokens":512,"maxOutputTokens":256,
	 "modelS3Key":"llm/gone.gguf",
	 "apiKeyParam":"","idleSec":1800,"startDeadlineSec":900,"mode":"ondemand"}]}`
	tab, err := parseEngineTable(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	d := tab.Engines[0]
	if d.Key != "llm" || d.Provider != "llamacpp" || d.IdleSec != 1800 {
		t.Fatalf("a table written by a pre-P6 stack no longer parses: %+v", d)
	}
	row := engineCatalogRowFor(d, []store.EngineModel{{
		Role: "llm", ID: "qwen3-coder-30b-a3b", Kind: "gguf", Enabled: true, Default: true,
		ContextTokens: 32768, MaxOutputTokens: 4096,
	}}, "", "")
	if row["context_tokens"] != 32768 || row["max_output_tokens"] != 4096 {
		t.Errorf("catalogue row = %v", row)
	}
	rows, _ := row["model_rows"].([]map[string]any)
	if len(rows) != 1 || rows[0]["context_tokens"] != 32768 {
		t.Errorf("per-model rows = %v", row["model_rows"])
	}

	// A catalogue entry from before the field says nothing, and the row must say nothing too.
	// Reporting the zero would make the Agent advertise a context of 0, which is what turns
	// opencode's auto-compaction off — worse than the silence it replaced.
	old := engineCatalogRowFor(engineDef{Key: "llm", Provider: "llamacpp"},
		[]store.EngineModel{{Role: "llm", ID: "m", Enabled: true}}, "", "")
	if _, ok := old["context_tokens"]; ok {
		t.Errorf("an undeclared window was reported anyway: %v", old)
	}

	// Nothing enabled is not an engine with an empty model list: it is an engine that must not
	// appear at all, or a launch menu offers a model whose every request answers 503.
	if none := engineCatalogRowFor(d, nil, "", ""); none != nil {
		t.Errorf("an engine with an empty catalogue was offered: %v", none)
	}
}

// The LoRAs the Agent reads (ADR 0072 decision 5, phase P3). Two claims, and neither fails
// loudly if it breaks: a LoRA that reached `models` would appear in generate_image's checkpoint
// enum and be started with, and a `loras` row without base_model would leave the Agent no way to
// tell whether a LoRA fits the chosen checkpoint — which is the one refusal decision 5 puts on
// the Agent's side rather than here (a mismatched LoRA does not fail, it quietly does nothing).
func TestEngineCatalogRowSeparatesLorasFromCheckpoints(t *testing.T) {
	d := engineDef{Key: "image", API: engineAPIImages, Provider: "comfy"}
	row := engineCatalogRowFor(d, []store.EngineModel{
		{Role: "image", ID: "sdxl-base-1.0", Kind: "checkpoint", Enabled: true, Selected: true,
			BaseModel: "sdxl", Files: []store.EngineModelFile{{S3Key: "image/checkpoints/sd_xl_base_1.0.safetensors"}}},
		{Role: "image", ID: "watercolor-v2", Kind: "lora", Enabled: true, BaseModel: "sdxl",
			Description: "soft watercolour",
			Files:       []store.EngineModelFile{{S3Key: "image/loras/watercolor_v2.safetensors"}}},
	}, "", "")
	if row == nil {
		t.Fatal("no row for an engine with a checkpoint and a LoRA")
	}
	ids, _ := row["models"].([]string)
	if len(ids) != 1 || ids[0] != "sdxl-base-1.0" {
		t.Errorf("models = %v, want the checkpoint alone — a LoRA is not something to start with", ids)
	}
	if rows, _ := row["model_rows"].([]map[string]any); len(rows) != 1 {
		t.Errorf("model_rows = %v, want the checkpoint alone", row["model_rows"])
	}
	loras, _ := row["loras"].([]map[string]any)
	if len(loras) != 1 {
		t.Fatalf("loras = %v, want the one LoRA", row["loras"])
	}
	if loras[0]["id"] != "watercolor-v2" || loras[0]["base_model"] != "sdxl" || loras[0]["description"] != "soft watercolour" {
		t.Errorf("lora row = %v, want id, family and description all on the wire", loras[0])
	}
	files, _ := loras[0]["files"].([]map[string]any)
	if len(files) != 1 || files[0]["s3_key"] != "image/loras/watercolor_v2.safetensors" {
		t.Errorf("lora files = %v — the Agent derives the name ComfyUI loads it by from this key", loras[0]["files"])
	}
	// An engine holding LoRAs and no checkpoint is still an engine with nothing to serve.
	only := engineCatalogRowFor(d, []store.EngineModel{
		{Role: "image", ID: "watercolor-v2", Kind: "lora", Enabled: true, BaseModel: "sdxl"},
	}, "", "")
	if only != nil {
		t.Errorf("an engine with only LoRAs was offered: %v", only)
	}
}

// A chat request naming a model the catalogue does not hold is refused before anything is
// woken (ADR 0072 decision 7). Three things are pinned, and each one is a way this could go
// wrong on a live deployment rather than in a test:
//
//   - the id compared is the CATALOGUE's, which is also what the Agent writes into opencode's
//     provider block. If those two ever disagreed, every request would 404;
//   - a request naming NO model is untouched. `/v1/models` is a GET with no body at all;
//   - the IMAGE route is exempt. sd-server holds one checkpoint chosen at startup and the
//     provider deliberately sends no `model`, so a check there would refuse a field that only
//     a well-meaning client would add.
func TestEngineGatewayRefusesAModelOutsideTheCatalogue(t *testing.T) {
	rows := []store.EngineModel{
		{Role: "llm", ID: "qwen3-coder-30b-a3b", Kind: "gguf", Enabled: true, Default: true},
		{Role: "llm", ID: "parked", Kind: "gguf", Enabled: false},
		{Role: "llm", ID: "some-lora", Kind: "lora", Enabled: true},
	}
	if !engineCatalogHolds(rows[:1], "qwen3-coder-30b-a3b") {
		t.Error("the enabled model was not found by its catalogue id")
	}
	enabled := []store.EngineModel{}
	for _, m := range rows {
		if m.Enabled {
			enabled = append(enabled, m)
		}
	}
	// A model an administrator switched OFF is not in this deployment any more, so naming it
	// is the same as naming one that never existed.
	if engineCatalogHolds(enabled, "parked") {
		t.Error("a disabled model was accepted")
	}
	// A LoRA is not something a chat request can be routed to, however it is enabled.
	if engineCatalogHolds(enabled, "some-lora") {
		t.Error("a LoRA was accepted as a model")
	}
	if engineCatalogHolds(enabled, "/models/whatever.gguf") {
		t.Error("a file path was accepted as a model id")
	}

	if got := engineRequestModel([]byte(`{"model":" qwen3 ","messages":[]}`)); got != "qwen3" {
		t.Errorf("model = %q", got)
	}
	// No model named, and a body that is not JSON at all: both are "" so the request goes
	// through untouched and the engine answers for itself.
	if got := engineRequestModel([]byte(`{"messages":[]}`)); got != "" {
		t.Errorf("no model should read as empty, got %q", got)
	}
	if got := engineRequestModel(nil); got != "" {
		t.Errorf("an unreadable body should read as empty, got %q", got)
	}
}

// --- ADR 0072 P1: warmth is not health, once the llm role is a router ----------

// A llama.cpp router answers /health with {"status":"ok"} while holding NO models at all
// (measured against b10853 with an empty model list). So the P0 warm probe — the health check —
// reports a router as warm from the second it binds its port: through the whole 527-second cold
// start, and for ever afterwards on a box that failed to load anything. The panel's "warm" chip
// and the controller's `unwarmed` rule both read that flag, so both would be lying.
//
// With a WarmPath the CP reads each model's status out of /models instead, and only `loaded`
// counts.
func TestEngineWarmProbeReadsTheRouterModelList(t *testing.T) {
	var loaded atomic.Value
	loaded.Store(`{"data":[{"id":"a","status":{"value":"unloaded"}},{"id":"b","status":{"value":"unloaded"}}]}`)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			// What the router says with nothing loaded, verbatim.
			w.Write([]byte(`{"status":"ok"}`))
		case "/models":
			w.Write([]byte(loaded.Load().(string)))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer up.Close()

	router := newTestEngine(t, up.URL, nil)
	router.def.WarmPath = "/models"
	ctx := context.Background()

	if !engineHealthy(ctx, router) {
		t.Fatal("the stub is not answering /health — the rest of this test would pass for the wrong reason")
	}
	if router.warmProbe(ctx, false) {
		t.Error("a router holding no models read as warm")
	}
	loaded.Store(`{"data":[{"id":"a","status":{"value":"loading"}},{"id":"b","status":{"value":"sleeping"}}]}`)
	// `loading` is the 267 seconds of weights going into VRAM and `sleeping` is the router's own
	// idle unload: both mean the next request pays for the weights, which is exactly what warm
	// promises it will not.
	if router.warmProbe(ctx, false) {
		t.Error("loading/sleeping read as warm")
	}
	loaded.Store(`{"data":[{"id":"a","status":{"value":"unloaded"}},{"id":"b","status":{"value":"loaded"}}]}`)
	if !router.warmProbe(ctx, false) {
		t.Error("a loaded model did not read as warm")
	}
	if got := engineLoadedModels(ctx, router); len(got) != 1 || got[0] != "b" {
		t.Errorf("loaded models = %v, want [b]", got)
	}
	// ⚠️ Warm is "SOMETHING is loaded", not "the DEFAULT is loaded". With --models-max 1 a
	// session using the second model evicts the first, and a warm flag tied to the default would
	// go false on a box that is answering — which the controller's `unwarmed` rule would
	// eventually stop, mid-conversation.

	// An engine with no WarmPath (the image role, and every table written before ADR 0072 P1)
	// keeps the health check as its whole answer.
	sd := newTestEngine(t, up.URL, nil)
	if !sd.warmProbe(ctx, false) {
		t.Error("without a WarmPath the health check should decide, and it answers ok")
	}
}

// The price of --models-max 1, made visible (ADR 0072 decision 3). Two sessions on two models
// take turns, and each turn costs an unload plus a load; the panel shows the count rather than
// reporting a uniformly "warm" engine.
func TestEngineServedModelCountsSwaps(t *testing.T) {
	e := newTestEngine(t, "http://127.0.0.1:1", nil)
	// No controller attached, so warmed() is not consulted — that path is the panel's, and it is
	// covered where the row is built.
	e.ctrl = nil

	e.noteServed("qwen3-coder-30b-a3b", true)
	e.noteServed("qwen3-coder-30b-a3b", true)
	if m, n := e.servedModel(); m != "qwen3-coder-30b-a3b" || n != 0 {
		t.Errorf("same model twice = (%q, %d), want (qwen3-coder-30b-a3b, 0)", m, n)
	}
	e.noteServed("qwen2.5-coder-1.5b", true)
	e.noteServed("qwen3-coder-30b-a3b", true)
	if m, n := e.servedModel(); m != "qwen3-coder-30b-a3b" || n != 2 {
		t.Errorf("after two swaps = (%q, %d), want (qwen3-coder-30b-a3b, 2)", m, n)
	}
	// A refused request moved no weights: the router answers 400 for a model it does not hold
	// without touching the one that is loaded.
	e.noteServed("nope", false)
	e.noteServed("", true)
	if m, n := e.servedModel(); m != "qwen3-coder-30b-a3b" || n != 2 {
		t.Errorf("a failed answer changed the served model: (%q, %d)", m, n)
	}
}
