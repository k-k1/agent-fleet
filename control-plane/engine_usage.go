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
// Posting it is best-effort and asynchronous. A row that cannot be delivered is logged and
// dropped: the alternative is either blocking somebody's answer on a bookkeeping call, or
// keeping a queue whose loss on a CP restart would be invisible anyway.
//
// No prompt and no completion text ever leaves this file — token counts and metadata only,
// which is the ledger's own non-negotiable.

import (
	"bytes"
	"context"
	"encoding/json"
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

func (g engineGateway) recordUsage(ctx context.Context, eng *engineRuntimeState,
	claims engineSessionClaims, mv store.MembershipView, u engineUsage, took time.Duration, ok bool) {

	row, count := engineUsageRowFor(eng.def, claims, u, took, ok)
	// Before the ledger, and whether or not there is one to write to: this is where the CP finds
	// out which model the router actually answered as, and the swap count that comes out of it
	// is the visible price of --models-max 1 (ADR 0072 decision 3).
	eng.noteServed(row.Model, ok)
	if !count || g.mgr == nil {
		return
	}
	// Detached from the request: the client's context is done the moment the answer is
	// delivered, and a row dropped because the reader hung up is a row nobody is billed for.
	go g.postUsage(context.WithoutCancel(ctx), mv, row)
}

func (g engineGateway) postUsage(ctx context.Context, mv store.MembershipView, row engineUsageRow) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	identityID, ok, err := g.mgr.store.IdentityIDForMembership(ctx, mv.MembershipID)
	if err != nil || !ok {
		log.Printf("engine usage: no identity for membership %s (%v)", mv.MembershipID, err)
		return
	}
	res, aerr := g.mgr.resolveByMembership(ctx, identityID, mv.MembershipID)
	if aerr != nil || res == nil || res.rt == nil || res.rt.Endpoint() == "" {
		// The workspace is stopped. That is not an error: the only caller that can produce
		// engine usage is a session inside a running workspace, so this means it went away
		// between the answer and the bookkeeping.
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
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		// An older Agent has no such route. Say so once per row rather than silently losing
		// the accounting — a CP newer than the image it launched must degrade visibly.
		log.Printf("engine usage: the agent answered %s (an older workspace image has no /engine/usage)", resp.Status)
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
func notifyEngineCatalogChanged(ctx context.Context, mgr *manager, key string) {
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
