package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
	return &ecs.DescribeContainerInstancesOutput{ContainerInstances: []ecstypes.ContainerInstance{{
		ContainerInstanceArn: aws.String("arn:ci/i-1"), CapacityProviderName: aws.String(f.instance),
	}}}, nil
}

// newTestEngine wires a gateway around one stubbed engine, with no store behind it: the
// membership lookup is the part these tests are not about, so the handler under test is
// exercised through the pieces that do not need one.
func newTestEngine(t *testing.T, url string, api engineECSAPI) *engineRuntimeState {
	t.Helper()
	def := engineDef{
		Key: "llm", Service: "af-llm", URL: url, Health: "/health",
		Provider: "llamacpp", Models: []string{"qwen3-coder-30b-a3b"},
		IdleSec: 1800, StartDeadlineSec: 900,
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
		engineSessionClaims{}, testMembership(), s.usage, time.Second, true)
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
