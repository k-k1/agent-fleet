package sessionx

// Agent-boot wiring for the fleet session graph (ADR 0096 decision 3 / decision 8):
// FleetGraphResync seeds the state-change writer's dedup map every restart, and
// FleetGraphBackfillFromMeta writes the one-time genesis skeleton from existing Metas the
// first time this feature runs in a given AgentStateDir.

import (
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/fleetgraph"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// FleetGraphResync writes one resync line per listed session (skipping archived ones, the
// same way HandleListSessions does) and reseeds the state-writer's last-observed-state map,
// so the very first ObserveState call after boot has a real `from` instead of none. Call
// once, synchronously, before the HTTP server starts accepting requests — a state line
// written before this runs would otherwise land with no resync boundary behind it.
func FleetGraphResync() {
	metas := map[string]session.Meta{}
	for _, m := range session.ListMetas() {
		metas[m.Name] = m
	}
	live := tmuxx.LiveSessionNames()
	for name, m := range metas {
		if m.DriverKind() == session.DriverManaged && ManagedAlive(m) {
			live[name] = true
		}
	}
	states := make(map[string]string, len(metas))
	for name, m := range metas {
		if m.Archived {
			continue
		}
		states[name] = wireSession(m, live[name]).State
	}
	fleetgraph.ResyncAll(states)
}

// FleetGraphBackfillFromMeta writes the genesis lineage skeleton (ADR 0096 decision 8) from
// every session Meta still on disk: one birth, and at most one death for a session already
// stopped. It runs exactly once per AgentStateDir (fleetgraph.BackfillDone gates it) — a
// second call after the ledger has real history would re-date lanes that have since moved
// on, and decision 12 is explicit that a pre-feature session's past stop/resume cycles
// cannot be recovered anyway (Meta keeps only the LATEST StoppedAt), so there is nothing
// later runs could usefully add.
func FleetGraphBackfillFromMeta() {
	if fleetgraph.BackfillDone() {
		return
	}
	for _, m := range session.ListMetas() {
		createdAt, _ := time.Parse(time.RFC3339, m.CreatedAt)
		fleetgraph.RecordBirth(fleetgraph.Birth{
			Name: m.Name, Kind: NormalizeKind(m.Kind), Repo: m.Repo,
			Origin: fleetgraph.NormalizeOrigin(session.OriginOf(m)), OriginSession: m.OriginSession,
			ForkFrom: m.ForkFrom, Display: session.Display(m), At: createdAt,
		})
		if m.StoppedAt != "" {
			if stoppedAt, err := time.Parse(time.RFC3339, m.StoppedAt); err == nil {
				fleetgraph.RecordDeathAt(m.Name, "", 0, 0, stoppedAt)
			}
		}
		if m.Archived {
			// The exact archive instant is not recoverable from Meta — stamped at backfill
			// time (decision 8's coarse skeleton), which is still enough for presence to
			// read right going forward.
			fleetgraph.RecordArchived(m.Name, true)
		}
	}
	fleetgraph.MarkBackfillDone()
}
