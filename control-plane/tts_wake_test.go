package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// newWakeAPI wires a wake endpoint over a fake ECS service. post() is called directly:
// the route wrapper only resolves the caller's identity, which is tested elsewhere.
func newWakeAPI(t *testing.T, svc *ecstypes.Service, engineURL string) (*ttsWakeAPI, *fakeTTSECS, store.Store) {
	t.Helper()
	st := testSettingsStore(t)
	f := &fakeTTSECS{svc: svc}
	eng := &engineECS{api: f, cluster: "c", service: "voicevox"}
	vv := &voicevoxProvider{base: engineURL}
	demand := newEngineDemand(st, ttsEngineSettings().demandAt, 5*time.Minute)
	ctrl := newTTSController(eng, vv, demand, st, nil, nil, testControlCfg())
	mgr := &manager{store: st}
	return &ttsWakeAPI{
		memberAuth: memberAuth{mgr},
		status:     ttsStatusView{vv: vv, pl: newPollyProvider(), eng: eng, settings: st},
		eng:        eng, ctrl: ctrl, demand: demand,
	}, f, st
}

func wakePost(t *testing.T, a *ttsWakeAPI, who string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	a.post(rec, httptest.NewRequest("POST", "/api/tts/wake", nil), store.Identity{ID: who})
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

// TestTTSWakeStarts — the explicit trigger of ADR 0070 decision 4. A member who wants the
// voice for their own notifications never accumulates 2,000 characters in five minutes,
// so without this they could never have it at all.
func TestTTSWakeStarts(t *testing.T) {
	clearTTSEnv(t)
	srv, _ := fakeVoicevox(t)
	a, f, st := newWakeAPI(t, &ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}, srv.URL)

	rec, out := wakePost(t, a, "u1")
	if rec.Code != http.StatusOK {
		t.Fatalf("wake = %d (%s)", rec.Code, rec.Body.String())
	}
	if out["started"] != true {
		t.Errorf("started = %v, want true", out["started"])
	}
	if len(f.desired) != 1 || f.desired[0] != 1 {
		t.Errorf("desired calls = %v, want a single start", f.desired)
	}
	// The press is what says "somebody is listening": without the stamp the controller's
	// very next tick reads a stale demand mark and stops what was just started.
	if v, _ := st.GetSetting(t.Context(), ttsEngineSettings().demandAt); v == "" {
		t.Error("wake must refresh the demand clock")
	}
	// It spends money, so it is in the ledger.
	logs, err := st.ListAuditByTenant(t.Context(), "", 10)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	found := false
	for _, l := range logs {
		if l.Action == "tts.engine.wake" && l.Target == "start" && l.ActorID == "u1" {
			found = true
		}
	}
	if !found {
		t.Errorf("no tts.engine.wake audit row: %+v", logs)
	}
}

// TestTTSWakeIdempotent — pressing it while the engine is already coming refreshes the
// clock and nothing else. Four presses must not queue four starts.
func TestTTSWakeIdempotent(t *testing.T) {
	clearTTSEnv(t)
	srv, _ := fakeVoicevox(t)
	a, f, _ := newWakeAPI(t, &ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 1, RunningCount: 0}, srv.URL)

	rec, out := wakePost(t, a, "u1")
	if rec.Code != http.StatusOK || out["started"] != false {
		t.Errorf("wake while starting: %d started=%v, want 200/false", rec.Code, out["started"])
	}
	if len(f.desired) != 0 {
		t.Errorf("desired calls = %v, want none (it is already on its way)", f.desired)
	}
}

// TestTTSWakeRefusals — the two answers that are not "no engine came": an engine nobody
// here can start, and speech an administrator switched off. A member cannot overrule the
// second, and starting a task whose output routing is off would buy silence.
func TestTTSWakeRefusals(t *testing.T) {
	clearTTSEnv(t)
	srv, _ := fakeVoicevox(t)

	a, _, st := newWakeAPI(t, &ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}, srv.URL)
	if err := st.SetSetting(t.Context(), ttsEngineSetting, engineModeOff); err != nil {
		t.Fatalf("set setting: %v", err)
	}
	if rec, _ := wakePost(t, a, "u1"); rec.Code != http.StatusConflict {
		t.Errorf("wake while off = %d, want 409", rec.Code)
	}

	// No ECS engine at all: not this request's failure, and not something to retry.
	unmanaged := &ttsWakeAPI{memberAuth: memberAuth{&manager{store: st}}}
	if rec, _ := wakePost(t, unmanaged, "u1"); rec.Code != http.StatusNotImplemented {
		t.Errorf("wake without a managed engine = %d, want 501", rec.Code)
	}
}

// TestTTSWakeRateLimit — the button sits in front of somebody watching a 70-second cold
// start, and there is nothing else to do while waiting. Per member, so one impatient
// person cannot exhaust it for everyone.
func TestTTSWakeRateLimit(t *testing.T) {
	clearTTSEnv(t)
	srv, _ := fakeVoicevox(t)
	a, _, _ := newWakeAPI(t, &ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 1, RunningCount: 1}, srv.URL)
	now := time.Date(2026, 9, 7, 10, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	for i := 0; i < ttsWakeBurst; i++ {
		if rec, _ := wakePost(t, a, "u1"); rec.Code != http.StatusOK {
			t.Fatalf("press %d = %d, want 200", i+1, rec.Code)
		}
	}
	if rec, _ := wakePost(t, a, "u1"); rec.Code != http.StatusTooManyRequests {
		t.Errorf("press %d = %d, want 429", ttsWakeBurst+1, rec.Code)
	}
	// Another member is unaffected — the limit is per person, not per deployment.
	if rec, _ := wakePost(t, a, "u2"); rec.Code != http.StatusOK {
		t.Errorf("another member = %d, want 200", rec.Code)
	}
	// And the window rolls.
	now = now.Add(ttsWakeWindow + time.Second)
	if rec, _ := wakePost(t, a, "u1"); rec.Code != http.StatusOK {
		t.Errorf("after the window = %d, want 200", rec.Code)
	}
}
