package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/memberorigin/memberorigintest"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
)

// putPrefs sends a PUT /env/ui-prefs the way a session's shell would (AGENT_TOKEN only), or with
// the Control Plane's member-origin mark when signer is given.
func putPrefs(t *testing.T, signer *memberorigintest.Signer, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/env/ui-prefs", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer agent-token")
	req.Header.Set("Content-Type", "application/json")
	if signer != nil {
		signer.Sign(req, []byte(body))
	}
	rec := httptest.NewRecorder()
	handlePutUIPrefs(rec, req)
	return rec
}

func TestMemoryReviewNeedsMarkRule(t *testing.T) {
	for _, tc := range []struct {
		name          string
		before, after map[string]any
		want          bool
	}{
		{"nothing", map[string]any{}, map[string]any{}, false},
		{"propose appears", map[string]any{}, map[string]any{"memoryReview": "propose"}, true},
		{"propose appears with memory on", map[string]any{"agentMemory": true}, map[string]any{"agentMemory": true, "memoryReview": "propose"}, true},
		{"propose stays", map[string]any{"memoryReview": "propose"}, map[string]any{"memoryReview": "propose", "iconSet": "x"}, false},
		{"propose stays, memory already on", map[string]any{"agentMemory": true, "memoryReview": "propose"}, map[string]any{"agentMemory": true, "memoryReview": "propose"}, false},
		{"memory switched on under propose", map[string]any{"memoryReview": "propose"}, map[string]any{"memoryReview": "propose", "agentMemory": true}, true},
		{"memory switched on, review off", map[string]any{}, map[string]any{"agentMemory": true}, false},
		{"review turned off", map[string]any{"agentMemory": true, "memoryReview": "propose"}, map[string]any{"agentMemory": true}, false},
		{"memory turned off", map[string]any{"agentMemory": true, "memoryReview": "propose"}, map[string]any{"memoryReview": "propose"}, false},
		{"auto is not a mode yet", map[string]any{}, map[string]any{"agentMemory": true, "memoryReview": "auto"}, false},
		{"wrong types", map[string]any{}, map[string]any{"agentMemory": "true", "memoryReview": true}, false},
		{"auto switched on", map[string]any{"agentMemory": true, "memoryReview": "propose"}, map[string]any{"agentMemory": true, "memoryReview": "propose", "memoryReviewAuto": true}, true},
		{"auto switched on before the review", map[string]any{}, map[string]any{"memoryReviewAuto": true}, true},
		{"auto stays on", map[string]any{"memoryReviewAuto": true}, map[string]any{"memoryReviewAuto": true, "iconSet": "x"}, false},
		{"auto switched off", map[string]any{"memoryReviewAuto": true}, map[string]any{"memoryReviewAuto": false}, false},
		{"auto wrong type", map[string]any{}, map[string]any{"memoryReviewAuto": "true"}, false},
	} {
		if got := memoryReviewNeedsMark(tc.before, tc.after); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A session holds AGENT_TOKEN and can read and write the prefs; it must not be able to start the
// memory review that way. Every route into the enabled state needs the CP's mark; nothing else does.
func TestAPrefsWriteThatArmsTheMemoryReviewNeedsTheMemberMark(t *testing.T) {
	signer := memberorigintest.Install(t)
	for name, tc := range map[string]struct{ before, body string }{
		"mode on":              {`{}`, `{"memoryReview":"propose"}`},
		"mode on, memory on":   {`{}`, `{"agentMemory":true,"memoryReview":"propose"}`},
		"memory on under mode": {`{"memoryReview":"propose"}`, `{"agentMemory":true,"memoryReview":"propose"}`},
		"auto on":              {`{"agentMemory":true,"memoryReview":"propose"}`, `{"agentMemory":true,"memoryReview":"propose","memoryReviewAuto":true}`},
	} {
		writeUIPrefs(t, tc.before)
		rec := putPrefs(t, nil, tc.body)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "member_origin") {
			t.Errorf("%s without a mark: %d %s", name, rec.Code, rec.Body)
		}
		if uiprefs.MemoryReview() == "propose" && tc.before == `{}` {
			t.Errorf("%s: the refused write changed the prefs", name)
		}
		if rec := putPrefs(t, signer, tc.body); rec.Code != http.StatusOK {
			t.Errorf("%s with a mark: %d %s", name, rec.Code, rec.Body)
		}
	}

	// Writes that do not arm it pass without a mark: unrelated settings, turning it off, and
	// changing something else while it is already on.
	writeUIPrefs(t, `{"agentMemory":true,"memoryReview":"propose"}`)
	for name, body := range map[string]string{
		"unrelated":    `{"agentMemory":true,"memoryReview":"propose","iconSet":"vscode"}`,
		"review off":   `{"agentMemory":true}`,
		"memory off":   `{"memoryReview":"propose"}`,
		"everything":   `{}`,
		"auto is none": `{"agentMemory":true,"memoryReview":"auto"}`,
	} {
		if rec := putPrefs(t, nil, body); rec.Code != http.StatusOK {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
		writeUIPrefs(t, `{"agentMemory":true,"memoryReview":"propose"}`)
	}
	// A mark made for another body does not carry over.
	writeUIPrefs(t, `{}`)
	req := httptest.NewRequest(http.MethodPut, "/env/ui-prefs", strings.NewReader(`{"memoryReview":"propose"}`))
	req.Header.Set("Content-Type", "application/json")
	signer.Sign(req, []byte(`{"iconSet":"x"}`))
	rec := httptest.NewRecorder()
	handlePutUIPrefs(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatal("a mark signed for another body armed the review")
	}
}

func TestMuseContributorNeedsMarkRule(t *testing.T) {
	for _, tc := range []struct {
		name          string
		before, after map[string]any
		want          bool
	}{
		{"nothing", map[string]any{}, map[string]any{}, false},
		{"switched on", map[string]any{}, map[string]any{"museContributorMode": true}, true},
		{"switched on from false", map[string]any{"museContributorMode": false}, map[string]any{"museContributorMode": true}, true},
		{"stays on", map[string]any{"museContributorMode": true}, map[string]any{"museContributorMode": true, "iconSet": "x"}, false},
		{"switched off", map[string]any{"museContributorMode": true}, map[string]any{"museContributorMode": false}, false},
		{"key dropped", map[string]any{"museContributorMode": true}, map[string]any{}, false},
		{"wrong type", map[string]any{}, map[string]any{"museContributorMode": "true"}, false},
	} {
		if got := museContributorNeedsMark(tc.before, tc.after); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A session holds AGENT_TOKEN and can write the prefs; it must not be able to switch Muse Code's
// contributor mode on that way, since on, the vendor may use the member's conversations (#2535).
func TestAPrefsWriteThatTurnsMuseContributorModeOnNeedsTheMemberMark(t *testing.T) {
	signer := memberorigintest.Install(t)
	writeUIPrefs(t, `{}`)
	rec := putPrefs(t, nil, `{"museContributorMode":true}`)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "member_origin") {
		t.Fatalf("without a mark: %d %s", rec.Code, rec.Body)
	}
	if uiprefs.MuseContributorMode() {
		t.Fatal("the refused write switched contributor mode on")
	}
	if rec := putPrefs(t, signer, `{"museContributorMode":true}`); rec.Code != http.StatusOK {
		t.Fatalf("with a mark: %d %s", rec.Code, rec.Body)
	}
	if !uiprefs.MuseContributorMode() {
		t.Fatal("the marked write did not switch contributor mode on")
	}
	// Leaving it on while changing something else, and then turning it off, need no mark. In this
	// order: from off, the first body would be a switch on.
	for _, body := range []string{`{"museContributorMode":true,"iconSet":"vscode"}`, `{"museContributorMode":false}`} {
		if rec := putPrefs(t, nil, body); rec.Code != http.StatusOK {
			t.Errorf("%s: %d %s", body, rec.Code, rec.Body)
		}
	}
}
