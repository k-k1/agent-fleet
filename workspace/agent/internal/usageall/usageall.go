// Package usageall serves GET /usage/all: every agent kind's usage-chip payload in one
// response, so the Console's WS bar polls once instead of once per chip (#3127).
//
// Each kind's payload is exactly what its own endpoint answers — the same handler runs — so
// the chips read the same shapes as before and the per-kind endpoints stay the source of
// truth (MCP get_agent_usage and the AgyCard still call them directly).
package usageall

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"sort"
	"time"

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
// Pending names the kinds still reading when the budget ran out; the Console asks again
// shortly for those.
type response struct {
	Kinds   map[string]json.RawMessage `json:"kinds"`
	Pending []string                   `json:"pending,omitempty"`
}

// budget bounds how long one answer waits for the slow kinds. A cold agy scrape takes
// seconds (TUI start, up to 25 s), and one shared answer would otherwise hold every chip
// blank until it ends — the per-chip requests this replaced let the fast ones show at once.
// A var so a test can shorten it.
var budget = 3 * time.Second

// recorder is the little of http.ResponseWriter the chip handlers use (httpx.WriteJSON).
type recorder struct {
	h      http.Header
	status int
	body   bytes.Buffer
}

func (r *recorder) Header() http.Header         { return r.h }
func (r *recorder) Write(b []byte) (int, error) { return r.body.Write(b) }
func (r *recorder) WriteHeader(code int)        { r.status = code }

// Handle serves GET /usage/all → {"kinds": {"claude": {...}, ...}, "pending": [...]}.
//
// The kinds run in parallel and the answer waits at most budget for them. A kind still running
// then is listed in pending and keeps running on its own: its handler fills that kind's cache,
// so the Console's follow-up read finds it warm. `?refresh=1&kind=<k>` forces a refresh of
// that one kind only — the dropdown's reload button — and the answer always waits for it.
// A kind whose handler answers anything but 200, or panics, is left out; the Console treats a
// missing kind as "no new reading" and keeps what it showed.
func Handle(w http.ResponseWriter, r *http.Request) {
	refreshKind := ""
	if r.URL.Query().Get("refresh") != "" {
		refreshKind = r.URL.Query().Get("kind")
	}
	type result struct {
		kind string
		body json.RawMessage // nil: left out
	}
	// Buffered for every kind, so a handler that finishes after the answer was written never
	// blocks on a reader that has gone.
	results := make(chan result, len(sources))
	// A handler that outlives the answer must not see its request cancelled: copilot's fetch
	// would then fail and never fill the cache the follow-up read is waiting for.
	ctx := context.WithoutCancel(r.Context())
	for kind, h := range sources {
		go func() {
			res := result{kind: kind}
			defer func() {
				// A handler panicking here would take the whole Agent down: outside the
				// request's own goroutine net/http's recovery does not reach it.
				if p := recover(); p != nil {
					log.Printf("usage/all: %s handler panicked: %v", kind, p)
					res.body = nil
				}
				results <- res
			}()
			u := url.URL{Path: r.URL.Path}
			if kind == refreshKind {
				u.RawQuery = "refresh=1"
			}
			req := r.Clone(ctx)
			req.URL = &u
			req.RequestURI = u.String()
			rec := &recorder{h: http.Header{}, status: http.StatusOK}
			h(rec, req)
			if rec.status == http.StatusOK && json.Valid(rec.body.Bytes()) {
				res.body = json.RawMessage(bytes.TrimSpace(rec.body.Bytes()))
			}
		}()
	}
	out := map[string]json.RawMessage{}
	waiting := map[string]bool{}
	for kind := range sources {
		waiting[kind] = true
	}
	timer := time.NewTimer(budget)
	defer timer.Stop()
	expired := false
	for len(waiting) > 0 && !(expired && !waiting[refreshKind]) {
		select {
		case res := <-results:
			delete(waiting, res.kind)
			if res.body != nil {
				out[res.kind] = res.body
			}
		case <-timer.C:
			expired = true
		}
	}
	resp := response{Kinds: out}
	for kind := range waiting {
		resp.Pending = append(resp.Pending, kind)
	}
	sort.Strings(resp.Pending)
	httpx.WriteJSON(w, http.StatusOK, resp)
}
