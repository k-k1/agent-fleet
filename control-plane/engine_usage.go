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
	"log"
	"net/http"
	"strings"
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
var engineUsageClient = &http.Client{Timeout: 10 * time.Second}

func (g engineGateway) recordUsage(ctx context.Context, eng *engineRuntimeState,
	claims engineSessionClaims, mv store.MembershipView, u engineUsage, took time.Duration, ok bool) {

	row := engineUsageRow{
		Feature:  engineUsageFeature(eng.def.Key),
		Provider: eng.def.Provider,
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
	if g.mgr == nil {
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
