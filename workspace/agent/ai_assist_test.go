package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
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

// A cold availability cache (a fresh process, or the 1-minute window elapsed) must answer
// "unknown" rather than start a CLI to find out (docs/log/103 §103.8-3) — the whole reason
// this endpoint exists as its own code path instead of just calling chatx.ResolveOneShot.
func TestAIAssistResolutionColdCacheIsUnknown(t *testing.T) {
	writeUIPrefs(t, `{}`)
	for _, r := range aiAssistResolutionCall(t) {
		if r.Source != "unknown" || r.Kind != "" {
			t.Fatalf("%s: source=%q kind=%q, want unknown/\"\" on a cold cache", r.Feature, r.Source, r.Kind)
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
