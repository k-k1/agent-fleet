package sessionx

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"os"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// firstTurnOrphanAge is how old a first-turn ledger must be before "its session has no meta"
// is believed. A create arms the ledger before it publishes the meta, and the longest create
// (a working copy clone, then the launch) takes minutes; a day is far above that, so a create
// in flight is never mistaken for a failed one. A ledger removed wrongly cannot be rebuilt, and
// the Control Plane's circuit breaker then never counts that delivery.
const firstTurnOrphanAge = 24 * time.Hour

// SweepFirstTurnOrphans removes the first-turn ledgers (status.FirstTurn) that no session meta
// claims any more: a create that failed after arming, or whose meta could not be written. Run
// at Agent start, where the other per-session leftovers are settled.
//
// Ledgers are keyed by session.UUID(dir, name), so the live set is built from the metas rather
// than from names. The metas are read here and not through session.ListMetas, because that
// skips a file it cannot parse, and a meta caught mid-rewrite looks the same as a session that
// has none: when any meta file cannot be read or parsed the whole round is skipped, and every
// ledger stays. The age bound above covers a meta that vanishes between the listing and its
// read.
func SweepFirstTurnOrphans() int {
	ents, err := os.ReadDir(session.MetaDir())
	if err != nil {
		// A missing directory is not read as "no sessions": it may be a store that is not
		// mounted, and then every ledger would look orphaned.
		return 0
	}
	live := map[string]bool{}
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(session.MetaPath(strings.TrimSuffix(e.Name(), ".json")))
		if errors.Is(err, fs.ErrNotExist) {
			continue // deleted since the listing
		}
		var m session.Meta
		if err != nil || json.Unmarshal(b, &m) != nil {
			log.Printf("first-turn sweep: %s is unreadable; keeping every ledger this round", e.Name())
			return 0
		}
		if m.Name == "" {
			m.Name = strings.TrimSuffix(e.Name(), ".json")
		}
		live[session.UUID(m.Dir, m.Name)] = true
	}
	// The turn starts (status.TurnStart) are keyed and orphaned the same way.
	status.SweepOrphanTurnStarts(live, firstTurnOrphanAge, time.Now())
	return status.SweepOrphanFirstTurns(live, firstTurnOrphanAge, time.Now())
}
