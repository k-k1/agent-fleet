// Package usageall serves GET /usage/all: every agent kind's usage-chip payload in one
// response, so the Console's WS bar polls once instead of once per chip (#3127).
//
// Each kind's payload is exactly what its own endpoint answers — the same handler runs — so
// the chips read the same shapes as before and the per-kind endpoints stay the source of
// truth (MCP get_agent_usage and the AgyCard still call them directly).
package usageall

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"sync"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/agy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/copilot"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/muse"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// sources maps the response key to the kind's chip handler. A var so a test can stand in.
var sources = map[string]http.HandlerFunc{
	"claude":  claude.HandleUsage,
	"codex":   codex.HandleUsage,
	"muse":    muse.HandleUsage,
	"copilot": copilot.HandleUsage,
	"agy":     agy.HandleUsage,
}

// response is the body of GET /usage/all: each kind's chip payload, verbatim, by kind.
type response struct {
	Kinds map[string]json.RawMessage `json:"kinds"`
}

// recorder is the little of http.ResponseWriter the chip handlers use (httpx.WriteJSON).
type recorder struct {
	h      http.Header
	status int
	body   bytes.Buffer
}

func (r *recorder) Header() http.Header         { return r.h }
func (r *recorder) Write(b []byte) (int, error) { return r.body.Write(b) }
func (r *recorder) WriteHeader(code int)        { r.status = code }

// Handle serves GET /usage/all → {"kinds": {"claude": {...}, ...}}.
//
// The kinds run in parallel: agy scrapes its TUI and copilot makes an HTTP call, each up to
// seconds on a cache miss, and run one after another they would make every chip wait for the
// slowest. `?refresh=1&kind=<k>` forces a refresh of that one kind only — the dropdown's
// reload button — so pressing it on one chip never re-scrapes the others. A kind whose
// handler answers anything but 200 is left out; the Console treats a missing kind as "no
// new reading" and keeps what it showed.
func Handle(w http.ResponseWriter, r *http.Request) {
	refreshKind := ""
	if r.URL.Query().Get("refresh") != "" {
		refreshKind = r.URL.Query().Get("kind")
	}
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		out = map[string]json.RawMessage{}
	)
	for kind, h := range sources {
		wg.Add(1)
		go func() {
			defer wg.Done()
			u := url.URL{Path: r.URL.Path}
			if kind == refreshKind {
				u.RawQuery = "refresh=1"
			}
			req := r.Clone(r.Context())
			req.URL = &u
			req.RequestURI = u.String()
			rec := &recorder{h: http.Header{}, status: http.StatusOK}
			h(rec, req)
			if rec.status != http.StatusOK || !json.Valid(rec.body.Bytes()) {
				return
			}
			mu.Lock()
			out[kind] = json.RawMessage(bytes.TrimSpace(rec.body.Bytes()))
			mu.Unlock()
		}()
	}
	wg.Wait()
	httpx.WriteJSON(w, http.StatusOK, response{Kinds: out})
}
