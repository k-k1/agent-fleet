package main

// What the instance has not synced yet, and what the gateway does about it
// (ADR 0072 P2 欠落 7 / open question 3).

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// fakePendingSSM answers the one parameter this reads, and counts the reads so the cache can
// be asserted on: one SSM call per completion is the cost this was written to avoid.
type fakePendingSSM struct {
	value string
	err   error
	reads int
	name  string
}

func (f *fakePendingSSM) GetParameter(_ context.Context, in *ssm.GetParameterInput,
	_ ...func(*ssm.Options)) (*ssm.GetParameterOutput, error) {
	f.reads++
	f.name = aws.ToString(in.Name)
	if f.err != nil {
		return nil, f.err
	}
	return &ssm.GetParameterOutput{Parameter: &ssmtypes.Parameter{Value: aws.String(f.value)}}, nil
}

// pendingEngine is a comfy image engine with a two-model catalogue, the shape 欠落 7 was
// measured on: one model is what the instance started with, the other is still coming down.
func pendingEngine(t *testing.T, ssmc engineSSMAPI) (engineGateway, *engineRuntimeState, store.Store) {
	t.Helper()
	st := testSettingsStore(t)
	e := newTestComfyEngine(t, "http://127.0.0.1:1", &engineTestECS{desired: 1, running: 1})
	e.settings, e.ctrl = st, nil
	e.catalog = newEngineCatalog(st, "image")
	e.pending = newEnginePending(ssmc, "/af-ws/engines", "image")
	for _, m := range []store.EngineModel{
		{Role: "image", ID: "flux-2-klein", Kind: "checkpoint", BaseModel: "flux2-klein", Enabled: true, Selected: true,
			Files: []store.EngineModelFile{{Flag: "--diffusion-model", S3Key: "image/diffusion_models/flux-2-klein-4b.safetensors"}}},
		{Role: "image", ID: "z-image-turbo", Kind: "checkpoint", BaseModel: "zimage", Enabled: true,
			Files: []store.EngineModelFile{
				{Flag: "--diffusion-model", S3Key: "image/diffusion_models/z_image_turbo_bf16.safetensors"},
				{Flag: "--vae", S3Key: "image/vae/ae.safetensors"},
			}},
	} {
		if err := st.PutEngineModel(t.Context(), m); err != nil {
			t.Fatal(err)
		}
	}
	e.catalog.invalidate()
	return engineGateway{reg: &engineRegistry{byKey: map[string]*engineRuntimeState{"image": e}}}, e, st
}

func pendingAsk(t *testing.T, g engineGateway, e *engineRuntimeState, model string) *apiError {
	t.Helper()
	r := httptest.NewRequest("POST", "/engine/image/v1/prompt", strings.NewReader(`{}`))
	// An image request names no model — ComfyUI's API has no such field — so the Agent states
	// it in the header the usage accounting already reads.
	r.Header.Set("X-AF-Model", model)
	return g.pendingGuard(t.Context(), e, []byte(`{}`), r)
}

// 🔴 The window this exists for. The sidecar releases the engine as soon as the START model is
// down and keeps fetching: `engine may start; 6 file(s) still to sync`. Measured on af-sandbox,
// a second instance spent ~270 s on 12 files / 48 GB, and a request for one of the others got
// ComfyUI's bare `Value not in list: unet_name: 'z_image_turbo_bf16.safetensors' not in […]`
// — a 400, from an engine that was telling the truth, which nothing in the fleet retries.
func TestEngineGatewayWaitsForAModelTheInstanceIsStillSyncing(t *testing.T) {
	ssmc := &fakePendingSSM{value: `["image/diffusion_models/z_image_turbo_bf16.safetensors"]`}
	g, e, _ := pendingEngine(t, ssmc)

	aerr := pendingAsk(t, g, e, "z-image-turbo")
	if aerr == nil {
		t.Fatal("a model whose file is still being synced went straight through — that is the bare 400")
	}
	if aerr.status != http.StatusServiceUnavailable || aerr.code != "engine_waking" {
		t.Fatalf("refusal = %d %s, want 503 engine_waking (the one every caller already retries)",
			aerr.status, aerr.code)
	}
	if !strings.Contains(aerr.message, "z-image-turbo") {
		t.Errorf("the refusal does not say which model: %s", aerr.message)
	}
	if ssmc.name != "/af-ws/engines/image/pending" {
		t.Errorf("read %q, want the pending parameter beside the active set", ssmc.name)
	}

	// 🔴 The positive control. The model the instance DID start with is on the same parameter's
	// other side, and it must go through — a guard that held everything would pass the
	// assertion above while making the engine unusable.
	if aerr := pendingAsk(t, g, e, "flux-2-klein"); aerr != nil {
		t.Fatalf("the model the instance started with was held: %v", aerr.message)
	}
	// And one file of a model being pending is enough: the vae of z-image is already there,
	// the unet is not, and the graph needs both.
	if aerr := pendingAsk(t, g, e, "z-image-turbo"); aerr == nil {
		t.Error("a model with ONE missing file went through")
	}

	// The catalogue's own TTL, not one SSM call per request: the reads above are one.
	if ssmc.reads != 1 {
		t.Errorf("SSM was read %d times for four requests — this is on the completion path", ssmc.reads)
	}
}

// 🔴 Through `serve`, because the guard being CALLED is the half a unit test of the guard
// cannot see: the check can be perfect and unreachable. What the caller gets has to be the
// retryable refusal with a Retry-After on it, which is the whole difference from the 400.
func TestEngineServeAnswersWakingWhileTheModelIsStillSyncing(t *testing.T) {
	// A short hold throughout: what is under test is WHICH refusal comes back, and the wait
	// that a dial to a dead engine would otherwise spend is 45 seconds of the suite.
	t.Setenv("AF_ENGINE_PLAIN_HOLD", "1")
	ssmc := &fakePendingSSM{value: `["image/diffusion_models/z_image_turbo_bf16.safetensors"]`}
	g, e, st := pendingEngine(t, ssmc)
	sql, ok := st.(*store.SQL)
	if !ok {
		t.Fatalf("the test store is %T", st)
	}
	mgr := &manager{store: sql}
	g.mgr = mgr
	g.reg.signKey = []byte("0123456789abcdef0123456789abcdef")
	// A real membership: the gateway resolves it from the store on every request rather than
	// trusting the token, so a stub would not get past the door.
	tn := seedGitOAuthTenant(t, sql, "pending", "admin@pending.co.jp")
	who, err := sql.UpsertIdentity(t.Context(), "user@pending.co.jp", "user-pending-co-jp", "")
	if err != nil {
		t.Fatal(err)
	}
	mem, err := sql.EnsureMembership(t.Context(), who.ID, tn.ID, "member")
	if err != nil {
		t.Fatal(err)
	}
	tok := mintEngineSessionToken(g.reg.signKey, mem.ID, "sess-a", "image", time.Now().Add(time.Hour))

	ask := func(model string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/engine/image/v1/prompt", strings.NewReader(`{}`))
		r.SetPathValue("key", "image")
		r.SetPathValue("path", "prompt")
		r.Header.Set("Authorization", "Bearer "+tok)
		r.Header.Set("X-AF-Model", model)
		g.serve(rec, r)
		return rec
	}

	rec := ask("z-image-turbo")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d (%s), want 503 — the engine is up, the weights are not", rec.Code, rec.Body.String())
	}
	// 🔴 Both halves. `engine_waking` is the code the caller retries on — and it is ALSO what a
	// dial to an engine that never answers produces, so the code alone cannot tell the two
	// apart and a guard that was never called would pass on it. The sentence is what says the
	// weights are on their way, and it is the assertion that fails when the guard is unwired.
	if !strings.Contains(rec.Body.String(), "engine_waking") {
		t.Errorf("the refusal is not the retryable one: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "still being synced") {
		t.Errorf("this is the generic wake, not the sync: %s", rec.Body.String())
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("no Retry-After — the caller is told to come back with no idea when")
	}
	// 🔴 The positive control, through the same door: the model the instance started with is
	// answered, so what is being measured is the sync and not the gateway refusing everything.
	// It gets past this check and fails at the upstream dial instead (there is no engine at
	// 127.0.0.1:1), which is the ordinary wake.
	if body := ask("flux-2-klein").Body.String(); strings.Contains(body, "still being synced") {
		t.Errorf("the model the instance has was held as syncing: %s", body)
	}
	_ = e
}

// An empty list is the sidecar saying "everything is here", which is the state a synced
// instance sits in for the whole of its life. It must cost nothing and hold nothing.
func TestEngineGatewayLetsASyncedInstanceThrough(t *testing.T) {
	g, e, _ := pendingEngine(t, &fakePendingSSM{value: `[]`})
	for _, id := range []string{"z-image-turbo", "flux-2-klein"} {
		if aerr := pendingAsk(t, g, e, id); aerr != nil {
			t.Errorf("%s was held with nothing pending: %v", id, aerr.message)
		}
	}
}

// 🔴 UNKNOWN is not "still syncing". A deployment whose engine stack predates this has no such
// parameter at all, and every one of them has to behave exactly as it did before the check
// existed — including the one where SSM itself is unreachable.
func TestEngineGatewayDoesNotHoldWhatItCannotKnow(t *testing.T) {
	for _, c := range []struct {
		what string
		ssmc *fakePendingSSM
	}{
		{"no parameter (an older sidecar)", &fakePendingSSM{err: &ssmtypes.ParameterNotFound{}}},
		{"SSM unreachable", &fakePendingSSM{err: errors.New("dial tcp: i/o timeout")}},
		{"a value this CP cannot read", &fakePendingSSM{value: `{"keys":"not a list"}`}},
	} {
		g, e, _ := pendingEngine(t, c.ssmc)
		if aerr := pendingAsk(t, g, e, "z-image-turbo"); aerr != nil {
			t.Errorf("%s held a request: %v", c.what, aerr.message)
		}
	}
	// And a CP with no SSM at all (an inline engine table, a dev machine) never even builds one.
	if p := newEnginePending(nil, "/af-ws/engines", "image"); p != nil {
		t.Error("a CP with no SSM client built a pending reader")
	}
	if p := newEnginePending(&fakePendingSSM{}, "", "image"); p != nil {
		t.Error("a deployment with no parameter base built a pending reader")
	}
	// nil is the shape the gateway asks, so it has to answer rather than panic.
	if got := (*enginePending)(nil).missing(context.Background(), store.EngineModel{}); got != nil {
		t.Errorf("a nil reader answered %v", got)
	}
}

// 🔴 The refusal has to leave a trace on the SERVER, not only in a response body nobody
// keeps. Until this line existed, pendingGuard's 503 went out through writeAPIErr and
// nowhere else — and the caller that hits it in practice, `generate_image`, retries on every
// Retry-After for up to a quarter of an hour and returns only the eventual success. The
// window was therefore observable nowhere: PR #535 could only reproduce it by driving raw
// HTTP by hand.
func TestEnginePendingRefusalIsLoggedOncePerMinute(t *testing.T) {
	var buf bytes.Buffer
	defer captureLog(&buf)()
	ssmc := &fakePendingSSM{value: `["image/diffusion_models/z_image_turbo_bf16.safetensors"]`}
	g, e, _ := pendingEngine(t, ssmc)

	if aerr := pendingAsk(t, g, e, "z-image-turbo"); aerr == nil {
		t.Fatal("nothing was refused, so there is nothing to log about")
	}
	line := buf.String()
	if n := strings.Count(line, "\n"); n != 1 {
		t.Fatalf("the refusal wrote %d line(s), want exactly 1:\n%s", n, line)
	}
	// Role, model, how many files are left, and when to come back — the four things an
	// operator reading this needs to tell a sync apart from a broken engine.
	for _, want := range []string{"image", "z-image-turbo", "1 file(s)", "engine_waking",
		"Retry-After " + strconv.Itoa(engineWakingRetryAfter()) + "s"} {
		if !strings.Contains(line, want) {
			t.Errorf("the log line does not carry %q:\n%s", want, line)
		}
	}

	// 🔴 The retries arrive every few seconds for the whole ~270 s sync. They must not each
	// write a line.
	buf.Reset()
	for i := 0; i < 5; i++ {
		if aerr := pendingAsk(t, g, e, "z-image-turbo"); aerr == nil {
			t.Fatal("the retry was not refused")
		}
	}
	if buf.Len() != 0 {
		t.Errorf("a retry within the minute wrote to the log:\n%s", buf.String())
	}

	// A DIFFERENT model is a different fact, and is not suppressed by the first one's slot.
	// (Give z-image's second file to the sidecar too, so flux is the one still syncing.)
	ssmc.value = `["image/diffusion_models/flux-2-klein-4b.safetensors"]`
	e.pending = newEnginePending(ssmc, "/af-ws/engines", "image")
	buf.Reset()
	if aerr := pendingAsk(t, g, e, "flux-2-klein"); aerr == nil {
		t.Fatal("the other model was not refused")
	}
	if !strings.Contains(buf.String(), "flux-2-klein") {
		t.Errorf("a second model's refusal was suppressed:\n%s", buf.String())
	}
}

// 🔴 The positive control for the test above. "No second line" and "the second refusal never
// happened" look identical in a log, so defeat the throttle and show the second line appear
// with everything else unchanged.
func TestEnginePendingRefusalLogThrottleIsWhatSuppressesTheSecondLine(t *testing.T) {
	var buf bytes.Buffer
	defer captureLog(&buf)()
	prev := enginePendingLogEvery
	enginePendingLogEvery = 0
	defer func() { enginePendingLogEvery = prev }()

	g, e, _ := pendingEngine(t, &fakePendingSSM{
		value: `["image/diffusion_models/z_image_turbo_bf16.safetensors"]`})
	for i := 0; i < 2; i++ {
		if aerr := pendingAsk(t, g, e, "z-image-turbo"); aerr == nil {
			t.Fatal("the request was not refused")
		}
	}
	if n := strings.Count(buf.String(), "\n"); n != 2 {
		t.Fatalf("with the throttle defeated the log has %d line(s), want 2 — the suppression "+
			"in the test above was not the throttle:\n%s", n, buf.String())
	}
}

// The slot is keyed on (role, model), not on the model alone: two engines can be syncing a
// model of the same name, and silencing one because the other just logged would hide a role
// entirely. Asserted directly — one gateway test cannot hold two roles.
func TestEnginePendingLogSlotIsPerRoleAndModel(t *testing.T) {
	p := newEnginePending(&fakePendingSSM{}, "/af-ws/engines", "image")
	now := time.Now()
	if !p.claimLogSlot("image", "z-image-turbo", now) {
		t.Fatal("the first claim was refused")
	}
	if p.claimLogSlot("image", "z-image-turbo", now.Add(time.Second)) {
		t.Error("the same (role, model) claimed a slot again within the minute")
	}
	if !p.claimLogSlot("llm", "z-image-turbo", now.Add(time.Second)) {
		t.Error("another role was silenced by the first one's slot")
	}
	if !p.claimLogSlot("image", "flux-2-klein", now.Add(time.Second)) {
		t.Error("another model was silenced by the first one's slot")
	}
	if !p.claimLogSlot("image", "z-image-turbo", now.Add(enginePendingLogEvery)) {
		t.Error("the slot never reopened")
	}
	// The gateway asks a nil reader on every deployment that has no pending parameter.
	if (*enginePending)(nil).claimLogSlot("image", "z-image-turbo", now) {
		t.Error("a nil reader claimed a log slot")
	}
}

// What the guard must NOT do: hold a request because some OTHER model is being synced. The
// whole point is that an instance mid-sync keeps serving what it already has.
func TestEngineGatewayHoldsOnlyTheModelThatIsMissing(t *testing.T) {
	ssmc := &fakePendingSSM{value: `["image/diffusion_models/z_image_turbo_bf16.safetensors","image/vae/ae.safetensors"]`}
	g, e, _ := pendingEngine(t, ssmc)
	if aerr := pendingAsk(t, g, e, "flux-2-klein"); aerr != nil {
		t.Errorf("a model with nothing pending was held while another was syncing: %v", aerr.message)
	}
	// A request naming no model at all is not this check's business: an image request that
	// carries no header, and every chat request that names nothing, go to the engine as before.
	r := httptest.NewRequest("POST", "/engine/image/v1/prompt", strings.NewReader(`{}`))
	if aerr := g.pendingGuard(t.Context(), e, []byte(`{}`), r); aerr != nil {
		t.Errorf("a request naming no model was held: %v", aerr.message)
	}
	// Nor is an id the catalogue does not hold — that request's answer is the 404 above.
	if aerr := pendingAsk(t, g, e, "never-registered"); aerr != nil {
		t.Errorf("an unknown id was held rather than 404'd: %v", aerr.message)
	}
}
