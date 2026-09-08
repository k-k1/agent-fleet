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
func enginePlainHold() time.Duration {
	hold := time.Duration(runtime.EnvInt("AF_ENGINE_PLAIN_HOLD", 45)) * time.Second
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

// registerEngineRoutes wires the gateway (called from buildMux). Nothing is registered when
// the deployment has no engines: a 404 from an unregistered path and a 404 from a handler
// that found no engine read the same to a client, and not registering keeps the surface off
// a deployment that never asked for it.
func registerEngineRoutes(mux *http.ServeMux, cfg config) {
	reg := newEngineRegistry(context.Background(), cfg.mgr)
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
	// The super-admin toggle. Registered here rather than in its own register* because it
	// needs the same registry, and building a second one would mean a second SSM read and two
	// answers to "what mode is this engine in".
	registerEngineAdminRoutes(mux, cfg, reg)
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
		row := engineCatalogRowFor(e.def, e.catalog.enabled(r.Context()))
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
func engineCatalogRowFor(d engineDef, models []store.EngineModel) map[string]any {
	ids := []string{}
	rows := []map[string]any{}
	loras := []map[string]any{}
	for _, m := range models {
		if engineModelIsLora(m) {
			loras = append(loras, engineCatalogModelRow(m))
			continue
		}
		ids = append(ids, m.ID)
		rows = append(rows, engineCatalogModelRow(m))
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
	go func() { ch <- g.dial(ctx, eng, r, body) }()

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
			eng.ecs.logKey(), claims.Session, time.Since(started).Truncate(time.Second), start.err)
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
				log.Printf("%s: the stream ended early: %v", eng.ecs.logKey(), rerr)
			}
			break
		}
	}
	g.recordUsage(r.Context(), eng, claims, mv, scan.usage, time.Since(started), true)
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
	ctx, cancel := context.WithTimeout(r.Context(), enginePlainHold())
	defer cancel()
	start := g.dial(ctx, eng, r, body)
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
		start.resp.StatusCode < 300)
}

// dial waits for the engine and sends the request, returning once its first byte is in hand.
// Everything slow lives in here so the caller can spend the wait writing heartbeats.
func (g engineGateway) dial(ctx context.Context, eng *engineRuntimeState, r *http.Request, body []byte) upstreamStart {
	if err := g.ensureReady(ctx, eng); err != nil {
		return upstreamStart{err: err}
	}
	// The path after /engine/<key> is passed through verbatim, so /v1/chat/completions,
	// /v1/models and llama.cpp's /v1/messages all work without this file knowing about them.
	target := strings.TrimRight(eng.def.URL, "/") + "/v1/" + r.PathValue("path")
	if q := r.URL.RawQuery; q != "" {
		target += "?" + q
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
	// The Workspace's token never goes upstream: it authenticates to the CP, and the CP
	// presents the engine's own key. Two different secrets, so one leaking is not the other.
	if eng.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+eng.apiKey)
	}
	resp, err := engineClient.Do(req)
	if err != nil {
		return upstreamStart{err: err}
	}
	// Read until there is at least one byte, so the caller's heartbeat covers the prefill
	// silence too — measured at 12.1 seconds to the first token for a 23k-token prompt on an
	// L4, and minutes on anything slower.
	first := make([]byte, 8<<10)
	n, rerr := resp.Body.Read(first)
	if n == 0 && rerr != nil && !errors.Is(rerr, io.EOF) {
		resp.Body.Close()
		return upstreamStart{err: rerr}
	}
	return upstreamStart{resp: resp, first: first[:n]}
}

// ensureReady starts the engine if it is stopped and waits until it answers its health
// endpoint. Returns as soon as it is up, and only errors when the wake timeout or the
// caller's own deadline runs out.
func (g engineGateway) ensureReady(ctx context.Context, eng *engineRuntimeState) error {
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
	if err := e.ecs.setEnabled(ctx, true); err != nil {
		return fmt.Errorf("could not start the engine: %w", err)
	}
	log.Printf("%s: started on demand", e.ecs.logKey())
	return nil
}

// engineHealthy asks the engine's health endpoint. A short timeout on purpose: while the
// service is at desired 0 the Cloud Map name does not resolve at all, and that lookup
// failing fast is the normal case, not an error worth logging.
func engineHealthy(ctx context.Context, eng *engineRuntimeState) bool {
	path := eng.def.Health
	if path == "" {
		path = "/health"
	}
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(c, http.MethodGet, strings.TrimRight(eng.def.URL, "/")+path, nil)
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

// warmProbe is the controller's readiness gate for this engine: the same health check, so
// the controller and the gateway cannot disagree about whether the engine is up.
func (e *engineRuntimeState) warmProbe(ctx context.Context, _ bool) bool {
	return engineHealthy(ctx, e)
}

// engineGatewayEnvName is the environment variable the session token is injected under, and
// the name opencode's config refers to as {env:…}. Named here because the CP hands it to the
// Agent in the token response's shape and the two must agree.
var engineGatewayEnvName = envx.Or("AF_ENGINE_TOKEN_ENV", "AF_ENGINE_TOKEN")
