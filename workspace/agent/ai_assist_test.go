package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/memreview"
)

func aiAssistResolutionCall(t *testing.T) []aiAssistResolutionRow {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/ai-assist/resolution", nil)
	w := httptest.NewRecorder()
	handleAIAssistResolution(w, r)
	// Drain the warms this request fired BEFORE returning. The response was written before
	// they were started, so the rows below are still the cold answer this endpoint's contract
	// promises — what the drain buys is that no warm survives into the next assertion or the
	// next test. Without it these tests shared chatx's availability cache with each other's
	// stragglers: 9 failures in 20 runs, plus a data race on the check seam.
	chatx.WaitForWarmsForTest()
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

// The 9 features tracked in aiAssistFeatures must be exactly the 9 the ledger already
// separates (usagex/ledger.go) — the point of decision 3 is that the settings screen and the
// usage view name the same thing.
func TestAIAssistResolutionListsAllNineFeatures(t *testing.T) {
	writeUIPrefs(t, `{}`)
	rows := aiAssistResolutionCall(t)
	want := map[string]bool{
		"title.session": false, "title.chat": false, "branch.suggest": false,
		"suggest.session": false, "suggest.chat": false, "suggest.edit": false,
		"plan.update": false, "translate.mirror": false, "memory.review": false,
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
		want := "unknown"
		if r.Feature == "memory.review" {
			want = "unavailable" // its one qualifying kind (claude) is confirmed unreachable, not cold
		}
		if r.Source != want || r.Kind != "" {
			t.Fatalf("%s: source=%q kind=%q, want %s/\"\" on a cold cache", r.Feature, r.Source, r.Kind, want)
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

	// No polling: aiAssistResolutionCall drains the first call's warms, so by the time the
	// second call runs, warming has demonstrably finished. A wall-clock loop here would only
	// re-introduce the question "did it not warm, or was I not patient enough" — and that
	// question is what a flake is made of.
	for _, r := range aiAssistResolutionCall(t) {
		if r.Source == "unknown" {
			t.Fatalf("%s still unknown after the first call's warming finished", r.Feature)
		}
	}
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

// The memory review runs only where tools can be denied, so its "currently uses" must not name a
// backend it would never run on: a pin on a backend that cannot qualify says "unavailable", and
// without one the answer is the first backend that can (claude), not the head of the shared order.
func TestAIAssistResolutionOfTheMemoryReviewFollowsTheDenyAllRule(t *testing.T) {
	row := func(t *testing.T) aiAssistResolutionRow {
		t.Helper()
		for _, r := range aiAssistResolutionCall(t) {
			if r.Feature == "memory.review" {
				return r
			}
		}
		t.Fatal("no memory.review row")
		return aiAssistResolutionRow{}
	}
	setup := func(t *testing.T, prefs string) {
		writeUIPrefs(t, prefs)
		for _, k := range []string{"claude", "codex", "opencode", "cursor", "agy"} {
			t.Cleanup(chatx.SetHeadlessAvailableForTest(k, true))
		}
	}

	t.Run("a pin that cannot run deny-all is unavailable", func(t *testing.T) {
		setup(t, `{"aiFeatureAgents":{"memory.review":"codex"}}`)
		if r := row(t); r.Source != chatx.OneShotSourceUnavailable || r.Kind != "" {
			t.Fatalf("row = %+v", r)
		}
	})
	t.Run("the shared order is walked past kinds that cannot qualify", func(t *testing.T) {
		setup(t, `{"aiAssistOrder":["codex","claude"]}`)
		if r := row(t); r.Kind != "claude" || r.Source != "default" {
			t.Fatalf("row = %+v", r)
		}
	})
	t.Run("enabled needs AF memory and a review mode", func(t *testing.T) {
		for prefs, want := range map[string]bool{
			`{}`:                         false,
			`{"agentMemory":true}`:       false,
			`{"memoryReview":"propose"}`: false,
			`{"agentMemory":true,"memoryReview":"propose"}`: true,
			`{"agentMemory":true,"memoryReview":"auto"}`:    false, // auto does not exist before P3
		} {
			setup(t, prefs)
			if got := row(t).Enabled; got != want {
				t.Errorf("%s: enabled = %v, want %v", prefs, got, want)
			}
		}
	})
}

// The memory review status and /ai-assist/resolution answer from one resolution: with no
// qualifying backend both say "unavailable", and the status carries a state only.
func TestMemoryReviewStatusAgreesWithTheResolutionWithoutABackend(t *testing.T) {
	writeUIPrefs(t, `{}`)
	forceAllHeadlessUnavailable(t)
	old1, old2 := memreview.ResolveBackend, memreview.WarmBackend
	memreview.ResolveBackend, memreview.WarmBackend = chatx.ResolveDenyAllCached, chatx.WarmDenyAllKind
	t.Cleanup(func() { memreview.ResolveBackend, memreview.WarmBackend = old1, old2 })

	var row aiAssistResolutionRow
	for _, r := range aiAssistResolutionCall(t) {
		if r.Feature == "memory.review" {
			row = r
		}
	}
	w := httptest.NewRecorder()
	memreview.HandleStatus(w, httptest.NewRequest(http.MethodGet, "/agents/memory-review", nil))
	var st memreview.StatusResponse
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil || w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if row.Source != "unavailable" || st.Backend != memreview.BackendUnavailable || st.BackendKind != "" {
		t.Fatalf("resolution %+v, status backend %q/%q", row, st.Backend, st.BackendKind)
	}
}
