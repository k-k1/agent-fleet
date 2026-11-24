//go:build contract

package opencode

import (
	"net/http"
	"slices"
	"testing"
	"time"
)

// The signal daemonSettled rests on, against a real opencode 2 serve while it boots: whenever the
// product's read (modelsFromDaemon, plugins first) says settled, the configured provider's models
// are in the listing. Polled from right after the serve answers, so on 2.4.x the boot steps where
// the listing is empty or holds opencode's own models alone are sampled too (logged, not required:
// a fast boot can settle before the first poll).
func TestContractV2PluginListingMarksTheModelListingWhole(t *testing.T) {
	// Both limits carry a context and an output: a context-only one makes 2.2.5 and older refuse
	// the whole provider (TestContractV2ModelLimitContextOnly), and this test is about the signal.
	startV2Serve(t, http.NotFoundHandler(), map[string]any{
		"small": limitsModel("small", map[string]any{"context": 20000, "output": 2000}),
		"large": limitsModel("large", map[string]any{"context": 200000, "output": 32000}),
	}, nil)
	v := opencodeVersion(t)
	want := []string{"stub/small", "stub/large"}
	read := func() daemonRead {
		modelsMu.Lock()
		defer modelsMu.Unlock()
		return modelsFromDaemon()
	}
	var unsettled, partial, settled int
	for deadline := time.Now().Add(60 * time.Second); settled < 3; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("%s: the serve never read as settled in 60s (%d unsettled reads)", v, unsettled)
		}
		r := read()
		if !r.up {
			t.Fatalf("%s: the serve is not up", v)
		}
		whole := !slices.ContainsFunc(want, func(id string) bool { return !slices.Contains(r.ids, id) })
		switch {
		case r.settled && !whole:
			t.Fatalf("%s: read as settled with the configured models missing: %v", v, r.ids)
		case r.settled:
			settled++
		default:
			unsettled++
			if len(r.ids) > 0 && !whole {
				partial++
			}
		}
	}
	t.Logf("%s: %d unsettled reads (%d of them a partial listing) before 3 settled ones", v, unsettled, partial)
}
