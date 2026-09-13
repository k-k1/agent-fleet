package main

// engine_usage_attribution_test.go — whose work the engine was doing (ADR 0079 open question 7).
//
// What these pin is the one thing a deployment that LENDS its engines could not answer at all:
// engine_hourly says a GPU box was up and has no membership axis, and the per-call rows were
// posted to a Workspace that a borrowing membership does not have.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// waitForAttribution polls because recordUsage does its bookkeeping in a detached goroutine —
// on purpose, so that nobody's answer waits on it.
func waitForAttribution(t *testing.T, st store.Store, key string, want func([]store.EngineMembershipHourRow) bool) []store.EngineMembershipHourRow {
	t.Helper()
	day := time.Now().UTC().Format(usageDayFmt)
	deadline := time.Now().Add(3 * time.Second)
	var rows []store.EngineMembershipHourRow
	for time.Now().Before(deadline) {
		var err error
		rows, err = st.ListEngineMembershipHourly(context.Background(), key, day+"T00", day+"T23")
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if want(rows) {
			return rows
		}
		time.Sleep(20 * time.Millisecond)
	}
	return rows
}

// The image role is counted, and that is the whole point of the hourly table.
//
// 🔴 The pairing is deliberate. An image answer carries no `usage` object, so engineUsageRowFor
// refuses it and recordUsage used to return before touching anything durable — which on a
// lending deployment meant a GPU bought for a picture with no record of who for. The chat row
// asserted next to it is what keeps this honest: a bucket that counted EVERYTHING regardless of
// role would also pass the image half alone, and this way the tokens have to land too.
func TestEngineMembershipHourCountsBothRoles(t *testing.T) {
	ctx := context.Background()
	st, mgr, tn, mem, _ := usageHourFixture(t)
	g := engineGateway{mgr: mgr}
	mv := store.MembershipView{MembershipID: mem.ID, TenantID: tn.ID}
	// The chat half posts its ledger row to the member's Agent, so this member needs a resolvable
	// one. The resolver's cache is the injection point: without it buildResolved reaches for a
	// container factory this fixture has none of.
	agent := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer agent.Close()
	mgr.rts[mem.ID] = cachedRT{rt: stubRuntime{endpoint: agent.URL, token: "tok"}}

	img := &engineRuntimeState{def: engineDef{Key: "image", API: engineAPIImages, Provider: "comfy"}}
	g.recordUsage(ctx, img, engineSessionClaims{Key: "image"}, mv, engineUsage{}, 1200*time.Millisecond, true, "sdxl")

	rows := waitForAttribution(t, st, "image", func(r []store.EngineMembershipHourRow) bool { return len(r) == 1 })
	if len(rows) != 1 {
		t.Fatalf("the image role left no attribution row: %+v", rows)
	}
	// `requests` is NOT asserted here: it is counted where the request is ADMITTED, in the
	// gateway, because a request that never reaches an answer still bought the box —
	// TestEngineRequestIsAttributedEvenWhenTheEngineNeverAnswers owns that half. What this
	// function is responsible for is the outcome, and for the image role it is all there is.
	if rows[0].OKRequests != 1 || rows[0].MS != 1200 {
		t.Errorf("image bucket = %+v, want one ok outcome of 1200 ms", rows[0].EngineMembershipHourCounters)
	}
	if rows[0].In != 0 || rows[0].Out != 0 {
		t.Errorf("image bucket carried tokens: %+v — an image answer has no usage object", rows[0])
	}
	if rows[0].MembershipID != mem.ID || rows[0].UserKey == "" {
		t.Errorf("row = %+v, want the membership and its joined label", rows[0])
	}

	llm := &engineRuntimeState{def: engineDef{Key: "llm", API: engineAPIChat, Provider: "llamacpp"}}
	g.recordUsage(ctx, llm, engineSessionClaims{Key: "llm", Session: "s-1"}, mv,
		engineUsage{PromptTokens: 24, CompletionTokens: 12, Model: "qwen3"}, 2*time.Second, true, "")

	rows = waitForAttribution(t, st, "llm", func(r []store.EngineMembershipHourRow) bool {
		return len(r) == 1 && r[0].In == 24
	})
	if len(rows) != 1 || rows[0].In != 24 || rows[0].Out != 12 || rows[0].OKRequests != 1 {
		t.Fatalf("llm bucket = %+v, want the tokens as well as the outcome", rows)
	}

	// Same membership, same hour, a second call: buckets accumulate rather than replace.
	g.recordUsage(ctx, llm, engineSessionClaims{Key: "llm", Session: "s-1"}, mv,
		engineUsage{PromptTokens: 6, CompletionTokens: 3, Model: "qwen3"}, time.Second, false, "")
	rows = waitForAttribution(t, st, "llm", func(r []store.EngineMembershipHourRow) bool {
		return len(r) == 1 && r[0].In == 30
	})
	if len(rows) != 1 || rows[0].OKRequests != 1 || rows[0].In != 30 || rows[0].MS != 3000 {
		t.Fatalf("after a second (failed) call = %+v, want 1 ok, 30 in, 3000 ms", rows)
	}
}

// The chat role's row survives having nowhere to go, which is open question 7's answer.
//
// 🔴 The pairing again: "kept when there is no workspace" is asserted against a membership whose
// workspace IS resolvable, where the row goes to the Agent instead. Asserting only the first
// half would pass for an implementation that kept every row twice — which would double-count
// every ordinary member on every deployment.
func TestEngineUsageRowIsKeptWhenThereIsNoWorkspace(t *testing.T) {
	ctx := context.Background()
	st, mgr, tn, mem, _ := usageHourFixture(t)
	g := engineGateway{mgr: mgr}

	// A second member of the same tenant, with no workspace at all — the shape of the
	// purpose-made membership a borrowing deployment is told to use (ADR 0079 decision 3).
	id, err := st.UpsertIdentity(ctx, "lender@acme.co.jp", "lender-acme-co-jp", "")
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	lender, err := st.EnsureMembership(ctx, id.ID, tn.ID, "member")
	if err != nil {
		t.Fatalf("membership: %v", err)
	}
	row := engineUsageRow{
		Feature: "engine.llm", Provider: "llamacpp", Session: "borrower-session-7",
		Model: "qwen3", In: 24, Out: 12, MS: 900, OK: true, Measured: "exact",
	}
	g.postUsage(ctx, store.MembershipView{MembershipID: lender.ID, TenantID: tn.ID}, row, "llm")

	day := time.Now().UTC().Format(usageDayFmt)
	kept, err := st.ListEngineUsageUndelivered(ctx, "", "", day+"T00:00:00Z", day+"T23:59:59Z", 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(kept) != 1 {
		t.Fatalf("kept = %d rows, want the undeliverable one: %+v", len(kept), kept)
	}
	if kept[0].Reason != "no_workspace" {
		t.Errorf("reason = %q, want no_workspace", kept[0].Reason)
	}
	// The borrower's session name is why the row is worth keeping at all: it is the only thing
	// on a lending deployment that says which borrower the GPU was for (ADR 0079 decision 8).
	if kept[0].Session != "borrower-session-7" || kept[0].In != 24 || kept[0].Out != 12 {
		t.Errorf("kept row = %+v, want the gateway's row intact", kept[0])
	}
	if !kept[0].OK || kept[0].Measured != "exact" {
		t.Errorf("kept row lost its outcome: %+v", kept[0])
	}
	// 🔴 And the bookkeeping provisioned nothing. resolveByMembership CREATES a workspace for a
	// membership that has none, so asking it would have written a workspace row for the very
	// membership the issue-token screen warns must not have one (ADR 0079 decision 3).
	if _, ok, err := st.GetWorkspaceByMembership(ctx, lender.ID); err != nil || ok {
		t.Errorf("the usage post-back created a workspace for the borrowing membership (ok=%v, err=%v)", ok, err)
	}

	// The other half: a member whose workspace answers. The row goes there and is NOT kept.
	var got engineUsageRow
	seen := make(chan struct{}, 1)
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		select {
		case seen <- struct{}{}:
		default:
		}
	}))
	defer agent.Close()
	mgr.rts[mem.ID] = cachedRT{rt: stubRuntime{endpoint: agent.URL, token: "tok"}}
	g.postUsage(ctx, store.MembershipView{MembershipID: mem.ID, TenantID: tn.ID}, row, "llm")
	select {
	case <-seen:
	case <-time.After(3 * time.Second):
		t.Fatal("the agent never received the row")
	}
	if got.Session != "borrower-session-7" {
		t.Errorf("the agent got %+v", got)
	}
	kept, err = st.ListEngineUsageUndelivered(ctx, "", "", day+"T00:00:00Z", day+"T23:59:59Z", 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(kept) != 1 {
		t.Errorf("kept = %d rows, want still just the undeliverable one — a delivered row must not also be stored", len(kept))
	}
}

// 🔴 A request that never gets an answer is still attributed, because it still bought the box.
//
// This closes the hole the LIVE RUN of 2026-09-13 found and the unit tests above did not: they
// call recordUsage directly, and three paths never reach it — a `dial` that fails, an upstream
// 3xx-or-worse on the streamed path, and the plain path's refusal. Measured on the live lending deployment: a
// borrowed request bought a g6.xlarge two seconds after it arrived, the model failed to load
// 3m53s later, and the attribution tables were empty. That is the most expensive case there is
// and the one a lending operator most needs a name for.
//
// The pairing is the same engine ANSWERING, which is what keeps the split honest: counting the
// request at admission and again in recordUsage would double every successful call.
func TestEngineRequestIsAttributedEvenWhenTheEngineNeverAnswers(t *testing.T) {
	ctx := context.Background()
	st := testSettingsStore(t)
	up := httptest.NewServer(comfyStub())
	defer up.Close()

	e := newTestExternalEngine(t, up.URL, st)
	reg := &engineRegistry{
		byKey:   map[string]*engineRuntimeState{"image": e},
		signKey: engineSignKey([]byte(strings.Repeat("k", 32))),
	}
	mgr := &manager{store: st}
	a := engineAdminAPI{memberAuth{mgr}, reg, st}
	g := engineGateway{mgr: mgr, reg: reg}

	// serve refuses before the demand mark when the catalogue is empty, so the row needs a model
	// — and that refusal is correct: a request that buys nothing is attributed to nobody.
	if code, out := adminModel(t, a, "POST", "image", "",
		`{"id":"sdxl-base-1.0","kind":"checkpoint","base_model":"sdxl",
		  "files":[{"s3Key":"checkpoints/sd_xl_base_1.0.safetensors"}]}`); code != http.StatusOK {
		t.Fatalf("POST model = %d (%v)", code, out)
	}
	if code, out := adminModel(t, a, "PUT", "image", "sdxl-base-1.0",
		`{"enabled":true,"selected":true}`); code != http.StatusOK {
		t.Fatalf("PUT model = %d (%v)", code, out)
	}
	e.catalog.invalidate()

	tn, err := st.CreateTenant(ctx, "acme", "acme")
	if err != nil {
		t.Fatalf("tenant: %v", err)
	}
	ident, err := st.UpsertIdentity(ctx, "u@example.invalid", "u-example-invalid", "")
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	mem, err := st.EnsureMembership(ctx, ident.ID, tn.ID, "member")
	if err != nil {
		t.Fatalf("membership: %v", err)
	}
	tok := mintEngineSessionToken(reg.signKey, mem.ID, "s-1", "image", time.Now().Add(time.Hour))
	call := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/engine/image/v1/prompt", strings.NewReader(`{}`))
		r.Header.Set("Authorization", "Bearer "+tok)
		r.SetPathValue("key", "image")
		r.SetPathValue("path", "prompt")
		g.serve(rec, r)
		return rec
	}

	// The half that works: one request, one success.
	if rec := call(); rec.Code != http.StatusOK {
		t.Fatalf("serve against a live engine = %d: %s", rec.Code, rec.Body.String())
	}
	rows := waitForAttribution(t, st, "image", func(r []store.EngineMembershipHourRow) bool {
		return len(r) == 1 && r[0].Requests == 1 && r[0].OKRequests == 1
	})
	if len(rows) != 1 || rows[0].Requests != 1 || rows[0].OKRequests != 1 {
		t.Fatalf("after one good call = %+v, want exactly 1 request and 1 success (a double count means the split is wrong)", rows)
	}

	// And the half the live run found: the engine is gone, so nothing reaches recordUsage.
	up.Close()
	if rec := call(); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("serve against a dead engine = %d: %s", rec.Code, rec.Body.String())
	}
	rows = waitForAttribution(t, st, "image", func(r []store.EngineMembershipHourRow) bool {
		return len(r) == 1 && r[0].Requests == 2
	})
	if len(rows) != 1 || rows[0].Requests != 2 {
		t.Fatalf("after a failed call = %+v, want the request counted — it still bought the box", rows)
	}
	if rows[0].OKRequests != 1 {
		t.Errorf("ok_requests = %d, want still 1: requests-ok_requests is the failure count", rows[0].OKRequests)
	}
}
