// agent_liveness.go — the CP-side dead-man check: a workspace the runtime reports running whose
// Agent has stopped answering (#2518). ECS cannot tell: the workspace container has no health
// check.
//
// The CP->Agent HTTP clients share one RoundTripper wrapper (answerTransport) that records
// every answer per endpoint. A workspace is Agent-silent when nothing has answered for
// agentSilentAfter AND a fresh /healthz probe then fails. The probe is the authority, so the
// state is derived per replica with no shared storage: a replica that merely missed the
// traffic probes, finds the Agent answering and stays quiet. Only the admin notice (and its
// audit rows) need cross-replica memory, and that is the workspace_notice mark.
package main

import (
	"context"
	"log"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/adminnotice"
	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// defaultAgentSilentAfter is how long a running workspace may go without any answer before
// the CP probes it. It sits well above anything a slow-but-alive Agent does (every bounded
// CP call is 5-10 s, agentReadyWait is 55 s, so even three missed ingress windows fit) and
// above the CP's own idle gap (the usage sweep answers once per 5 min, so an unwatched
// workspace is probed at most once per sweep, never continuously), yet it is a fraction of
// the 40 minutes the second incident went unnoticed. AF_AGENT_SILENT_AFTER overrides it
// ("0" turns the check off).
const defaultAgentSilentAfter = 3 * time.Minute

// agentSilentProbeEvery rate-limits the /healthz probe per workspace: a Console polling every
// 4 s must not turn into a probe every 4 s against an Agent that is already struggling.
var agentSilentProbeEvery = 30 * time.Second

func agentSilentAfter() time.Duration {
	return intervalOff(os.Getenv("AF_AGENT_SILENT_AFTER"), defaultAgentSilentAfter)
}

type liveEntry struct {
	since    time.Time // first observed running; the clock for an Agent that never answered
	answered time.Time // last answer; zero = none since `since`
	probedAt time.Time
	probing  bool
	// gen identifies this running spell and rev counts answers; a probe that started under
	// another gen, or before a later answer, must not flip the entry.
	gen, rev uint64
	silent   bool
	// silentAt is when the probe flipped silent; the auto-stop ceiling (agent_silent_stop.go)
	// is measured from it, never from the last answer, so a CP restart restarts that clock too.
	silentAt time.Time
	// recovered is set by an answer that arrived while silent; the next assess reports it
	// once so the caller (which holds the workspace row) can retire the notice.
	recovered bool
}

type agentLiveness struct {
	mu      sync.Mutex
	m       map[string]*liveEntry
	nextGen uint64
}

// probeToken is what a probe remembers from the moment it started.
type probeToken struct{ gen, rev uint64 }

var agentLive = &agentLiveness{m: map[string]*liveEntry{}}

// endpointKey is the map key: the Agent's host:port. Both the transport (which only sees a
// request URL) and the callers (which hold a Runtime) can compute it.
func endpointKey(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Host
}

// note records an answer. It only updates an entry that assess created, so traffic to a
// workspace nobody is assessing (or one that was deleted) cannot grow the map.
func (l *agentLiveness) note(key string, now time.Time) {
	if key == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if e := l.m[key]; e != nil {
		e.answered = now
		e.rev++
		if e.silent {
			e.silent, e.silentAt = false, time.Time{}
			e.recovered = true
		}
	}
}

// forget drops the entry: the workspace is not running, so there is nothing to be silent
// about, and its next running spell starts a fresh clock.
func (l *agentLiveness) forget(key string) {
	l.mu.Lock()
	delete(l.m, key)
	l.mu.Unlock()
}

// assess is called for a workspace the runtime reports running. A first sight starts the
// clock (so a CP restart, or a workspace that just came up, gets a full grace period and
// cannot raise an alarm storm). An Agent that has never answered since then gets twice the
// period: right after the boot budget (runtime.AgentBootBudget) State() falls back to
// running even if the entrypoint is still installing.
func (l *agentLiveness) assess(key string, now time.Time, after time.Duration) (silent bool, probe *probeToken, recovered bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.m[key]
	if e == nil {
		l.nextGen++
		l.m[key] = &liveEntry{since: now, gen: l.nextGen}
		return false, nil, false
	}
	recovered, e.recovered = e.recovered, false
	last, limit := e.answered, after
	if last.IsZero() {
		last, limit = e.since, 2*after
	}
	if now.Sub(last) < limit {
		return false, nil, recovered
	}
	if !e.probing && now.Sub(e.probedAt) >= agentSilentProbeEvery {
		e.probing, e.probedAt = true, now
		probe = &probeToken{gen: e.gen, rev: e.rev}
	}
	return e.silent, probe, recovered
}

// answeredWithin reports a positive sign of life: an answer newer than d.
func (l *agentLiveness) answeredWithin(key string, now time.Time, d time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.m[key]
	return e != nil && !e.answered.IsZero() && now.Sub(e.answered) < d
}

// probeDone ends a probe. A failure counts only if the entry is the one the probe started
// under and nothing has answered since; otherwise it is stale evidence and is dropped.
// changed is true when it flipped the workspace to silent.
func (l *agentLiveness) probeDone(key string, tok probeToken, answered bool) (changed bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.m[key]
	if e == nil || e.gen != tok.gen {
		return false
	}
	e.probing = false
	if answered || e.silent || e.rev != tok.rev {
		return false
	}
	e.silent, e.silentAt = true, time.Now()
	return true
}

// silentFor is how long the entry has been flagged silent (0 when it is not).
func (l *agentLiveness) silentFor(key string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e := l.m[key]; e != nil && e.silent && !e.silentAt.IsZero() {
		return now.Sub(e.silentAt)
	}
	return 0
}

// silentToken identifies the silent verdict of key's entry: its generation and answer count, and
// how long it has stood. ok is false when the entry is not flagged.
func (l *agentLiveness) silentToken(key string, now time.Time) (tok probeToken, silentFor time.Duration, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.m[key]
	if e == nil || !e.silent || e.silentAt.IsZero() {
		return probeToken{}, 0, false
	}
	return probeToken{gen: e.gen, rev: e.rev}, now.Sub(e.silentAt), true
}

// silentSince is when key's entry was flagged (zero when it is not).
func (l *agentLiveness) silentSince(key string) time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e := l.m[key]; e != nil && e.silent {
		return e.silentAt
	}
	return time.Time{}
}

// sameVerdict reports whether the entry is still flagged under tok: no answer, recovery or new
// running spell since the token was taken.
func (l *agentLiveness) sameVerdict(key string, tok probeToken) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.m[key]
	return e != nil && e.silent && e.gen == tok.gen && e.rev == tok.rev
}

// genOf is the current generation of key's entry (0 if none).
func (l *agentLiveness) genOf(key string) uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e := l.m[key]; e != nil {
		return e.gen
	}
	return 0
}

// stillSilent reports whether the entry of generation gen is still flagged.
func (l *agentLiveness) stillSilent(key string, gen uint64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.m[key]
	return e != nil && e.gen == gen && e.silent
}

// answerTransport records every answer that came back from an Agent. A 502/503/504 is what
// the Service Connect proxy says on the Agent's behalf when nothing is listening, so it is
// not an answer. A transport error is no evidence either way here: the probe decides.
type answerTransport struct{ base http.RoundTripper }

func (t answerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err == nil {
		switch resp.StatusCode {
		case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		default:
			agentLive.note(req.URL.Host, time.Now())
		}
	}
	return resp, err
}

func newAnswerTransport() http.RoundTripper { return answerTransport{newAgentTransport()} }

// checkAgentSilent reports whether the running workspace ws is Agent-silent, and keeps the
// admin notice and the audit trail in step. state is the runtime state the caller already
// holds; anything but "running" clears the entry (never flag during starting or stopping).
// It never blocks on the Agent: the confirming probe runs in its own goroutine.
func (m *manager) checkAgentSilent(ctx context.Context, ws store.Workspace, rt runtime.Runtime, state string) bool {
	key := endpointKey(rt.Endpoint())
	if key == "" {
		return false
	}
	after := agentSilentAfter()
	if state != "running" || after <= 0 {
		agentLive.forget(key)
		return false
	}
	silent, probe, recovered := agentLive.assess(key, time.Now(), after)
	if recovered {
		m.agentRecovered(ctx, ws)
	}
	if probe != nil {
		go m.probeAgent(ws, rt.Endpoint(), key, *probe)
	}
	return silent
}

func (m *manager) probeAgent(ws store.Workspace, endpoint, key string, tok probeToken) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*healthzClient.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/healthz", nil)
	answered := false
	if err == nil {
		// The transport notes the answer; nothing to do with the body.
		if resp, derr := healthzClient.Do(req); derr == nil {
			resp.Body.Close()
			answered = resp.StatusCode != http.StatusBadGateway &&
				resp.StatusCode != http.StatusServiceUnavailable && resp.StatusCode != http.StatusGatewayTimeout
		}
	}
	if !agentLive.probeDone(key, tok, answered) {
		return
	}
	log.Printf("agent silent: workspace %s runs but its Agent has not answered for %s and /healthz fails",
		ws.ID, agentSilentAfter())
	adminnotice.AgentSilent(ctx, m.store, ws, true)
	// An answer (or a stop) may have landed between the verdict and the raise; the mark must
	// not outlive it, and the recovery flag may already have been consumed.
	if !agentLive.stillSilent(key, tok.gen) {
		adminnotice.AgentSilent(context.WithoutCancel(ctx), m.store, ws, false)
	}
}

func (m *manager) agentRecovered(ctx context.Context, ws store.Workspace) {
	log.Printf("agent silent: workspace %s answers again", ws.ID)
	adminnotice.AgentSilent(context.WithoutCancel(ctx), m.store, ws, false)
}
