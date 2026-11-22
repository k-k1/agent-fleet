package memoafter

// A start time a queue message carries (ADR 0137, addendum 2026-11-22, #4777): the dispatcher does
// not claim the message, by itself or on "fire now", before it (Waits). Unlike a run's hand-back
// (memo.not_before) it is content: whoever wrote the message sets it, a release is bound to it, and
// a release does not clear it.

import (
	"fmt"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// StartAfterMaxAhead bounds how far ahead a start time may be. A message parked for longer than
// this is a reminder, not queued work.
const StartAfterMaxAhead = 60 * 24 * time.Hour

// ParseStartAfter checks a start time a create or an update gave and returns the column's value: ""
// for an empty one or one that has already passed at now (it would wait for nothing), the time in
// DeliveryTS's fixed width otherwise. The offset is required: a time without one is the likeliest
// way for a model to land nine hours off, so it is refused rather than guessed. The error is
// worded for the caller.
func ParseStartAfter(raw string, now time.Time) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	// time.RFC3339 requires the offset ("Z" or "+09:00"); a fraction of a second is accepted too.
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return "", fmt.Errorf("startAfter must be an RFC 3339 time with an offset, such as 2026-12-04T09:00:00+09:00")
	}
	if !t.After(now) {
		return "", nil
	}
	if t.After(now.Add(StartAfterMaxAhead)) {
		return "", fmt.Errorf("startAfter must be at most %d days from now", int(StartAfterMaxAhead/(24*time.Hour)))
	}
	return store.DeliveryTS(t), nil
}

// StartAfterHashParts is appended to the content hash of a message that carries a start time. A
// message without one hashes as it did before the column existed, so no release already recorded
// changes.
func StartAfterHashParts(m store.Memo) []string {
	if m.StartAfter == "" {
		return nil
	}
	return []string{"start_after", m.StartAfter}
}
