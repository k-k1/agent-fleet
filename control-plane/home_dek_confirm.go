package main

// The confirm step of the home key (ADR 0005 addendum 2026-10-10, part B). The CP cannot see a
// home's secrets.enc, so it never decides by itself that a store has moved to the home's key:
// it reads what the Agent reported at boot on /healthz, and only a report from an Agent that
// had the home's key in use counts.

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// The Agent's key states (workspace/agent/internal/secrets: KeyState*).
const (
	agentKeyNone       = "none"
	agentKeyCurrent    = "current"
	agentKeyMigrated   = "migrated"
	agentKeyDerived    = "derived"
	agentKeyUnreadable = "unreadable"
)

// Defaults for the wait after a start: long enough for an ECS cold start (image pull and
// task placement), and the poll is one /healthz call.
const (
	homeDEKConfirmBudgetDefault = 15 * time.Minute
	homeDEKConfirmPollDefault   = 3 * time.Second
)

// agentKeyReport is the part of the Agent's /healthz the confirm step reads. Next says the
// Agent sealed its store under AF_SECRET_KEY_NEXT; without it "current" only means the store
// opens with AF_SECRET_KEY.
type agentKeyReport struct {
	State string `json:"secrets_key"`
	Next  bool   `json:"secrets_key_next"`
}

// agentKeyState makes one /healthz call. ok is false while the Agent does not answer 200.
func agentKeyState(ctx context.Context, rt runtime.Runtime) (agentKeyReport, bool) {
	if rt.Endpoint() == "" {
		return agentKeyReport{}, false
	}
	req, err := http.NewRequestWithContext(ctx, "GET", rt.Endpoint()+"/healthz", nil)
	if err != nil {
		return agentKeyReport{}, false
	}
	resp, err := healthzClient.Do(req)
	if err != nil {
		return agentKeyReport{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return agentKeyReport{}, false
	}
	var r agentKeyReport
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return agentKeyReport{}, false
	}
	return r, true
}

// watchHomeDEK waits for the Agent the start just launched and acts on its key report. A
// CP that restarts in the meantime loses the wait, and the home stays 'migrating': its next
// start injects both keys again and is confirmed then.
func (m *manager) watchHomeDEK(rt runtime.Runtime, home store.HomeDEK) {
	if rt.Endpoint() == "" {
		return // nothing to ask; the next start tries again
	}
	budget, poll := m.homeDEKConfirmBudget, m.homeDEKConfirmPoll
	if budget <= 0 {
		budget = homeDEKConfirmBudgetDefault
	}
	if poll <= 0 {
		poll = homeDEKConfirmPollDefault
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	for {
		if r, ok := agentKeyState(ctx, rt); ok {
			m.applyHomeDEKReport(ctx, home, r)
			return
		}
		select {
		case <-ctx.Done():
			log.Printf("home key of membership %s: no report from the Agent within %s; it stays %s", home.MembershipID, budget, home.Scheme)
			return
		case <-time.After(poll):
		}
	}
}

// applyHomeDEKReport confirms a migrating home only on a report from an Agent that sealed
// under the home's key and holds a readable (or no) store. Anything else changes nothing,
// and nothing is ever deleted here.
func (m *manager) applyHomeDEKReport(ctx context.Context, home store.HomeDEK, r agentKeyReport) {
	switch {
	case r.State == "":
		// An Agent that predates the home key: it ignored AF_SECRET_KEY_NEXT.
		log.Printf("home key of membership %s: the workspace image does not report its key; it stays %s", home.MembershipID, home.Scheme)
	case r.State == agentKeyUnreadable || r.State == agentKeyDerived:
		hint := ""
		if home.Scheme == store.HomeDEKRandom {
			hint = " (a home restored from before its store moved: af-cp home-dek-status --remigrate " + home.MembershipID + ", then restart it)"
		}
		log.Printf("WARNING: home key of membership %s: the Agent reports its credential store %s; it stays %s%s", home.MembershipID, r.State, home.Scheme, hint)
	case home.Scheme != store.HomeDEKMigrating:
	case !r.Next:
		log.Printf("home key of membership %s: the Agent did not use the home's key; it stays migrating", home.MembershipID)
	case r.State == agentKeyNone || r.State == agentKeyCurrent || r.State == agentKeyMigrated:
		ok, err := m.store.ConfirmHomeDEK(ctx, home.MembershipID, home.Ciphertext)
		switch {
		case err != nil:
			log.Printf("home key of membership %s: confirm: %v; it stays migrating", home.MembershipID, err)
		case ok:
			log.Printf("home key of membership %s: confirmed (%s); later starts get the home's key alone", home.MembershipID, r.State)
		}
	default:
		log.Printf("home key of membership %s: unknown report %q; it stays migrating", home.MembershipID, r.State)
	}
}
