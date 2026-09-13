package main

// engine_usage.go — counting what a member spent on the fleet's own engines (ADR 0071
// decision 9, ADR 0029's ledger).
//
// The ledger is a file inside the Workspace (workspace/agent/internal/usagex), which is
// where every other feature's rows live and where the Console reads its usage graph from.
// The gateway is the only party that sees an engine response, so it reads the `usage` object
// out and posts one row to the Agent — the same direction the CP already calls in for
// /work-items/fetch and /notifications.
//
// Posting it is best-effort and asynchronous: nothing here may block somebody's answer on a
// bookkeeping call.
//
// 🔴 A row that cannot be delivered is KEPT, in the CP's own store, and this is a change
// (ADR 0079 open question 7). It used to be logged and dropped, on the grounds that a queue
// whose loss on a CP restart would be invisible is no better than nothing — a durable table is
// not that queue. What forced it is lending: a borrowing membership is purpose-made and has no
// Workspace at all (ADR 0079 decision 3), so on a deployment that lends its engines EVERY row
// took the undeliverable branch and the operator who paid for the GPU was left with a bill and
// no name. The same branch was quietly losing an ordinary member's row whenever their workspace
// stopped between the answer and the bookkeeping.
//
// ⚠️ Kept is not delivered. Nothing re-posts these into a Workspace ledger later — see
// keepUndelivered — and this store is NOT a second ledger: ADR 0029's ledger stays the file in
// the Workspace, the usage graph still reads only that, and nothing here feeds it.
//
// No prompt and no completion text ever leaves this file — token counts and metadata only,
// which is the ledger's own non-negotiable.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// engineUsage is the OpenAI-compatible usage object, reduced to what the ledger keeps.
type engineUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	// Model is not part of `usage`; it is the response's own `model` field, carried here so
	// one struct is all that has to be threaded through.
	Model string `json:"-"`
}

func (u engineUsage) empty() bool { return u.PromptTokens == 0 && u.CompletionTokens == 0 }

// parseEngineUsage pulls the usage out of a whole non-streamed response body.
func parseEngineUsage(body []byte) engineUsage {
	var doc struct {
		Model string      `json:"model"`
		Usage engineUsage `json:"usage"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return engineUsage{}
	}
	doc.Usage.Model = doc.Model
	return doc.Usage
}

// engineUsageScanner reads the usage out of a stream as it goes past, without holding the
// stream up or keeping it in memory.
//
// It works because the AI SDK's openai-compatible provider — the one opencode uses, and the
// one ADR 0071 has the Workspace configure — sends `stream_options: {include_usage: true}`,
// so llama.cpp emits a final chunk carrying `usage`. When it does not, the row is written
// with measured="none" rather than with zeros: "the engine reported nothing" and "the engine
// reported nothing was spent" are different facts and the graph must not merge them.
type engineUsageScanner struct {
	buf   []byte
	usage engineUsage
}

// engineScannerMaxLine caps one buffered SSE line. A chunk is a few hundred bytes; anything
// past this is not a line this scanner understands, and holding it would let an engine grow
// the CP's memory one unterminated write at a time.
const engineScannerMaxLine = 1 << 20

func (s *engineUsageScanner) feed(p []byte) {
	s.buf = append(s.buf, p...)
	for {
		i := bytes.IndexByte(s.buf, '\n')
		if i < 0 {
			if len(s.buf) > engineScannerMaxLine {
				s.buf = s.buf[:0]
			}
			return
		}
		line := s.buf[:i]
		s.buf = s.buf[i+1:]
		data, ok := bytes.CutPrefix(bytes.TrimSpace(line), []byte("data:"))
		if !ok {
			continue
		}
		data = bytes.TrimSpace(data)
		if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
			continue
		}
		// Only the chunks that actually carry a usage object are parsed. A generation is
		// hundreds of chunks and all but the last one would be parsed for nothing.
		if !bytes.Contains(data, []byte(`"usage"`)) {
			// The model id rides on the first chunk, so it is taken from whichever chunk has
			// it — the last one to carry usage often does not repeat it.
			if s.usage.Model == "" && bytes.Contains(data, []byte(`"model"`)) {
				var doc struct {
					Model string `json:"model"`
				}
				if json.Unmarshal(data, &doc) == nil {
					s.usage.Model = doc.Model
				}
			}
			continue
		}
		var doc struct {
			Model string       `json:"model"`
			Usage *engineUsage `json:"usage"`
		}
		if json.Unmarshal(data, &doc) != nil || doc.Usage == nil {
			continue
		}
		model := s.usage.Model
		if doc.Model != "" {
			model = doc.Model
		}
		s.usage = *doc.Usage
		s.usage.Model = model
	}
}

// engineUsageRow is the wire the Agent's POST /engine/usage takes. It is deliberately the
// ledger's own vocabulary (feature / in / out / measured) rather than the gateway's, so the
// Agent side is a translation of names it already knows and not a second definition of what
// a usage row means.
type engineUsageRow struct {
	Feature  string `json:"feature"`  // "engine.llm"
	Provider string `json:"provider"` // "llamacpp"
	Session  string `json:"session"`  // the ledger's `ref`
	Model    string `json:"model"`
	In       int    `json:"in"`
	Out      int    `json:"out"`
	MS       int    `json:"ms"`
	OK       bool   `json:"ok"`
	Measured string `json:"measured"` // "exact" | "none"
}

// engineUsageFeature is the ledger's feature name for one call to a self-hosted engine. It
// joins the enumeration in ADR 0029 §2 next to tool.imagegen.
func engineUsageFeature(key string) string { return "engine." + key }

// engineUsageClient is separate from the upstream one: this call must not sit behind a
// generation's worth of idle connections, and it has a real timeout because nothing is
// waiting on it.
//
// ⚠️ newAgentTransport, not a bare client. A Service Connect alias is not DNS — the ECS agent
// writes it into /etc/hosts once, at CP task start — so a workspace created after the CP came
// up does not resolve, and only agent_dial.go's Cloud Map fallback finds it. Measured on the
// live deployment: a plain client lost the row with
// `dial tcp: lookup af-ws-… on 10.20.0.2:53: no such host`, which is precisely the failure
// that file exists to prevent, and it is silent — a dropped usage row looks like no usage.
var engineUsageClient = &http.Client{Timeout: 10 * time.Second, Transport: newAgentTransport()}

// engineUsageRowFor builds the row for one call, or reports that this engine's consumption is
// not the gateway's to count.
//
// Only the token-bearing engines are counted here. An image engine's response has no `usage`
// in it at all — what it spends is pixels, and the party that can count those is the Agent,
// which writes the tool.imagegen row as it stores the file (ADR 0071 decision 9, ADR 0069
// decision 9). Writing one from here as well would put an `engine.image` line with
// measured="none" next to every real one, in a graph whose categories are a frozen
// enumeration (ADR 0029 §2).
func engineUsageRowFor(def engineDef, claims engineSessionClaims, u engineUsage,
	took time.Duration, ok bool) (engineUsageRow, bool) {

	if def.api() != engineAPIChat {
		return engineUsageRow{}, false
	}
	row := engineUsageRow{
		Feature:  engineUsageFeature(def.Key),
		Provider: def.Provider,
		Session:  claims.Session,
		Model:    strings.TrimSpace(u.Model),
		In:       u.PromptTokens,
		Out:      u.CompletionTokens,
		MS:       int(took.Milliseconds()),
		OK:       ok,
		Measured: "exact",
	}
	if u.empty() {
		row.Measured = "none"
	}
	return row, true
}

// headerModel is X-AF-Model, the Workspace's own declaration of which model it asked for. Chat
// responses carry `model` in their own JSON (u.Model already has it); an image engine's answer
// never does — sd-server and ComfyUI both answer with pixels, not a model field — so a header is
// the only way this file can learn what an IMAGE request actually used. It only ever WIDENS what
// is known (u.Model wins when the response itself said something), never overrides it, and it
// changes nothing for the ledger row below (engineUsageRowFor still only counts chat engines) —
// it exists purely so warm-model tracking (ADR 0072 decision 7) is not blind to every image
// request the way it always has been.
func (g engineGateway) recordUsage(ctx context.Context, eng *engineRuntimeState,
	claims engineSessionClaims, mv store.MembershipView, u engineUsage, took time.Duration, ok bool, headerModel string) {

	if strings.TrimSpace(u.Model) == "" {
		u.Model = strings.TrimSpace(headerModel)
	}
	row, count := engineUsageRowFor(eng.def, claims, u, took, ok)
	// Before the ledger, and whether or not there is one to write to: this is where the CP finds
	// out which model the router actually answered as, and the swap count that comes out of it
	// is the visible price of --models-max 1 (ADR 0072 decision 3) — or, for comfy, simply which
	// checkpoint is now loaded (ADR 0072 decision 7's warm_model).
	eng.noteServed(u.Model, ok)
	if g.mgr == nil {
		return
	}
	// Detached from the request: the client's context is done the moment the answer is
	// delivered, and a row dropped because the reader hung up is a row nobody is billed for.
	bg := context.WithoutCancel(ctx)
	go func() {
		// Who the work was for (ADR 0079 open question 7). OUTSIDE the `count` gate on purpose:
		// the image role never produces a ledger row at all, so requests-by-membership is the
		// only count it can have — and an operator asking "whose box was that" must not be made
		// to read two tables and add them up, so the counted roles land here too.
		g.noteEngineMembershipHour(bg, eng.def.Key, mv, u, took, ok)
		if count {
			g.postUsage(bg, mv, row, eng.def.Key)
		}
	}()
}

// noteEngineMembershipHour accumulates one relayed request into (engine, membership, hour).
//
// This is the deployment's own bookkeeping and says nothing about price (ADR 0048 decision 2,
// ADR 0071 decision 9). It exists because engine_hourly answers "was the GPU up" and has no
// membership axis: until a deployment LENDS its engines there was always somewhere else to look
// for the owner — the member's own ledger inside their Workspace — and a borrowing membership
// has no Workspace (ADR 0079 decision 3).
func (g engineGateway) noteEngineMembershipHour(ctx context.Context, key string, mv store.MembershipView,
	u engineUsage, took time.Duration, ok bool) {

	if g.mgr == nil || g.mgr.store == nil || strings.TrimSpace(mv.MembershipID) == "" {
		return
	}
	// No Requests here: the gateway already counted the request where it was ADMITTED, which is
	// also where the box is bought (engine_gateway.go's demand mark). Counting it again would
	// double every successful call — and counting it ONLY here would miss every call that never
	// got an answer, which is what the live run of 2026-09-13 found.
	c := store.EngineMembershipHourCounters{
		MS:  int(took.Milliseconds()),
		In:  u.PromptTokens,
		Out: u.CompletionTokens,
	}
	if ok {
		c.OKRequests = 1
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	// UTC, like every other hourly bucket in this deployment: the client shifts to local time
	// (usage.go's usageHourFmt, engine_uptime.go).
	hour := time.Now().UTC().Format(usageHourFmt)
	if err := g.mgr.store.AddEngineMembershipHour(ctx, key, mv.MembershipID, mv.TenantID, hour, c); err != nil {
		log.Printf("engine usage: attributing %s to membership %s: %v", key, mv.MembershipID, err)
	}
}

// noteEngineMembershipRequest counts one ADMITTED request against (engine, membership, hour).
//
// Separate from noteEngineMembershipHour because the two happen at different moments and only
// one of them is guaranteed to happen at all: a request that never gets an answer — the far
// engine never came up, the upstream answered 5xx — still bought a GPU box for this membership,
// and that is precisely the spend a lending operator cannot otherwise attribute.
func (g engineGateway) noteEngineMembershipRequest(ctx context.Context, key string, mv store.MembershipView) {
	if g.mgr == nil || g.mgr.store == nil || strings.TrimSpace(mv.MembershipID) == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		hour := time.Now().UTC().Format(usageHourFmt)
		if err := g.mgr.store.AddEngineMembershipHour(ctx, key, mv.MembershipID, mv.TenantID, hour,
			store.EngineMembershipHourCounters{Requests: 1}); err != nil {
			log.Printf("engine usage: attributing a %s request to membership %s: %v", key, mv.MembershipID, err)
		}
	}()
}

// keepUndelivered stores a row the Agent never got, instead of dropping it (ADR 0079 open
// question 7).
//
// 🔴 It is NOT re-delivered when the Workspace comes back. A row that arrived days late would
// land in the ledger under the hour it was written rather than the hour it happened, and a
// ledger that quietly rewrites its own past is worse than one with a hole an operator can see.
// What this buys is that the hole is now readable.
func (g engineGateway) keepUndelivered(ctx context.Context, mv store.MembershipView, row engineUsageRow,
	engineKey, reason string) {

	if g.mgr == nil || g.mgr.store == nil || strings.TrimSpace(mv.MembershipID) == "" {
		return
	}
	err := g.mgr.store.AddEngineUsageUndelivered(ctx, store.EngineUsageRow{
		TS:           time.Now().UTC().Format(time.RFC3339),
		MembershipID: mv.MembershipID,
		TenantID:     mv.TenantID,
		EngineKey:    engineKey,
		Reason:       reason,
		Feature:      row.Feature,
		Provider:     row.Provider,
		Session:      row.Session,
		Model:        row.Model,
		In:           row.In,
		Out:          row.Out,
		MS:           row.MS,
		OK:           row.OK,
		Measured:     row.Measured,
	})
	if err != nil {
		log.Printf("engine usage: keeping the undelivered row for membership %s: %v", mv.MembershipID, err)
	}
}

func (g engineGateway) postUsage(ctx context.Context, mv store.MembershipView, row engineUsageRow, engineKey string) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	identityID, ok, err := g.mgr.store.IdentityIDForMembership(ctx, mv.MembershipID)
	if err != nil || !ok {
		log.Printf("engine usage: no identity for membership %s (%v)", mv.MembershipID, err)
		g.keepUndelivered(ctx, mv, row, engineKey, "no_identity")
		return
	}
	// 🔴 Asked BEFORE resolveByMembership, because that function CREATES a workspace for a
	// membership that has none (resolver.go's buildResolved -> createWorkspace) — it allocates a
	// port, mints an agent token and writes the row. Bookkeeping must not provision anything, and
	// on a lending deployment it would provision for every borrowing membership: the row would
	// then flip `has_workspace` on the issue-token screen, which exists to warn the operator that
	// a membership with a workspace looks like a PERSON's and must not be lent (decision 3). The
	// screen would be warning about a workspace this file had just created.
	if _, ok, err := g.mgr.store.GetWorkspaceByMembership(ctx, mv.MembershipID); err != nil || !ok {
		g.keepUndelivered(ctx, mv, row, engineKey, "no_workspace")
		return
	}
	res, aerr := g.mgr.resolveByMembership(ctx, identityID, mv.MembershipID)
	if aerr != nil || res == nil || res.rt == nil || res.rt.Endpoint() == "" {
		// The workspace is stopped. That is not an error: the only caller that can produce
		// engine usage is a session inside a running workspace, so this means it went away
		// between the answer and the bookkeeping.
		//
		// 🔴 It is also the ORDINARY case on a deployment that lends its engines: a borrowing
		// membership is purpose-made and never has a workspace (ADR 0079 decision 3), so every
		// borrowed conversation used to end here and leave the lender nothing at all. The row
		// is kept rather than dropped, which is open question 7's answer for the chat role.
		g.keepUndelivered(ctx, mv, row, engineKey, "no_workspace")
		return
	}
	body, _ := json.Marshal(row)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, res.rt.Endpoint()+"/engine/usage", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if t := res.rt.Token(); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
	resp, err := engineUsageClient.Do(req)
	if err != nil {
		log.Printf("engine usage: posting to the agent failed: %v", err)
		g.keepUndelivered(ctx, mv, row, engineKey, "post_failed")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		// An older Agent has no such route. Say so once per row rather than silently losing
		// the accounting — a CP newer than the image it launched must degrade visibly.
		log.Printf("engine usage: the agent answered %s (an older workspace image has no /engine/usage)", resp.Status)
		g.keepUndelivered(ctx, mv, row, engineKey, "post_failed")
	}
}

// --- telling the workspaces the catalogue moved (ADR 0072 decision 7) --------------

// engineCatalogPushConcurrency bounds the fan-out. A deployment can hold hundreds of
// workspaces and this is a background nicety — the Agent's own 10-minute TTL is what
// guarantees convergence — so it is deliberately slow and cheap rather than a burst of
// connections from the CP the moment an administrator presses a button.
const engineCatalogPushConcurrency = 8

// notifyEngineCatalogChanged tells every RUNNING workspace to re-read /internal/engine/catalog.
//
// The reverse direction already exists (postUsage above) and this uses the same transport for
// the same reason: a Service Connect alias is written into /etc/hosts once, at CP task start,
// so a plain client cannot resolve a workspace created afterwards — newAgentTransport's Cloud
// Map fallback is what finds it, and getting this wrong is silent.
//
// Best-effort by construction. A workspace that is stopped, unreachable or running an image
// older than the route simply catches up at its next TTL, so nothing here retries and nothing
// blocks the administrator's request.
// A var, like gitBackendServe: the push is a goroutine reaching real Workspaces, so the only
// way a test can state "this route tells running sessions" is to watch the call.
var notifyEngineCatalogChanged = func(ctx context.Context, mgr *manager, key string) {
	if mgr == nil || mgr.store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	tenants, err := mgr.store.ListTenants(ctx)
	if err != nil {
		log.Printf("engines: listing tenants for the catalogue push failed: %v", err)
		return
	}
	sem := make(chan struct{}, engineCatalogPushConcurrency)
	var wg sync.WaitGroup
	sent := 0
	for _, t := range tenants {
		wss, err := mgr.store.ListWorkspaces(ctx, t.ID)
		if err != nil {
			continue
		}
		for _, ws := range wss {
			// The DB's state column, not a live probe: asking the runtime would be one ECS or
			// Docker call per workspace before a notification nobody is waiting for. A stale
			// "running" costs one refused connection.
			if ws.State != "running" {
				continue
			}
			rt := mgr.runtimeFor(ws, "")
			if rt == nil || rt.Endpoint() == "" {
				continue
			}
			sent++
			wg.Add(1)
			go func(endpoint, token string) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				postEngineCatalogChanged(ctx, endpoint, token, key)
			}(rt.Endpoint(), rt.Token())
		}
	}
	wg.Wait()
	if sent > 0 {
		log.Printf("engines: catalogue change for %s pushed to %d workspace(s)", key, sent)
	}
}

func postEngineCatalogChanged(ctx context.Context, endpoint, token, key string) {
	body, _ := json.Marshal(map[string]string{"key": key})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/engine/catalog-changed", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := engineUsageClient.Do(req)
	if err != nil {
		return // stopped, or on its way there: the TTL covers it
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
}

// --- reading it back (ADR 0079 open question 7) ---------------------------------

// engineAttributionLimit bounds the undelivered list in one answer. A borrowing deployment
// writes one row per conversation and the window is fourteen days by default, so the honest
// failure is a truncated list with a total next to it rather than a multi-megabyte JSON the
// panel cannot draw.
const engineAttributionLimit = 500

// engineAttribution is what one engine's "whose work was this" question answers with.
//
// Two halves because the two roles are answerable to different depths (ADR 0079 decision 9):
// `memberships` covers BOTH roles, because requests and milliseconds are all an image answer
// can offer; `undelivered` is the chat role's own rows, kept whole, and is empty on a
// deployment nobody borrows from and whose members' workspaces were all running.
type engineAttribution struct {
	Engine      string                          `json:"engine"`
	From        string                          `json:"from"`
	To          string                          `json:"to"`
	Memberships []store.EngineMembershipHourRow `json:"memberships"`
	Undelivered []store.EngineUsageRow          `json:"undelivered"`
	// Truncated says the list hit engineAttributionLimit, so `undelivered` is the newest page
	// and not the whole window. Without it a capped list reads as a complete one.
	Truncated bool `json:"truncated,omitempty"`
}

// attribution (GET /api/admin/engines/{key}/attribution) answers "whose work was this engine
// doing", which is the question a deployment that LENDS its engines could not answer at all
// before ADR 0079 open question 7: engine_hourly says a GPU was up and has no membership axis,
// and the per-call rows went to a Workspace the borrowing membership does not have.
//
// super_admin, like the uptime route next door and for the same reason: it is a statement about
// the whole deployment's hardware, across every tenant.
func (a engineAdminAPI) attribution(w http.ResponseWriter, r *http.Request, _ store.Identity) {
	key := strings.TrimSpace(r.PathValue("key"))
	if a.reg.get(key) == nil {
		writeAPIErr(w, &apiError{http.StatusNotFound, errCodeEngineUnknown, "no engine " + key})
		return
	}
	if a.mgr == nil || a.mgr.store == nil {
		writeAPIErr(w, internalErr(errors.New("no store")))
		return
	}
	fromDay, toDay, fromHour, toHour, aerr := usageHourWindow(r, time.Now().UTC())
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	hours, err := a.mgr.store.ListEngineMembershipHourly(r.Context(), key, fromHour, toHour)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	// The undelivered rows are timestamped to the second, not bucketed to the hour, so the
	// window is widened to whole days at both ends rather than reusing the hour strings.
	rows, err := a.mgr.store.ListEngineUsageUndelivered(r.Context(), "", key,
		fromDay+"T00:00:00Z", toDay+"T23:59:59Z", engineAttributionLimit+1)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	out := engineAttribution{Engine: key, From: fromDay, To: toDay, Memberships: hours, Undelivered: rows}
	if len(rows) > engineAttributionLimit {
		out.Undelivered, out.Truncated = rows[:engineAttributionLimit], true
	}
	writeJSON(w, http.StatusOK, out)
}
