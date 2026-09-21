package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
)

func aiAssistResolutionCall(t *testing.T) []aiAssistResolutionRow {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/ai-assist/resolution", nil)
	w := httptest.NewRecorder()
	handleAIAssistResolution(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Features []aiAssistResolutionRow `json:"features"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, w.Body.String())
	}
	return resp.Features
}

// The 8 features tracked in aiAssistFeatures must be exactly the 8 the ledger already
// separates (usagex/ledger.go) — the point of decision 3 is that the settings screen and the
// usage view name the same thing.
func TestAIAssistResolutionListsAllEightFeatures(t *testing.T) {
	writeUIPrefs(t, `{}`)
	rows := aiAssistResolutionCall(t)
	want := map[string]bool{
		"title.session": false, "title.chat": false, "branch.suggest": false,
		"suggest.session": false, "suggest.chat": false, "suggest.edit": false,
		"plan.update": false, "translate.mirror": false,
	}
	if len(rows) != len(want) {
		t.Fatalf("got %d rows, want %d: %+v", len(rows), len(want), rows)
	}
	for _, r := range rows {
		if _, ok := want[r.Feature]; !ok {
			t.Fatalf("unexpected feature %q", r.Feature)
		}
		want[r.Feature] = true
	}
	for f, seen := range want {
		if !seen {
			t.Fatalf("feature %q missing from the resolution", f)
		}
	}
}

// forceAllHeadlessUnavailable pins every candidate kind's availability to a KNOWN false, rather
// than relying on the cache being untouched: the response path's own background warming
// (103-impl-review 重大3, chatx.WarmOneShotKind) means chatx's cache can no longer be assumed
// empty just because this test hasn't touched it — an earlier test's straggler warm goroutine
// (or this handler's own, from a previous call in the same test) could otherwise leave a REAL
// answer behind. A known-false answer produces the same "unknown" resolution as a truly empty
// cache (oneShotKindCached only reports ok=true on a known TRUE), so this is deterministic
// either way.
func forceAllHeadlessUnavailable(t *testing.T) {
	t.Helper()
	for _, k := range []string{"claude", "codex", "opencode", "cursor", "agy"} {
		t.Cleanup(chatx.SetHeadlessAvailableForTest(k, false))
	}
}

// A cold availability cache (a fresh process, or the 1-minute window elapsed) must answer
// "unknown" rather than start a CLI to find out (docs/log/103 §103.8-3) — the whole reason
// this endpoint exists as its own code path instead of just calling chatx.ResolveOneShot.
func TestAIAssistResolutionColdCacheIsUnknown(t *testing.T) {
	writeUIPrefs(t, `{}`)
	forceAllHeadlessUnavailable(t)
	for _, r := range aiAssistResolutionCall(t) {
		if r.Source != "unknown" || r.Kind != "" {
			t.Fatalf("%s: source=%q kind=%q, want unknown/\"\" on a cold cache", r.Feature, r.Source, r.Kind)
		}
	}
}

// TestAIAssistResolutionWarmsAfterAnUnknownAnswer is 103-impl-review 重大3: the endpoint must
// not be the reason a settings tab polls "unknown" forever. Claude's entry is CLEARED (not
// pinned false) so it is genuinely cold: the first call answers unknown without starting a CLI
// (the request's own contract), and the background chatx.WarmOneShotKind it fires afterward
// resolves it, so a later poll (what this waits for) has a real answer.
//
// ⚠️ An earlier version of this test polled in a tight 20ms loop against the REAL
// headlessAgentAvailable("claude") -> `claude auth status`. Before chatx grew an in-flight
// guard (headlessAvailInFlight), every one of those polls — plus every one of the up to 8
// WarmOneShotKind goroutines the handler fires per unknown response — started its OWN `claude`
// process with no cap. Measured: ~490 real `claude` processes, ~25GiB RSS, OOM-killed the
// container. The guard now caps concurrent execs to one per cold kind regardless of how this
// test polls, but this version ALSO never touches the real CLI at all
// (chatx.SetHeadlessAvailCheckForTest stands in for it) and bounds every wait with a fixed,
// short deadline — belt and braces, because a test that can loop against something that can
// start a process is exactly the shape that caused the incident.
func TestAIAssistResolutionWarmsAfterAnUnknownAnswer(t *testing.T) {
	writeUIPrefs(t, `{}`)
	for _, k := range []string{"codex", "opencode", "cursor", "agy"} {
		t.Cleanup(chatx.SetHeadlessAvailableForTest(k, false))
	}
	t.Cleanup(chatx.ClearHeadlessAvailableForTest("claude"))
	t.Cleanup(chatx.SetHeadlessAvailCheckForTest(func(kind string) bool { return kind == "claude" }))

	first := aiAssistResolutionCall(t)
	for _, r := range first {
		if r.Source != "unknown" {
			t.Fatalf("first call: %s already answered (%q) — the test's own forcing did not take", r.Feature, r.Source)
		}
	}

	const pollInterval = 50 * time.Millisecond
	const maxPolls = 20 // 1s ceiling — the fake check is instant, so warming lands within one poll
	for i := 0; i < maxPolls; i++ {
		warmed := true
		for _, r := range aiAssistResolutionCall(t) {
			if r.Source == "unknown" {
				warmed = false
				break
			}
		}
		if warmed {
			return
		}
		time.Sleep(pollInterval)
	}
	t.Fatalf("no feature warmed up within %v of the first unknown answer", time.Duration(maxPolls)*pollInterval)
}

// Each row's enabled flag is that feature's OWN gate, reachable and distinguishable per
// feature — the failure mode §103.3-3 found (one shared key silently answering for two
// features) would show up here as two rows moving together.
func TestAIAssistResolutionEnabledPerFeature(t *testing.T) {
	writeUIPrefs(t, `{"mirrorTranslateEnabled":false,"planUpdateEnabled":false}`)
	got := map[string]bool{}
	for _, r := range aiAssistResolutionCall(t) {
		got[r.Feature] = r.Enabled
	}
	if got["translate.mirror"] || got["plan.update"] {
		t.Fatalf("disabled features reported enabled: %+v", got)
	}
	for _, f := range []string{"title.session", "title.chat", "branch.suggest", "suggest.session", "suggest.chat", "suggest.edit"} {
		if !got[f] {
			t.Fatalf("%s must stay enabled (default ON), got false: %+v", f, got)
		}
	}
}
