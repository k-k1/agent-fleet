package routes

import (
	"log"
	"time"
)

// Launch is what a session ran on when it was launched (ADR 0138 D11): the route its create
// resolved to, explicit or not, and the binding of the credential instance behind it (D5b,
// internal/credbind). It is recorded at create, fork and recreate, moved only by a consented
// route switch (a recorded transition), and never re-resolved from today's credential: a policy
// judges an existing session on this record, and a resume whose current credential no longer
// matches it is a replacement, refused rather than moved. A session created before the record
// existed has none (nil); an empty Route is a create whose route could not be resolved.
type Launch struct {
	Route   string `json:"route,omitempty"`
	Binding string `json:"binding,omitempty"`
}

// Clone returns an independent copy, nil for nil.
func (l *Launch) Clone() *Launch {
	if l == nil {
		return nil
	}
	c := *l
	return &c
}

// ObserveBudget bounds how long an execution path waits for an observation of its route and
// binding (a launch record, a ledger row's attribution). The observation is evidence, not a
// condition of the call: a resolver that is slow or hangs (a cold CLI probe, a locked store, an
// unreachable file system) costs the record, never the call.
const ObserveBudget = 500 * time.Millisecond

// Observe runs f within budget and returns its answer, or ok=false when it has not answered in
// time; a late answer is dropped. what names the observation in the log line.
func Observe(budget time.Duration, what string, f func() Launch) (Launch, bool) {
	ch := make(chan Launch, 1)
	go func() { ch <- f() }()
	t := time.NewTimer(budget)
	defer t.Stop()
	select {
	case l := <-ch:
		return l, true
	case <-t.C:
		log.Printf("route observation: %s did not answer within %v; recorded as unresolved", what, budget)
		return Launch{}, false
	}
}
