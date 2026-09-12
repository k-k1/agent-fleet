package main

// engine_gateway.go — /engine/<key>/v1/* , the only way a Workspace reaches a self-hosted
// engine (ADR 0071 decisions 4 and 5).
//
// Why a gateway rather than letting the Workspace dial the engine directly:
//
//	(a) the route already exists — a Workspace reaches the CP today for git, MCP and memos —
//	    so nothing has to be added to no_proxy or to the tenant egress allowlist;
//	(b) SOMEBODY has to hold the first request while a GPU box comes up, and the CP is the
//	    only party in the path that outlives the request;
//	(c) the usage a member spends is in the response, and this is where it can be counted;
//	(d) llama-server has mutating endpoints and no authentication of its own. Reachability is
//	    its access control, and a Workspace is not on the list.
//
// ## Holding the first request (decision 5, as revised by the review's R1)
//
// A cold engine takes 527 seconds to answer (measured, from S3, with the image already
// pulled). opencode gives up at 300 — but NOT on elapsed time. Measured three ways against
// the real client: holding the connection with nothing sent is cut at 300.1 s; sending the
// 200 and the headers and then nothing is cut at 306.9 s; sending the headers and then one
// SSE comment line every 10 seconds is not cut at all, and a 400-second wait produced the
// answer on the FIRST attempt.
//
// So a streaming request is answered immediately with 200 and text/event-stream, and a
// comment line goes out every 10 seconds until the first byte of the real answer arrives —
// through the engine start AND through the prefill silence that follows it, since neither is
// distinguishable from the client's side. 300 seconds is the maximum SILENCE, not the
// maximum wait.
//
// 503 + Retry-After is therefore NOT the normal path. It is for a non-streaming request
// (which has nowhere to put a heartbeat) and for a start that genuinely failed. A 503 spends
// one of the client's finite retries, and how many it has is not something this side knows.
//
// ## The non-streaming path cannot hold for 900 seconds (measured 2026-09-07, ADR 0071 P1)
//
// The first real generate_image call against a stopped image engine came back as
// `POST /engine/image/v1/images/generations 503 59.998s` — the CP's own hold is 900 s, so
// something else cut it: the ingress ALB's `idle_timeout.timeout_seconds`, which
// 30-ingress.yaml sets to 60. A request that has sent no response byte for 60 seconds is
// closed under both ends, and the image engine needed 165 s to come up. The streaming path
// never noticed because its heartbeat is a byte every 10 s — written for opencode's 300 s
// ceiling, and it turns out to have been carrying the ALB too.
//
// So the hold on THIS path is bounded below the ingress's idle timeout and the answer is the
// 503 + Retry-After the design already called for, which the caller retries against its own
// (much longer) budget. Bounding it here rather than raising the ALB's timeout is what keeps
// this working behind a CloudFront, an nginx or a customer's own reverse proxy, none of which
// this process can interrogate.
//
// The engine keeps coming up across those retries: every attempt records demand and finds
// desired already at 1, so a retry costs one ECS read, not another start.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/envx"
	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// engineHeartbeatInterval is how often a comment line goes out while the answer is not yet
// flowing. 10 s against a measured 300 s tolerance: thirty times the margin, and an SSE
// comment is a handful of bytes, so there is nothing to buy by cutting it finer. A variable
// only so a test can measure the mechanism instead of the wall clock.
var engineHeartbeatInterval = 10 * time.Second

// engineHeartbeatLine is a comment in the SSE grammar — a line beginning with ':'. Every
// conforming client discards it, so it cannot be mistaken for content, and it is a byte on
// the wire, which is all the client's idle timer is watching for.
var engineHeartbeatLine = []byte(": af-engine waking\n\n")

// engineWakeTimeout bounds the whole hold. 900 s is the measured 527 s cold start plus room
// for the pull to be slower than the day it was measured; below it, the deployment's own
// hardware decides, so it is a knob (ADR 0071 decision 5).
func engineWakeTimeout() time.Duration {
	return time.Duration(runtime.EnvInt("AF_ENGINE_WAKE_TIMEOUT", 900)) * time.Second
}

// enginePlainHold bounds the hold on the NON-streaming path, and it exists because that path
// has no heartbeat and therefore no way to survive an ingress idle timeout (see the note at
// the top of this file). 45 s against the ALB's 60 leaves room for the 503 to be written and
// travel; a deployment whose ingress is more generous can raise it, and one behind a proxy
// that cuts at 30 s must lower it — this process cannot ask, so it is a knob with a default
// that matches the ingress this repository ships.
//
// It never RAISES the wait: the effective bound is the smaller of this and the wake timeout,
// so setting AF_ENGINE_WAKE_TIMEOUT low still means what it says.
const engineManagedPlainHoldSeconds = 45

// engineRemotePlainHoldSeconds is that same bound for a BORROWED row (ADR 0079 decision 6). The
// far deployment's own plain path holds for 45 s before answering `engine_waking`, and a local
// hold of 45 s expires first every time — so the answer a borrower gets would always be this
// side's, one round trip before the far side had said anything. A little more than the far
// default lets the far sentence through, and there is no ALB in the way of it: the number that
// has to stay under an ingress idle timeout is the one a MANAGED row uses, which is why this is
// a second default rather than a new one.
//
// 🔴 Comfort, not correctness. The far side's 45 s is AF_ENGINE_PLAIN_HOLD *over there*, a knob
// this process cannot read: a far operator who raised theirs puts the local expiry first again
// whatever is chosen here. What makes a borrowed cold start work is the engine_waking mapping
// below, not this number.
const engineRemotePlainHoldSeconds = 75

// enginePlainHoldFor is the hold for ONE row. Per row rather than per process because 75 s as a
// global default would raise managed rows above the ecs-ec2 ingress ALB's 60-second idle timeout
// (deploy/aws/ecs/cfn/30-ingress.yaml) and reintroduce exactly the 504 the 45 s exists to avoid.
func enginePlainHoldFor(eng *engineRuntimeState) time.Duration {
	if eng != nil && eng.def.remote() {
		return enginePlainHoldOf(engineRemotePlainHoldSeconds)
	}
	return enginePlainHoldOf(engineManagedPlainHoldSeconds)
}

// enginePlainHoldOf applies the operator's override and the wake-timeout cap to one default.
func enginePlainHoldOf(def int) time.Duration {
	hold := time.Duration(runtime.EnvInt("AF_ENGINE_PLAIN_HOLD", def)) * time.Second
	if wake := engineWakeTimeout(); hold > wake {
		return wake
	}
	return hold
}

// errEngineWaking says the hold ran out while the engine was on its way up, as opposed to a
// start that failed. The two are the same status to a client that cannot retry and opposite
// facts to one that can, which is the whole point of answering with a code it can read.
var errEngineWaking = errors.New("the engine is still coming up")

// engineReadyPoll is how often the engine's health endpoint is asked while it starts. Fast
// enough that the first answer is not held back by the poll itself, slow enough that a
// 527-second start is ~175 requests against a service that is mostly not there yet.
var engineReadyPoll = 3 * time.Second

// engineWakingRetryAfter is the Retry-After seconds that ride on an `engine_waking` refusal.
// A function rather than a constant because engineReadyPoll is swapped in tests, and one
// function rather than the expression at each site because the refusal LOG prints this
// number too — a header and a log line that disagree about when to come back is a bug the
// operator has no way to see.
func engineWakingRetryAfter() int { return int(engineReadyPoll.Seconds() * 2) }

// engineMaxRequestBody bounds what is buffered before being forwarded. The body has to be
// read in full to answer "is this streaming" and to be replayed upstream after the wait, and
// an unbounded read here is a way to spend the CP's memory from a Workspace. opencode's
// largest measured request is 70 KB, with 18.7k tokens of context in it.
const engineMaxRequestBody = 32 << 20

// engineClient is the upstream client. No timeout at all, and that is deliberate: a
// generation legitimately runs for many minutes, and the bound that matters (the wake
// timeout, and the caller hanging up) is carried on the context instead. Only the connection
// phase is bounded.
var engineClient = &http.Client{
	Transport: &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		ResponseHeaderTimeout: 0,
		IdleConnTimeout:       90 * time.Second,
		// The engine is one task behind one Cloud Map name; more than a handful of idle
		// connections to it is pointless.
		MaxIdleConnsPerHost: 4,
	},
}

type engineGateway struct {
	mgr *manager
	reg *engineRegistry
}

// registerEngineRoutes wires the gateway (called from buildMux).
//
// The GATEWAY is registered only when the deployment has engines: a 404 from an unregistered
// path and a 404 from a handler that found no engine read the same to a client, and not
// registering keeps the surface off a deployment that never asked for it.
//
// 🔴 The ADMIN routes are registered either way, and that is a deliberate exception. They used
// to sit behind the same return, so a deployment without 60-engines answered 404 to
// `GET /api/admin/engines` — the panel could not even say "no engines here", and ADR 0072
// decision 11's browse (which needs no engine, no token and no bucket) was unreachable exactly
// where it is most useful: deciding whether to stand the stack up at all. Every one of those
// handlers is nil-safe on the registry and answers "there is no such engine" by itself.
func registerEngineRoutes(mux *http.ServeMux, cfg config) {
	reg := newEngineRegistry(context.Background(), cfg.mgr)
	// The super-admin panel. Registered before the gateway's own guard because it is the one
	// part that has something to say when there is no engine at all.
	registerEngineAdminRoutes(mux, cfg, reg)
	if reg == nil {
		return
	}
	g := engineGateway{mgr: cfg.mgr, reg: reg}
	// Session-exempt, like /mcp and /git/*: the caller is a Workspace with a token, not a
	// browser with a login cookie, and the ingress passes both through untouched.
	exemptPrefix("/engine/", "/internal/engine/")
	mux.HandleFunc("POST /internal/engine/token", g.issueSessionToken)
	mux.HandleFunc("GET /internal/engine/catalog", g.catalog)
	mux.HandleFunc("/engine/{key}/v1/{path...}", g.serve)
}

// --- token issue --------------------------------------------------------------

// issueSessionToken exchanges the Workspace's issuing token for a session-scoped one. The
// session name is the caller's to state and is NOT verified against the Agent's session
// list: the token it produces is worth exactly the same as the issuing token that bought it
// (the same membership's engine access), so a wrong name costs a mislabelled usage row, not
// access to anything. Verifying it would mean a CP→Agent round trip on every session launch.
func (g engineGateway) issueSessionToken(w http.ResponseWriter, r *http.Request) {
	mv, aerr := g.issuerMembership(r)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	var req struct {
		Session string `json:"session"`
		Key     string `json:"key"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid body"})
		return
	}
	key := strings.TrimSpace(req.Key)
	if key == "" {
		key = "llm"
	}
	eng := g.reg.get(key)
	if eng == nil {
		writeAPIErr(w, &apiError{http.StatusNotFound, "engine_unknown", "no engine " + key})
		return
	}
	exp := time.Now().Add(engineSessionTokenTTL)
	tok := mintEngineSessionToken(g.reg.signKey, mv.MembershipID, strings.TrimSpace(req.Session), key, exp)
	writeJSON(w, http.StatusOK, map[string]any{
		"token":      tok,
		"expires_at": exp.UTC().Format(time.RFC3339),
		"base_url":   "/engine/" + key + "/v1",
		"models":     eng.modelIDs(r.Context()),
	})
}

// catalog is what the Agent asks so it can write the provider block into opencode's config:
// which engines exist, what provider id they answer to, and which model ids they offer. It
// never touches the engines themselves — the whole point is that the launch menu can be
// drawn while every engine is asleep.
func (g engineGateway) catalog(w http.ResponseWriter, r *http.Request) {
	if _, aerr := g.issuerMembership(r); aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	out := []map[string]any{}
	for _, e := range g.reg.list() {
		if e.mode(r.Context()) == engineModeOff {
			continue // an engine an admin switched off is not offered, rather than offered and refused
		}
		served, _ := e.servedModel()
		row := engineCatalogRowFor(e.def, e.catalog.enabled(r.Context()), served, e.negativeAlways(r.Context()))
		if row == nil {
			continue // nothing enabled: the same as switched off, from a Workspace's point of view
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"engines": out})
}

// engineCatalogRowFor is one engine as the Agent reads it, built from the CATALOGUE rather
// than from the stack (ADR 0072 decision 7).
//
// Two shapes ride together, and they are not redundant:
//
//   - `models` stays a flat list of ids, because that is what an Agent old enough to predate
//     this ADR reads to write opencode's provider block. Removing it would take the launch
//     menu away from every workspace image not yet rebuilt.
//   - `model_rows` carries the same models with the per-model window, description and declared
//     sizes. The window is per MODEL now, which is what removes ADR 0071's "two models with
//     different windows are two engines".
//
// The window is reported only when it was declared. A zero is not a small context, it is
// "nobody said" — and forwarding it has the Agent advertise a context of 0 to opencode, which
// switches auto-compaction off, i.e. exactly the state the field exists to fix.
//
// nil when nothing is enabled. An engine with an empty catalogue cannot serve anything, so
// offering it would put a model in a launch menu that answers 503 (decision 1).
func engineCatalogRowFor(d engineDef, models []store.EngineModel, warm, negativeAlways string) map[string]any {
	ids := []string{}
	rows := []map[string]any{}
	loras := []map[string]any{}
	for _, m := range models {
		if engineModelIsLora(m) {
			loras = append(loras, engineCatalogModelRow(m, warm))
			continue
		}
		ids = append(ids, m.ID)
		rows = append(rows, engineCatalogModelRow(m, warm))
	}
	if len(ids) == 0 {
		return nil
	}
	row := map[string]any{
		"key":        d.Key,
		"api":        d.api(),
		"provider":   d.Provider,
		"base_url":   "/engine/" + d.Key + "/v1",
		"models":     ids,
		"model_rows": rows,
	}
	if len(loras) > 0 {
		row["loras"] = loras
	}
	// What this deployment excludes from every image on this engine (ADR 0072 follow-up,
	// negative prompts). It rides with the catalogue rather than being fetched on its own
	// because it is read at exactly the same moment and changes just as rarely — and because an
	// Agent that failed to fetch it would compose a request WITHOUT the administrator's list and
	// have no way to know it had.
	if negativeAlways != "" {
		row["negative_always"] = negativeAlways
	}
	// The engine-wide window, kept for an Agent that has no per-model reader yet. It is the
	// window of whichever model the engine will start with, which for a one-model role is the
	// same number ADR 0071 published.
	if c, mo := engineStartWindow(models); c > 0 {
		row["context_tokens"] = c
		row["max_output_tokens"] = mo
	}
	return row
}

// engineStartWindow is the window of the model the engine is started with — the selected or
// default one, falling back to the first. It exists so the engine-wide fields above describe
// the model that will actually answer, rather than an arbitrary row.
func engineStartWindow(models []store.EngineModel) (int, int) {
	var first store.EngineModel
	found := false
	for _, m := range models {
		if engineModelIsLora(m) {
			continue
		}
		if !found {
			first, found = m, true
		}
		if m.Selected || m.Default {
			return m.ContextTokens, m.MaxOutputTokens
		}
	}
	if !found {
		return 0, 0
	}
	return first.ContextTokens, first.MaxOutputTokens
}

func (g engineGateway) issuerMembership(r *http.Request) (store.MembershipView, *apiError) {
	tok := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	mid, ok := verifyEngineIssueToken(g.reg.signKey, tok)
	if !ok {
		return store.MembershipView{}, &apiError{http.StatusUnauthorized, "unauthenticated", "invalid engine token"}
	}
	return g.liveMembership(r.Context(), mid)
}

// liveMembership resolves the membership from the store rather than from the token, so a
// member who was removed loses the engine at once rather than at the token's expiry.
func (g engineGateway) liveMembership(ctx context.Context, mid string) (store.MembershipView, *apiError) {
	if g.mgr == nil || g.mgr.store == nil {
		return store.MembershipView{}, internalErr(errors.New("no store"))
	}
	mv, ok, err := g.mgr.store.GetMembershipByID(ctx, mid)
	if err != nil {
		return store.MembershipView{}, internalErr(err)
	}
	if !ok {
		return store.MembershipView{}, &apiError{http.StatusUnauthorized, "unauthenticated", "membership not active"}
	}
	return mv, nil
}

// engineAuthFailure names, for the log only, why a token was refused. It never reaches the
// client.
func engineAuthFailure(signKey []byte, tok, key string) string {
	switch {
	case tok == "":
		return "no bearer token — the workspace's AF_ENGINE_TOKEN is unset, so {env:…} resolved to nothing"
	case !strings.HasPrefix(tok, "afe_"):
		if strings.HasPrefix(tok, "afei_") {
			return "the issuing token was presented as a session token"
		}
		return "not an engine token at all"
	}
	// Order matters, and getting it wrong is how a diagnostic misleads: verifying against the
	// epoch succeeds for EVERY unexpired token, so asking that first labels all of them
	// "expired". Ask about now first — if it passes, signature and clock are both fine and the
	// engine is the only thing left.
	if c, ok := verifyEngineSessionToken(signKey, tok, time.Now()); ok {
		if c.Key != key {
			return "a token for engine " + c.Key + " was used on " + key
		}
		return "the token verifies now — the caller was refused for something else"
	}
	if _, ok := verifyEngineSessionToken(signKey, tok, time.Unix(0, 0)); ok {
		return "the token expired"
	}
	return "bad signature (a token from another deployment, or the CP's signing master changed)"
}

// --- the proxy ----------------------------------------------------------------

func (g engineGateway) serve(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	eng := g.reg.get(key)
	if eng == nil {
		writeAPIErr(w, &apiError{http.StatusNotFound, "engine_unknown", "no engine " + key})
		return
	}
	tok := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	claims, ok := verifyEngineSessionToken(g.reg.signKey, tok, time.Now())
	if !ok || claims.Key != key {
		// One message for "not a token", "expired" and "a token for the other engine": the
		// caller can do nothing different about any of them, and saying which is a probing
		// aid. The OPERATOR does need to tell them apart, though — a 401 with no reason on
		// the server side cost a live debugging round when the managed route turned out not
		// to be carrying the token at all — so the distinction is logged, never returned.
		log.Printf("engine %s: refusing a request (%s)", key, engineAuthFailure(g.reg.signKey, tok, key))
		writeAPIErr(w, &apiError{http.StatusUnauthorized, "unauthenticated", "invalid engine session token"})
		return
	}
	mv, aerr := g.liveMembership(r.Context(), claims.MembershipID)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	if eng.mode(r.Context()) == engineModeOff {
		writeAPIErr(w, &apiError{http.StatusServiceUnavailable, "engine_off", "this engine is switched off"})
		return
	}
	// An engine whose catalogue is empty has nothing to answer with, and waking it would buy a
	// GPU box to run `sleep infinity` (ADR 0072 decision 1). `engine_unavailable` rather than
	// `engine_waking`: the sdcpp provider retries the second for a quarter of an hour, and no
	// amount of waiting adds a model.
	if !eng.catalog.hasModels(r.Context()) {
		writeAPIErr(w, &apiError{http.StatusServiceUnavailable, "engine_unavailable",
			"this engine has no enabled model — an administrator has to select one"})
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, engineMaxRequestBody))
	if err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "could not read the request body"})
		return
	}
	// A chat request naming a model the catalogue does not hold is refused HERE (ADR 0072
	// decision 7). The point is not validation for its own sake: with P1's router the engine
	// would happily load a file off disk by name, and "what this deployment offers" has to be
	// the catalogue rather than the contents of a directory. Refusing before the wake also
	// means a typo does not buy a GPU box.
	//
	// Only when a model is actually named, and only for chat: the image route deliberately
	// sends no `model` (sd-server holds one, chosen at startup), and a GET has no body.
	if eng.def.api() == engineAPIChat {
		if m := engineRequestModel(body); m != "" && !engineCatalogHolds(eng.catalog.enabled(r.Context()), m) {
			writeAPIErr(w, &apiError{http.StatusNotFound, "model_unknown",
				"no model " + m + " in this engine's catalogue"})
			return
		}
	}
	// The request IS the demand (decision 5). Recorded before anything can fail, so an
	// engine that is mid-start does not read as unwanted and get stopped by the controller
	// on the very tick somebody is waiting for it.
	eng.demand.record(r.Context(), 1)

	// 🔴 And the model may not be ON the instance yet. The sidecar releases the engine once the
	// START model is down and keeps fetching the rest, so there is a window — measured at ~270
	// seconds for 12 files / 48 GB — in which the engine is up, health passes, and a request
	// for one of the other models gets ComfyUI's bare `Value not in list: …` 400 (ADR 0072 P2
	// 欠落 7). Answered here as `engine_waking`, which is the same retryable refusal the
	// caller already handles for a cold start, because that is what it is.
	//
	// After the demand mark on purpose: somebody IS waiting for this engine, and a controller
	// that stopped it mid-sync would make the wait permanent.
	if aerr := g.pendingGuard(r.Context(), eng, body, r); aerr != nil {
		w.Header().Set("Retry-After", strconv.Itoa(engineWakingRetryAfter()))
		writeAPIErr(w, aerr)
		return
	}

	if engineWantsStream(body) {
		g.streamed(w, r, eng, claims, mv, askForStreamUsage(body))
		return
	}
	g.plain(w, r, eng, claims, mv, body)
}

// askForStreamUsage adds stream_options.include_usage to a streaming request that did not
// already ask for it.
//
// Measured against the real engine: with stream_options.include_usage the last chunk before
// [DONE] carries `usage`, and WITHOUT it there is no usage chunk at all — the stream simply
// ends. The AI SDK's openai-compatible provider does set it, but the accounting is the CP's
// job (ADR 0071 decision 9), and a feature that only works because some other codebase
// happens to send the right flag is a feature that breaks silently when it stops.
//
// Additive and safe for the caller: the extra chunk has an empty `choices`, which every
// client of this API already skips. A body that cannot be parsed is returned untouched —
// the engine is the one entitled to reject it, not this.
func askForStreamUsage(body []byte) []byte {
	var doc map[string]any
	if json.Unmarshal(body, &doc) != nil || doc == nil {
		return body
	}
	opts, _ := doc["stream_options"].(map[string]any)
	if opts == nil {
		opts = map[string]any{}
	}
	if _, already := opts["include_usage"]; already {
		return body
	}
	opts["include_usage"] = true
	doc["stream_options"] = opts
	out, err := json.Marshal(doc)
	if err != nil {
		return body
	}
	return out
}

// engineRequestModel is the model an OpenAI-compatible request named, or "" when it named none
// or the body is not JSON at all. Unparseable is "" rather than an error: the engine is the one
// entitled to reject a body this gateway could not read (the same rule askForStreamUsage
// follows), and refusing here would turn a bad request into a confusing 404.
func engineRequestModel(body []byte) string {
	var probe struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(body, &probe) != nil {
		return ""
	}
	return strings.TrimSpace(probe.Model)
}

// pendingGuard refuses — retryably — a request for a model whose files the instance is still
// downloading (ADR 0072 P2 欠落 7).
//
// It answers nil for everything it cannot establish: no pending parameter (a deployment whose
// engine stack predates it), a request that names no model, an id the catalogue does not hold
// (the 404 above is that request's answer), and a row with no declared files. Each of those is
// the behaviour the fleet had before this check, which is the only safe direction — a guard
// that guesses "still syncing" holds requests an engine could have served.
func (g engineGateway) pendingGuard(ctx context.Context, eng *engineRuntimeState, body []byte,
	r *http.Request) *apiError {

	if eng.pending == nil {
		return nil
	}
	id := engineRequestedModelID(eng, body, r)
	if id == "" {
		return nil
	}
	for _, m := range eng.catalog.enabled(ctx) {
		if m.ID != id || engineModelIsLora(m) {
			continue
		}
		missing := eng.pending.missing(ctx, m)
		if len(missing) == 0 {
			return nil
		}
		// 🔴 Say so in the CP's own log as well. Until now this refusal went out through
		// writeAPIErr and nothing else, so it left no trace on the server at all — and the
		// one caller that hits it in practice, generate_image, retries on every Retry-After
		// for up to a quarter of an hour and returns only the eventual success. The body was
		// therefore observable nowhere: reproducing it (PR #535) meant driving raw HTTP by
		// hand. Throttled per (role, model), because those retries arrive every few seconds.
		if eng.pending.claimLogSlot(eng.def.Key, id, time.Now()) {
			log.Printf("engines: %s: refusing a request for %s with engine_waking — %d file(s) still syncing onto the instance; Retry-After %ds",
				eng.def.Key, id, len(missing), engineWakingRetryAfter())
		}
		// The count, not the keys: the caller cannot act on a bucket path, and the operator
		// reads the sidecar's own log. What the sentence has to carry is that waiting is the
		// right thing to do, which "still being synced" says and a 400 never could.
		return &apiError{http.StatusServiceUnavailable, "engine_waking", fmt.Sprintf(
			"%s is still being synced onto this engine's instance (%d file(s) to go); retry",
			id, len(missing))}
	}
	return nil
}

// engineRequestedModelID is which catalogue row this request is for.
//
// Two sources because the two roles ask differently: a chat request names its model in the
// body, and an image request does not name one at all — ComfyUI's native API has no such field
// — so the Agent states it in `X-AF-Model`, which is the same header the usage accounting
// reads to know which model answered.
func engineRequestedModelID(eng *engineRuntimeState, body []byte, r *http.Request) string {
	if eng.def.api() == engineAPIChat {
		if m := engineRequestModel(body); m != "" {
			return m
		}
	}
	return strings.TrimSpace(r.Header.Get("X-AF-Model"))
}

// engineCatalogHolds reports whether one of the enabled models answers to this id. LoRAs are
// skipped: they are not something a chat request can be routed to.
func engineCatalogHolds(models []store.EngineModel, id string) bool {
	for _, m := range models {
		if !engineModelIsLora(m) && m.ID == id {
			return true
		}
	}
	return false
}

// engineWantsStream reports whether the caller asked for a streamed answer. Read from the
// body because that is where OpenAI-compatible clients put it; anything unparseable is
// treated as non-streaming, which is the conservative direction (a 503 the client can retry,
// rather than an event-stream a plain client cannot read).
func engineWantsStream(body []byte) bool {
	var probe struct {
		Stream *bool `json:"stream"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return false
	}
	return probe.Stream != nil && *probe.Stream
}

// upstreamStart is the outcome of "wake the engine, send the request, wait for its first
// byte" — everything that has to happen before there is anything to relay.
type upstreamStart struct {
	resp  *http.Response
	first []byte // the first chunk, already read off the body
	err   error
}

func (g engineGateway) streamed(w http.ResponseWriter, r *http.Request, eng *engineRuntimeState,
	claims engineSessionClaims, mv store.MembershipView, body []byte) {

	flusher, canFlush := w.(http.Flusher)
	if !canFlush {
		// No flusher means no heartbeat, and no heartbeat means the client cuts us off at
		// 300 seconds with no way to say why. Fall back to the honest path rather than
		// pretending to stream.
		g.plain(w, r, eng, claims, mv, body)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// nginx and every other buffering proxy will otherwise hold the heartbeat back, which
	// turns the whole mechanism off without changing anything visible here.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	started := time.Now()
	ctx, cancel := context.WithTimeout(r.Context(), engineWakeTimeout())
	defer cancel()
	ch := make(chan upstreamStart, 1)
	go func() { ch <- g.dial(ctx, eng, r, body, claims) }()

	tick := time.NewTicker(engineHeartbeatInterval)
	defer tick.Stop()
	var start upstreamStart
	for waiting := true; waiting; {
		select {
		case <-r.Context().Done():
			// The client hung up. Nothing to report to anyone.
			return
		case <-tick.C:
			if _, err := w.Write(engineHeartbeatLine); err != nil {
				return
			}
			flusher.Flush()
		case start = <-ch:
			waiting = false
		}
	}
	if start.err != nil {
		// The 200 is long gone, so the failure has to be said inside the stream. The shape is
		// the OpenAI error object every client of this API already knows how to surface, and
		// the text is written to be read by a person AND by the model that will see it.
		log.Printf("%s: waking for session %s failed after %s: %v",
			eng.logKey(), claims.Session, time.Since(started).Truncate(time.Second), start.err)
		writeEngineStreamError(w, flusher, start.err)
		return
	}
	defer start.resp.Body.Close()
	if start.resp.StatusCode >= 300 {
		rest, _ := io.ReadAll(io.LimitReader(start.resp.Body, 1<<16))
		writeEngineStreamError(w, flusher, fmt.Errorf("the engine answered %s: %s",
			start.resp.Status, strings.TrimSpace(string(start.first)+string(rest))))
		return
	}

	// From here the engine is talking, and the only job left is to get its bytes out
	// unchanged while reading the usage out of them on the way past.
	scan := &engineUsageScanner{}
	if len(start.first) > 0 {
		scan.feed(start.first)
		if _, err := w.Write(start.first); err != nil {
			return
		}
		flusher.Flush()
	}
	buf := make([]byte, 32<<10)
	for {
		n, rerr := start.resp.Body.Read(buf)
		if n > 0 {
			scan.feed(buf[:n])
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			flusher.Flush()
		}
		if rerr != nil {
			if !errors.Is(rerr, io.EOF) {
				log.Printf("%s: the stream ended early: %v", eng.logKey(), rerr)
			}
			break
		}
	}
	g.recordUsage(r.Context(), eng, claims, mv, scan.usage, time.Since(started), true, r.Header.Get("X-AF-Model"))
}

// writeEngineStreamError puts a failure into an already-open event stream, then closes it
// the way the protocol expects so the client stops waiting instead of timing out.
func writeEngineStreamError(w http.ResponseWriter, flusher http.Flusher, err error) {
	payload, _ := json.Marshal(map[string]any{
		"error": map[string]any{
			"type":    "engine_unavailable",
			"message": "the fleet's own inference engine did not come up in time: " + err.Error(),
		},
	})
	_, _ = w.Write([]byte("data: " + string(payload) + "\n\n"))
	_, _ = w.Write([]byte("data: [DONE]\n\n"))
	flusher.Flush()
}

// plain is the non-streaming path. There is nowhere to put a heartbeat, so this one really
// does have to answer 503 when the wait runs out — which is why the streaming path exists.
func (g engineGateway) plain(w http.ResponseWriter, r *http.Request, eng *engineRuntimeState,
	claims engineSessionClaims, mv store.MembershipView, body []byte) {

	started := time.Now()
	ctx, cancel := context.WithTimeout(r.Context(), enginePlainHoldFor(eng))
	defer cancel()
	start := g.dial(ctx, eng, r, body, claims)
	if start.err != nil {
		// Two different facts behind one status. "Still waking" is the ordinary answer on this
		// path and says come back; anything else is a failure the caller should surface.
		if errors.Is(start.err, errEngineWaking) {
			w.Header().Set("Retry-After", strconv.Itoa(int(engineReadyPoll.Seconds()*2)))
			writeAPIErr(w, &apiError{http.StatusServiceUnavailable, "engine_waking",
				"the fleet's own inference engine is starting; retry"})
			return
		}
		retry := int(engineReadyPoll.Seconds() * 10)
		w.Header().Set("Retry-After", strconv.Itoa(retry))
		writeAPIErr(w, &apiError{http.StatusServiceUnavailable, "engine_unavailable",
			"the fleet's own inference engine did not come up in time: " + start.err.Error()})
		return
	}
	defer start.resp.Body.Close()
	for k, vs := range start.resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(start.resp.StatusCode)
	rest, _ := io.ReadAll(start.resp.Body)
	full := append(append([]byte{}, start.first...), rest...)
	_, _ = w.Write(full)
	g.recordUsage(r.Context(), eng, claims, mv, parseEngineUsage(full), time.Since(started),
		start.resp.StatusCode < 300, r.Header.Get("X-AF-Model"))
}

// dial waits for the engine and sends the request, returning once its first byte is in hand.
// Everything slow lives in here so the caller can spend the wait writing heartbeats.
func (g engineGateway) dial(ctx context.Context, eng *engineRuntimeState, r *http.Request, body []byte,
	claims engineSessionClaims) upstreamStart {

	if err := g.ensureReady(ctx, eng); err != nil {
		return upstreamStart{err: err}
	}
	// The bearer. Two of the three lifecycles present one string fixed when the row was built;
	// a BORROWED row presents a far session token bought per (engine key, local session name),
	// which is what puts this deployment's session on the far side's usage rows (ADR 0079
	// decisions 4 and 8). Bought first, because the far gateway states its own base path in the
	// same answer and the URL below is built from it.
	bearer := eng.apiKey
	if eng.def.remote() {
		tok, err := eng.remote.sessionToken(ctx, claims.Session)
		if err != nil {
			return upstreamStart{err: engineRelayErr(ctx, eng, err)}
		}
		bearer = tok
	}
	target, err := engineUpstreamTarget(eng, r)
	if err != nil {
		return upstreamStart{err: err}
	}
	req, err := http.NewRequestWithContext(ctx, r.Method, target, bytes.NewReader(body))
	if err != nil {
		return upstreamStart{err: err}
	}
	// The caller's own Content-Type, not a hard-coded application/json: /v1/images/edits is
	// multipart/form-data and its boundary parameter lives in that header, so replacing it
	// makes the body unreadable at the far end — a failure that only shows up on the one
	// endpoint that is not JSON (ADR 0071 P1). JSON is the fallback for a caller that sent
	// none, which is what every OpenAI-compatible client does anyway.
	ctype := strings.TrimSpace(r.Header.Get("Content-Type"))
	if ctype == "" {
		ctype = "application/json"
	}
	req.Header.Set("Content-Type", ctype)
	if v := r.Header.Get("Accept"); v != "" {
		req.Header.Set("Accept", v)
	}
	// Which catalogue row this request is for. The engines in this deployment ignore it, but a
	// FAR gateway reads it twice — its usage accounting, and the pending guard that answers
	// `engine_waking` while a model is still being synced onto its instance (ADR 0072 P2 欠落 7).
	// Dropping it fails nothing and silently degrades both, which is the worst shape a bug has.
	if v := strings.TrimSpace(r.Header.Get("X-AF-Model")); v != "" {
		req.Header.Set("X-AF-Model", v)
	}
	// The Workspace's token never goes upstream: it authenticates to the CP, and the CP
	// presents the engine's own key. Two different secrets, so one leaking is not the other.
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := engineClient.Do(req)
	if err != nil {
		return upstreamStart{err: engineRelayErr(ctx, eng, err)}
	}
	// Read until there is at least one byte, so the caller's heartbeat covers the prefill
	// silence too — measured at 12.1 seconds to the first token for a 23k-token prompt on an
	// L4, and minutes on anything slower.
	first := make([]byte, 8<<10)
	n, rerr := resp.Body.Read(first)
	if n == 0 && rerr != nil && !errors.Is(rerr, io.EOF) {
		resp.Body.Close()
		return upstreamStart{err: engineRelayErr(ctx, eng, rerr)}
	}
	return upstreamStart{resp: resp, first: first[:n]}
}

// engineUpstreamTarget is where one request goes upstream.
//
// The path after /engine/<key>/v1/ is passed through verbatim, so /v1/chat/completions,
// /v1/models and llama.cpp's /v1/messages all work without this file knowing about them —
// PROVIDED the upstream engine's own API actually lives under /v1, which llama-server and
// sd-server's OpenAI-compatible faces do. ComfyUI's native API (ADR 0072 decision 4, phase
// P2) does not: /prompt, /history/<id> and /view live at the engine's root. The route's own
// literal `/v1/` stays fixed either way — only what gets prepended to the UPSTREAM path
// differs — so a comfy provider's request still arrives at /engine/<key>/v1/prompt and the
// Workspace side never needs to know this distinction exists.
//
// 🔴 A BORROWED row is the one case where that prefix must NOT be applied (ADR 0079 decision 4).
// What is upstream there is another fleet's gateway, not an engine, and it does this same rewrite
// itself when the request reaches it — applying it twice is how /v1/v1/chat/completions happens.
// Its middle segment is the far side's own `base_url`, READ from the token answer and never
// composed here: guessing `/engine/<key>/v1` would be this deployment asserting the other one's
// route layout. So an unknown one is an error rather than a guess — the far side has not yet been
// asked, or answered without it, and a URL invented from that reaches whatever happens to live
// there.
func engineUpstreamTarget(eng *engineRuntimeState, r *http.Request) (string, error) {
	base := strings.TrimRight(eng.def.URL, "/")
	middle := engineUpstreamPrefix(eng.def.Provider)
	if eng.def.remote() {
		far := strings.TrimRight(eng.remote.upstreamBase(), "/")
		if far == "" {
			return "", fmt.Errorf("%s has not said where its %s engine lives (no base_url on its token answer)",
				base, eng.def.Key)
		}
		middle = far + "/"
	}
	target := base + middle + r.PathValue("path")
	if q := r.URL.RawQuery; q != "" {
		target += "?" + q
	}
	return target, nil
}

// engineRelayErr maps the LOCAL hold running out while relaying to a borrowed engine onto
// `engine_waking` (ADR 0079 decision 6).
//
// 🔴 This, and not the longer hold, is what makes a borrowed cold start work. Without it the
// deadline fires inside engineClient.Do and `plain` reports `engine_unavailable` — which both
// image providers refuse to retry (workspace/agent/internal/imagegen/sdcpp.go's sdcppRetryable,
// shared with comfy), while they retry `engine_waking` for sixteen minutes. A GPU on its way up
// over there would be a permanent failure over here, reported as a start that failed.
//
// ⚠️ The caller hanging up is NOT that, and the two arrive as errors on the SAME context. Asking
// about the deadline specifically is already enough to tell them apart — a cancelled request never
// produces context.DeadlineExceeded — so the first branch is belt-and-braces against the obvious
// loosening of the second ("the context ended, so it must be waking"), which is the shape this
// mapping would take if somebody wrote it quickly. Measured with both mutations: the loosening
// alone stays correct, the loosening without this branch answers `engine_waking` to a caller who
// is no longer there.
func engineRelayErr(ctx context.Context, eng *engineRuntimeState, err error) error {
	if err == nil || eng == nil || !eng.def.remote() {
		return err
	}
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		return err
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%s did not answer for %s within this deployment's own hold: %w",
			strings.TrimRight(eng.def.URL, "/"), eng.def.Key, errEngineWaking)
	}
	return err
}

// logKey names one engine in a log line, for every lifecycle.
//
// 🔴 eng.ecs.logKey() is nil-safe but ANONYMOUS: a row with no ECS adapter — external, and every
// borrowed row — prints as the bare word "engine", so two roles borrowed from the same fleet
// cannot be told apart in the one place an operator learns that a borrowed role is misbehaving
// (ADR 0079 review R11).
func (e *engineRuntimeState) logKey() string {
	if e == nil {
		return "engine"
	}
	if e.ecs != nil {
		return e.ecs.logKey()
	}
	if k := strings.TrimSpace(e.def.Key); k != "" {
		return "engine " + k
	}
	return "engine"
}

// engineUpstreamPrefix is what dial prepends to the path after /engine/<key>/v1/ before
// forwarding upstream. "/v1/" for every OpenAI-compatible engine (llamacpp, sdcpp); "/" for
// comfy, whose native API has no /v1 of its own (ADR 0072 decision 4, phase P2).
func engineUpstreamPrefix(provider string) string {
	if provider == "comfy" {
		return "/"
	}
	return "/v1/"
}

// ensureReady starts the engine if it is stopped and waits until it answers its health
// endpoint. Returns as soon as it is up, and only errors when the wake timeout or the
// caller's own deadline runs out.
func (g engineGateway) ensureReady(ctx context.Context, eng *engineRuntimeState) error {
	// 🔴 A BORROWED row is ready by definition: there is nobody here to start it and nothing to
	// probe (ADR 0079 decision 5). engineHealthy would ask the row's URL plus /health, and what is
	// at that URL is another fleet's Control Plane, which publishes nothing there about an engine.
	//
	// 🔥 And on one shape the probe is not merely useless but expensive. A row whose URL already
	// contains the engine path — AF_COMFY_URL=https://<far>/engine/image/v1, the no-code-change
	// way an operator reaches for this first — puts the probe on the far GATEWAY as
	// /engine/image/v1/health, which records demand over there and buys a GPU box. Before
	// engineHealthy, therefore, and not inside it.
	if eng.def.remote() {
		return nil
	}
	waitStarted := time.Now()
	for {
		if engineHealthy(ctx, eng) {
			return nil
		}
		if err := eng.ensureStarted(ctx); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			// Wrapped, not replaced: the streaming path turns this into the text a person and a
			// model both read, and the non-streaming one turns it into a code a client retries
			// on. Both need to know it was the WAIT that ended, not the start that failed.
			return fmt.Errorf("gave up waiting for the engine after %s: %w",
				time.Since(waitStarted).Truncate(time.Second), errEngineWaking)
		case <-time.After(engineReadyPoll):
		}
	}
}

// ensureStarted moves the desired count to 1 when it is not there already. It reads the
// service through the adapter's short cache first, so a hundred requests arriving during a
// cold start make one UpdateService call between them rather than a hundred.
func (e *engineRuntimeState) ensureStarted(ctx context.Context) error {
	// Nothing here owns this engine (ADR 0076 decision 4), so there is nothing to wait for: a
	// LAN ComfyUI that is not answering does not come up because the fleet held the request.
	// `engine_waking` would be worse than useless — the provider retries it for sixteen minutes
	// (ADR 0071 decision 5), a budget meant for buying a box and pulling weights from S3 — so
	// this fails at once and names the URL and the path the operator has to go and look at.
	if e.ecs == nil {
		return fmt.Errorf("%s is not answering; this engine is externally managed and nothing here can start it",
			engineHealthURL(e.def))
	}
	view, err := e.ecs.view(ctx)
	if err != nil {
		return fmt.Errorf("could not read the engine service: %w", err)
	}
	if view.state == "none" {
		return errors.New("the engine's ECS service does not exist")
	}
	if view.desired >= 1 {
		return nil
	}
	// The third start path, and it needs the same gate as the controller's and the admin
	// panel's (ADR 0074 decision 4): a request arriving right after an instance class change
	// would otherwise buy the previous rung's box, or collide with the one still draining.
	//
	// Not an error. The caller is a wait loop that polls this, so "not yet" means it comes back
	// — and the request ends in the retryable `503 engine_waking` the provider already knows
	// how to answer, rather than in a failure that names a class change nobody asked about.
	if e.offers.startInFlight() {
		// The box for this start is bought and is joining the cluster (ADR 0077 decision 2). The
		// controller finishes it; asking the gate again here would begin the walk again on every
		// poll of this wait loop, which is every three seconds for as long as the request is
		// held — and each lap of it would buy a GPU.
		return nil
	}
	if ok, why := e.startGate(ctx); !ok {
		log.Printf("%s: a request is waiting, but the start is held back (%s)", e.ecs.logKey(), why)
		return nil
	}
	// Through the engine's own start, like the other two: the gate above has just chosen an
	// offer, and this is where the box is bought and — once it has registered — the desired
	// count written (ADR 0077 decisions 1 and 2). Moving the count alone here would ask for a
	// task on a cluster with no box in it.
	if err := e.startEngine(ctx); err != nil {
		if errors.Is(err, errEngineBoxRegistering) {
			// Half done and not an error: the box is bought and the desired count follows on the
			// controller's next tick. The caller is a wait loop, so "not yet" is an answer it
			// already knows how to hold — the same shape as the start gate's refusal above.
			log.Printf("%s: a request is waiting; its box is bought and registering", e.ecs.logKey())
			return nil
		}
		return fmt.Errorf("could not start the engine: %w", err)
	}
	log.Printf("%s: started on demand", e.ecs.logKey())
	return nil
}

// engineHealthPath is where this engine says whether it is up. "/health" is the default every
// table written before the image role relied on; the comfy role declares /system_stats.
func engineHealthPath(d engineDef) string {
	if p := strings.TrimSpace(d.Health); p != "" {
		return p
	}
	return "/health"
}

// engineHealthURL is that path against the engine's own base. Built in one place because the
// refusal an externally managed engine answers with names it (ADR 0076 decision 4), and a
// message pointing at a URL nothing actually dialled would send the operator to the wrong box.
func engineHealthURL(d engineDef) string {
	return strings.TrimRight(d.URL, "/") + engineHealthPath(d)
}

// engineHealthy asks the engine's health endpoint. A short timeout on purpose: while the
// service is at desired 0 the Cloud Map name does not resolve at all, and that lookup
// failing fast is the normal case, not an error worth logging.
func engineHealthy(ctx context.Context, eng *engineRuntimeState) bool {
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(c, http.MethodGet, engineHealthURL(eng.def), nil)
	if err != nil {
		return false
	}
	if eng.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+eng.apiKey)
	}
	resp, err := engineClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	// llama-server answers 503 with {"status":"loading model"} while the weights go into
	// VRAM — the 267 seconds that dominate a cold start. That is precisely NOT ready, and
	// treating any answer as ready would send the first request into it.
	return resp.StatusCode == http.StatusOK
}

// warmProbe is the controller's readiness gate for this engine.
//
// Without a WarmPath it is the same health check the gateway waits on, so the two cannot
// disagree about whether the engine is up. With one — the llm role, which is a llama.cpp ROUTER
// since ADR 0072 P1 — health and warmth stop being the same question: measured on b10853, a
// router holding NO models at all answers /health with {"status":"ok"}. Reading warmth off that
// would report an engine as ready through its whole 527-second cold start, tell the panel it is
// warm while nothing is loaded, and defuse the `unwarmed` rule that exists to stop a box which
// reached RUNNING and never came up.
func (e *engineRuntimeState) warmProbe(ctx context.Context, _ bool) bool {
	if strings.TrimSpace(e.def.WarmPath) == "" {
		return engineHealthy(ctx, e)
	}
	return len(engineLoadedModels(ctx, e)) > 0
}

// engineLoadedModels asks the router which models have weights in memory right now.
//
// ⚠️ This is the one question the CP does put to an engine, and it is legitimate under ADR 0053
// because it is not a DECLARATION: what may be loaded is the catalogue's answer, given while the
// box is asleep; what IS loaded is a fact only the running process holds. Nothing here decides
// what may be offered.
//
// "Loaded" is the only value that counts as warm. `sleeping` (the router's idle unload) and
// `loading` both mean the next request pays for weights again, which is exactly what warm is
// supposed to promise it will not.
func engineLoadedModels(ctx context.Context, eng *engineRuntimeState) []string {
	path := strings.TrimSpace(eng.def.WarmPath)
	if path == "" {
		return nil
	}
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(c, http.MethodGet, strings.TrimRight(eng.def.URL, "/")+path, nil)
	if err != nil {
		return nil
	}
	if eng.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+eng.apiKey)
	}
	resp, err := engineClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var doc struct {
		Data []struct {
			ID     string `json:"id"`
			Status struct {
				Value string `json:"value"`
			} `json:"status"`
		} `json:"data"`
	}
	// Capped: the router lists every model it knows with its full argument list and metadata,
	// and this runs on a 30-second timer.
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&doc); err != nil {
		return nil
	}
	var out []string
	for _, m := range doc.Data {
		if strings.EqualFold(m.Status.Value, "loaded") {
			out = append(out, m.ID)
		}
	}
	return out
}

// engineGatewayEnvName is the environment variable the session token is injected under, and
// the name opencode's config refers to as {env:…}. Named here because the CP hands it to the
// Agent in the token response's shape and the two must agree.
var engineGatewayEnvName = envx.Or("AF_ENGINE_TOKEN_ENV", "AF_ENGINE_TOKEN")
