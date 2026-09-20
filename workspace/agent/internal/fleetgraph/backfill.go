package fleetgraph

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// backfillMarkerPath records the ONE instant genesis backfill ran (ADR 0096 decision 8):
// lineage older than it is the coarse "wrote from Meta at first boot" skeleton — one birth
// and at most one death per lane, never arrows or earlier runs — and coverage.
// backfilledBefore is how the view draws that boundary instead of fading out silently.
func backfillMarkerPath() string { return filepath.Join(dir(), "backfilled-before") }

// readBackfillMarker returns the recorded boundary in millis, ok=false if backfill has
// not run in this AgentStateDir yet (a fresh install with nothing to write from Meta).
func readBackfillMarker() (int64, bool) {
	b, err := os.ReadFile(backfillMarkerPath())
	if err != nil {
		return 0, false
	}
	v, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// BackfillDone reports whether the genesis backfill has already run, so the one-time
// caller (internal/sessionx, from existing session Metas) can skip it on every later boot.
func BackfillDone() bool {
	_, ok := readBackfillMarker()
	return ok
}

// MarkBackfillDone stamps the boundary instant once, at the end of a successful genesis
// backfill. Idempotent: a second call before the ledger is ever erased just overwrites the
// same file with a later instant, which is harmless because BackfillDone gates the caller.
func MarkBackfillDone() {
	ms := clockNow().UnixMilli()
	if err := os.MkdirAll(dir(), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(backfillMarkerPath(), []byte(strconv.FormatInt(ms, 10)), 0o600)
}
