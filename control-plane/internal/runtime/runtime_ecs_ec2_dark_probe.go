package runtime

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"syscall"
	"time"
)

// Probe of the workspace Agent for a slot whose service is up (ADR 0121 decision 1, last
// bullet of the predicate). The ECS and SSM legs say the box is silent to the cluster; a
// service that is up may still be serving its member, so the sweeper asks the Agent itself
// before it calls such a slot dark.

// AgentProbe is the answer of one probe. The zero value is AgentProbeUnknown on purpose: a
// result nobody filled in must never read as "failed".
type AgentProbe int

const (
	// AgentProbeUnknown: the question was not answered. A probe that could not reach the
	// Agent's side at all (no route, a CP-side error, a cancelled context, a slow answer,
	// an exhausted budget) says nothing about the Agent.
	AgentProbeUnknown AgentProbe = iota
	// AgentProbeHealthy: the Agent answered.
	AgentProbeHealthy
	// AgentProbeFailed: the connection reached the Agent's side and was dropped (reset,
	// EOF) or the Service Connect proxy answered 502/503/504 for it.
	AgentProbeFailed
)

func (p AgentProbe) String() string {
	switch p {
	case AgentProbeHealthy:
		return "healthy"
	case AgentProbeFailed:
		return "failed"
	}
	return "unknown"
}

const (
	// darkProbeBudgetDefault bounds the probing of one sweep pass; darkProbeTimeoutDefault
	// bounds one probe. Probes run one after another, so the budget is what keeps a box
	// that answers slowly from stretching the pass for every slot behind it.
	darkProbeBudgetDefault  = 30 * time.Second
	darkProbeTimeoutDefault = 5 * time.Second
)

// probeObservation is the latest probe of a slot, kept so a later predicate (the wedged
// slot, #3963) can use it as a signal of its own without probing again.
type probeObservation struct {
	Result AgentProbe
	At     time.Time
}

// agentProbes is the factory's memory of the latest probe per slot (instance id).
type agentProbes struct {
	mu sync.Mutex
	m  map[string]probeObservation
}

func (a *agentProbes) set(slot string, o probeObservation) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.m == nil {
		a.m = map[string]probeObservation{}
	}
	a.m[slot] = o
}

func (a *agentProbes) forget(slot string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.m, slot)
}

// LastAgentProbe returns the latest Agent probe of a slot and when it ran. ok is false when
// the slot was never probed or its last pass did not probe it (service down, a leg healthy,
// a read failed): a caller must check At against its own freshness bound.
func (f *ecsEC2Factory) LastAgentProbe(slot string) (AgentProbe, time.Time, bool) {
	f.probes.mu.Lock()
	defer f.probes.mu.Unlock()
	o, ok := f.probes.m[slot]
	return o.Result, o.At, ok
}

func (f *ecsEC2Factory) darkNow() time.Time {
	if f.clock != nil {
		return f.clock()
	}
	return time.Now()
}

func (f *ecsEC2Factory) agentDoer() httpDoer {
	if f.agentHTTP != nil {
		return f.agentHTTP
	}
	return healthzClient // the CP's client, with the Cloud Map fallback (deps.go)
}

// darkProbePass is the time budget of one sweep pass.
type darkProbePass struct{ deadline time.Time }

func (f *ecsEC2Factory) newDarkProbePass() *darkProbePass {
	b := f.pool.darkProbeBudget
	if b <= 0 {
		b = darkProbeBudgetDefault
	}
	return &darkProbePass{deadline: f.darkNow().Add(b)}
}

// probeAgent makes one GET /healthz against endpoint. It sends no credentials: /healthz is
// the Agent's open readiness route, so the bearer token never enters this path or its logs.
func (f *ecsEC2Factory) probeAgent(ctx context.Context, pass *darkProbePass, endpoint string) AgentProbe {
	if endpoint == "" || ctx.Err() != nil {
		return AgentProbeUnknown
	}
	remaining := pass.deadline.Sub(f.darkNow())
	if remaining <= 0 {
		return AgentProbeUnknown // budget spent: this slot is not looked at this pass
	}
	each := f.pool.darkProbeTimeout
	if each <= 0 {
		each = darkProbeTimeoutDefault
	}
	pctx, cancel := context.WithTimeout(ctx, min(each, remaining))
	defer cancel()
	req, err := http.NewRequestWithContext(pctx, http.MethodGet, endpoint+"/healthz", nil)
	if err != nil {
		return AgentProbeUnknown
	}
	resp, err := f.agentDoer().Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return AgentProbeUnknown
		}
		return classifyProbeError(err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		// What the Service Connect proxy says for an Agent nobody is listening for.
		return AgentProbeFailed
	}
	return AgentProbeHealthy
}

// classifyProbeError maps a transport error onto failed only for the shapes measured on a
// thrashing box (docs: ADR 0121 Open question 6): the connection was made and then dropped.
// Everything else, a slow answer included, is unknown: a healthy but slow Agent must not be
// called failed, and when in doubt the slot stays unjudged.
func classifyProbeError(err error) AgentProbe {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return AgentProbeUnknown
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return AgentProbeUnknown
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) {
		return AgentProbeFailed
	}
	return AgentProbeUnknown
}
