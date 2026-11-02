// agent_sessions_share.go — one Agent GET /sessions per workspace per tick, however many
// Console windows are subscribed (#3236).
//
// Every /api/events subscriber ticks on its own and used to call the Agent's GET /sessions
// itself, so N visible windows for one workspace cost N Agent reads per tick (each one a git
// spawn per working copy on the Agent side). agentSessionsShared collapses concurrent callers
// into one in-flight read and reuses a successful result for sharedSessionsTTL, which is below
// the 4 s tick, so the subscribers of one workspace end up sharing one read per tick.
//
// Only the raw Agent envelope is shared. Everything that differs per member (inbox origins,
// notifications, the engine gate) is applied by the caller on its own copy of the list; the
// copy below is what makes that safe, because applyInboxOrigins writes into the slice it gets.
//
// Callers that decide something from the read (the reaper, the scheduler, quota and mirror
// checks, mutation follow-ups) keep calling agentSessionsEnvQuery directly and stay fresh.
package main

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
)

const (
	// sharedSessionsTTL stays under eventsTick (4 s): a longer reuse would let a subscriber
	// serve the same list for two ticks and delay every state change by a tick.
	sharedSessionsTTL = 2500 * time.Millisecond
	// sharedSessionsTimeout bounds the detached read. The read belongs to no single caller, so
	// no caller's context can bound it.
	sharedSessionsTimeout = 20 * time.Second
	// sharedSessionsMax bounds the map; expired entries are dropped on every insert, so this is
	// only reached by that many workspaces being read within one TTL.
	sharedSessionsMax = 4096
)

// sharedSessionsKey carries everything that changes the response. The endpoint moves with the
// Agent (restart, blue/green, recreate), so a re-pointed workspace never reads the old list.
type sharedSessionsKey struct {
	endpoint, token, query string
}

type sharedSessionsEntry struct {
	env     agentSessionsEnvelope
	readAt  time.Time // when the read started — what the DB mirror orders by
	expires time.Time
}

type sharedSessionsCall struct {
	done   chan struct{}
	env    agentSessionsEnvelope
	readAt time.Time
	err    error
}

// sessionsShare is zero-value usable (tests build &manager{}).
type sessionsShare struct {
	mu       sync.Mutex
	gen      uint64 // bumped by invalidate; a read that started under an older gen is not cached
	entries  map[sharedSessionsKey]sharedSessionsEntry
	inflight map[sharedSessionsKey]*sharedSessionsCall
	// ttl overrides sharedSessionsTTL when non-zero (tests that tick faster than production).
	ttl time.Duration
	// fetch and now are test seams; nil means agentSessionsEnvQuery and time.Now.
	fetch func(ctx context.Context, rt runtime.Runtime, query string) (agentSessionsEnvelope, error)
	now   func() time.Time
}

func (s *sessionsShare) reuse() time.Duration {
	if s.ttl != 0 {
		return s.ttl
	}
	return sharedSessionsTTL
}

func (s *sessionsShare) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// invalidate drops every cached list and keeps reads already in flight from being cached. Called
// after a write that goes through this CP, so the next read sees it. Coarse on purpose: a write
// is rare next to the 4 s ticks, and keying it by workspace would need the endpoint the write's
// handler may not have.
func (s *sessionsShare) invalidate() {
	s.mu.Lock()
	s.gen++
	s.entries = nil
	// Detach reads in flight: a caller arriving after the write must start its own read, not join
	// one that may have sampled the Agent before the write.
	s.inflight = nil
	s.mu.Unlock()
}

// agentSessionsShared returns the Agent's plain /sessions envelope for rt, shared with other
// callers of the same Agent, plus when the underlying read started. The returned Sessions slice
// is the caller's own.
//
// A failed read is returned to the callers waiting on it and never stored. The read runs on a
// context of its own, so one caller going away neither cancels nor fails the others; a caller
// whose own ctx ends stops waiting and gets ctx.Err().
func (m *manager) agentSessionsShared(ctx context.Context, rt runtime.Runtime) (agentSessionsEnvelope, time.Time, error) {
	s := &m.sessionsShare
	key := sharedSessionsKey{endpoint: rt.Endpoint(), token: rt.Token()}
	s.mu.Lock()
	if e, ok := s.entries[key]; ok && s.clock().Before(e.expires) {
		s.mu.Unlock()
		return cloneEnvelope(e.env), e.readAt, nil
	}
	c, running := s.inflight[key]
	if !running {
		c = &sharedSessionsCall{done: make(chan struct{})}
		if s.inflight == nil {
			s.inflight = map[sharedSessionsKey]*sharedSessionsCall{}
		}
		s.inflight[key] = c
		gen := s.gen
		go m.runSharedSessions(rt, key, c, gen)
	}
	s.mu.Unlock()
	select {
	case <-c.done:
	case <-ctx.Done():
		return agentSessionsEnvelope{}, time.Time{}, ctx.Err()
	}
	if c.err != nil {
		return agentSessionsEnvelope{}, time.Time{}, c.err
	}
	return cloneEnvelope(c.env), c.readAt, nil
}

func (m *manager) runSharedSessions(rt runtime.Runtime, key sharedSessionsKey, c *sharedSessionsCall, gen uint64) {
	s := &m.sessionsShare
	ctx, cancel := context.WithTimeout(context.Background(), sharedSessionsTimeout)
	defer cancel()
	fetch := s.fetch
	if fetch == nil {
		fetch = m.agentSessionsEnvQuery
	}
	c.readAt = s.clock()
	c.env, c.err = fetch(ctx, rt, "")
	s.mu.Lock()
	if s.inflight[key] == c {
		delete(s.inflight, key)
	}
	if c.err == nil && gen == s.gen {
		now := s.clock()
		if s.entries == nil {
			s.entries = map[sharedSessionsKey]sharedSessionsEntry{}
		}
		for k, e := range s.entries {
			if !now.Before(e.expires) {
				delete(s.entries, k)
			}
		}
		if len(s.entries) < sharedSessionsMax {
			s.entries[key] = sharedSessionsEntry{env: c.env, readAt: c.readAt, expires: now.Add(s.reuse())}
		}
	}
	s.mu.Unlock()
	close(c.done)
}

// cloneEnvelope copies the session slice so a caller may write into its elements' top-level
// fields (applyInboxOrigins does). Fields that hold pointers or raw JSON are shared and must be
// replaced, never edited in place.
func cloneEnvelope(e agentSessionsEnvelope) agentSessionsEnvelope {
	// nil stays nil and an empty list stays empty: {"sessions":[]} must not become null, which the
	// Console ignores as "no list".
	if e.Sessions != nil {
		e.Sessions = append(make([]sessionWire, 0, len(e.Sessions)), e.Sessions...)
	}
	return e
}

// invalidatesSessionsOnWrite wraps the whole mux, so that every route that may change the session
// list drops the shared read, whichever auth wrapper (or none: /mcp, /internal/*, admin and
// schedule routes) the route uses. It runs after the handler, so a read begun during the write is
// dropped too. GET/HEAD/OPTIONS never invalidate: the 4 s event ticks are GETs and would defeat
// the sharing.
//
// Public surfaces that cannot change the session list never invalidate (writeMayChangeSessions):
// they answer anonymous callers with 302/400/200/503, which no status list can tell from a real
// write.
//
// A refusal or routing redirect that happened before any handler work (401, 403, 404, 405, 3xx) does not invalidate either:
// an anonymous POST to /internal/* or to a route that does not exist would otherwise flush every
// workspace's cache and detach its in-flight read. Any other status, 5xx after a partial success
// included, invalidates.
func (m *manager) invalidatesSessionsOnWrite(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if !writeMayChangeSessions(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		sw := &statusWriter{ResponseWriter: w} // keeps Flusher, Hijacker and ResponseController working
		defer func() {
			switch c := sw.code(); {
			case c == http.StatusUnauthorized, c == http.StatusForbidden, c == http.StatusNotFound,
				c == http.StatusMethodNotAllowed,
				// ServeMux answers a cleaned or subtree path with a redirect before any handler
				// (or its auth) runs; no session-changing route answers a write with a redirect.
				c >= 300 && c < 400:
			default:
				m.sessionsShare.invalidate()
			}
		}()
		next.ServeHTTP(sw, r)
	})
}

// writeMayChangeSessions reports whether a non-GET on path can change the session list. Routes
// reachable without a session (the auth-exempt registry: login, OAuth/MCP-OAuth, legacy redirect,
// git, engine gateway) cannot, except /mcp and the token-authenticated /internal/* routes (session
// tools, schedule run-now), which authenticate inside the handler. Deriving this from the registry
// keeps a newly exempted public route from becoming an anonymous cache-flush.
func writeMayChangeSessions(path string) bool {
	if strings.HasPrefix(path, "/internal/engine/") {
		return false
	}
	if path == "/mcp" || strings.HasPrefix(path, "/mcp/") || strings.HasPrefix(path, "/internal/") {
		return true
	}
	return !isAuthExempt(path)
}
