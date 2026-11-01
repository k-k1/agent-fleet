// agent_liveness.go — the CP-side dead-man check for a running workspace whose Agent has
// stopped answering (#2518).
//
// In both memory-thrash incidents the container stayed RUNNING/HEALTHY for ECS while the
// Agent inside it made no progress, and every CP call ended in a timeout. Nothing said so.
//
// Design, in one paragraph. The CP already talks to every running Agent: the Console's
// polls, the relay, the usage sampler's sweep, the /healthz probes. Those clients share one
// RoundTripper wrapper (answerTransport) that records "an answer came back from this
// endpoint". A workspace is Agent-silent when the runtime says running, nothing has answered
// for agentSilentAfter, AND a fresh /healthz probe then fails. The probe is the authority, so
// the state is derived per replica with no shared storage: a replica that merely did not see
// the traffic probes, finds the Agent answering and stays quiet. Only the admin notice needs
// cross-replica memory (once per outage), and that is the existing workspace_notice mark.
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

// Audit actions, written by the replica that flipped its own view.
const (
	auditAgentSilent    = "workspace.agent_silent"
	auditAgentRecovered = "workspace.agent_recovered"
)

type liveEntry struct {
	since    time.Time // first observed running; the clock for an Agent that never answered
	answered time.Time // last answer; zero = none since `since`
	probedAt time.Time
	probing  bool
	silent   bool
	// recovered is set by an answer that arrived while silent; the next assess reports it
	// once so the caller (which holds the workspace row) can retire the notice.
	recovered bool
}

type agentLiveness struct {
	mu sync.Mutex
	m  map[string]*liveEntry
}

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
		if e.silent {
			e.silent = false
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
func (l *agentLiveness) assess(key string, now time.Time, after time.Duration) (silent, probe, recovered bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.m[key]
	if e == nil {
		l.m[key] = &liveEntry{since: now}
		return false, false, false
	}
	recovered, e.recovered = e.recovered, false
	last, limit := e.answered, after
	if last.IsZero() {
		last, limit = e.since, 2*after
	}
	if now.Sub(last) < limit {
		return false, false, recovered
	}
	if !e.probing && now.Sub(e.probedAt) >= agentSilentProbeEvery {
		e.probing, e.probedAt = true, now
		probe = true
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

// probeDone ends a probe. changed is true when it flipped the workspace to silent.
func (l *agentLiveness) probeDone(key string, answered bool) (changed bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.m[key]
	if e == nil {
		return false
	}
	e.probing = false
	if answered || e.silent {
		return false
	}
	e.silent = true
	return true
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
	if probe {
		go m.probeAgent(ws, rt.Endpoint(), key)
	}
	return silent
}

func (m *manager) probeAgent(ws store.Workspace, endpoint, key string) {
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
	if !agentLive.probeDone(key, answered) {
		return
	}
	log.Printf("agent silent: workspace %s runs but its Agent has not answered for %s and /healthz fails",
		ws.ID, agentSilentAfter())
	m.auditAgentLiveness(ctx, ws, auditAgentSilent)
	adminnotice.AgentSilent(ctx, m.store, ws, true)
}

func (m *manager) agentRecovered(ctx context.Context, ws store.Workspace) {
	log.Printf("agent silent: workspace %s answers again", ws.ID)
	m.auditAgentLiveness(ctx, ws, auditAgentRecovered)
	adminnotice.AgentSilent(context.WithoutCancel(ctx), m.store, ws, false)
}

func (m *manager) auditAgentLiveness(ctx context.Context, ws store.Workspace, action string) {
	_ = m.store.InsertAudit(context.WithoutCancel(ctx), store.AuditLog{
		ID: store.NewID(), TenantID: ws.TenantID, ActorKind: "system", ActorID: "agent-liveness",
		Action: action, Target: ws.ID, At: store.NowTS(),
	})
}
