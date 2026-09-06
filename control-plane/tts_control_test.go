package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

func testControlCfg() ttsControlCfg {
	return ttsControlCfg{
		interval:   30 * time.Second,
		window:     5 * time.Minute,
		startChars: 2000,
		idle:       30 * time.Minute,
		deadline:   5 * time.Minute,
		cooldown:   15 * time.Minute,
		offGrace:   time.Minute,
	}
}

// TestDecideEngineAction is the table test of the whole controller judgement (ADR 0070
// decisions 5 and 9). Every case is a rule that costs money or silence when it breaks.
func TestDecideEngineAction(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	base := ttsEngineSnapshot{mode: ttsModeOnDemand, lastDemand: ago(time.Minute)}

	with := func(f func(s *ttsEngineSnapshot)) ttsEngineSnapshot {
		s := base
		f(&s)
		return s
	}

	cases := []struct {
		name       string
		snap       ttsEngineSnapshot
		cfg        ttsControlCfg
		wantAction string
		wantReason string
	}{
		{
			"no service at all → never touch anything",
			with(func(s *ttsEngineSnapshot) { s.state = "none" }),
			testControlCfg(), ttsActionNone, ttsReasonNoService,
		},
		{
			"ondemand: demand over the threshold starts it",
			with(func(s *ttsEngineSnapshot) { s.state = "stopped"; s.windowChars = 2000 }),
			testControlCfg(), ttsActionStart, ttsReasonDemand,
		},
		{
			"ondemand: a Console chiming announcements never starts it",
			with(func(s *ttsEngineSnapshot) { s.state = "stopped"; s.windowChars = 120 }),
			testControlCfg(), ttsActionNone, ttsReasonBelow,
		},
		{
			"ondemand: idle window expired → stop",
			with(func(s *ttsEngineSnapshot) {
				s.state, s.desired, s.lastDemand = "running", 1, ago(31*time.Minute)
			}),
			testControlCfg(), ttsActionStop, ttsReasonIdle,
		},
		{
			"ondemand: somebody is still listening → leave it",
			with(func(s *ttsEngineSnapshot) {
				s.state, s.desired, s.lastDemand = "running", 1, ago(29*time.Minute)
			}),
			testControlCfg(), ttsActionNone, ttsReasonInUse,
		},
		{
			// A window shorter than a cold start would stop the service while it is still
			// starting, and the next sentence would start it again.
			"ondemand: the idle window is clamped to the start deadline",
			with(func(s *ttsEngineSnapshot) {
				s.state, s.desired, s.lastDemand, s.lastStart = "starting", 1, ago(3*time.Minute), ago(3*time.Minute)
			}),
			func() ttsControlCfg { c := testControlCfg(); c.idle = time.Minute; return c }(),
			ttsActionNone, ttsReasonInUse,
		},
		{
			"ondemand: idle applies to a service stuck at starting too",
			with(func(s *ttsEngineSnapshot) {
				s.state, s.desired, s.lastDemand, s.lastStart = "starting", 1, ago(40*time.Minute), ago(2*time.Minute)
			}),
			testControlCfg(), ttsActionStop, ttsReasonIdle,
		},
		{
			"ondemand: a start that never became running is a failure, not a retry",
			with(func(s *ttsEngineSnapshot) {
				s.state, s.desired, s.lastStart = "starting", 1, ago(6*time.Minute)
			}),
			testControlCfg(), ttsActionStop, ttsReasonDeadline,
		},
		{
			"ondemand: inside the cooldown a fresh demand buys nothing",
			with(func(s *ttsEngineSnapshot) {
				s.state, s.windowChars, s.failures, s.lastFailure = "stopped", 9000, 1, ago(10*time.Minute)
			}),
			testControlCfg(), ttsActionNone, ttsReasonCooldown,
		},
		{
			"ondemand: the cooldown doubles with the second failure",
			with(func(s *ttsEngineSnapshot) {
				s.state, s.windowChars, s.failures, s.lastFailure = "stopped", 9000, 2, ago(20*time.Minute)
			}),
			testControlCfg(), ttsActionNone, ttsReasonCooldown,
		},
		{
			"ondemand: once the cooldown is over, demand starts it again",
			with(func(s *ttsEngineSnapshot) {
				s.state, s.windowChars, s.failures, s.lastFailure = "stopped", 9000, 1, ago(16*time.Minute)
			}),
			testControlCfg(), ttsActionStart, ttsReasonDemand,
		},
		{
			"ondemand: no demand mark stored → stamp only, decide nothing",
			with(func(s *ttsEngineSnapshot) {
				s.state, s.desired, s.lastDemand = "running", 1, time.Time{}
			}),
			testControlCfg(), ttsActionNone, ttsReasonFirstPass,
		},
		{
			"ondemand: idle 0 means never stop",
			with(func(s *ttsEngineSnapshot) {
				s.state, s.desired, s.lastDemand = "running", 1, ago(10*time.Hour)
			}),
			func() ttsControlCfg { c := testControlCfg(); c.idle = 0; return c }(),
			ttsActionNone, ttsReasonNoIdleStop,
		},
		{
			"off: inside the undo window the desired count does not move",
			with(func(s *ttsEngineSnapshot) {
				s.state, s.desired, s.mode, s.modeAt = "running", 1, ttsModeOff, ago(20*time.Second)
			}),
			testControlCfg(), ttsActionNone, ttsReasonOffGrace,
		},
		{
			"off: past the undo window it stops",
			with(func(s *ttsEngineSnapshot) {
				s.state, s.desired, s.mode, s.modeAt = "running", 1, ttsModeOff, ago(2*time.Minute)
			}),
			testControlCfg(), ttsActionStop, ttsReasonAdminOff,
		},
		{
			// A CP that restarted inside the window has no mark; stopping is the safe
			// direction, because off means routing already goes to Polly.
			"off: with no recorded mode time there is no grace to wait out",
			with(func(s *ttsEngineSnapshot) {
				s.state, s.desired, s.mode = "running", 1, ttsModeOff
			}),
			testControlCfg(), ttsActionStop, ttsReasonAdminOff,
		},
		{
			"off: already stopped → nothing to do",
			with(func(s *ttsEngineSnapshot) { s.state, s.mode = "stopped", ttsModeOff }),
			testControlCfg(), ttsActionNone, ttsReasonOff,
		},
		{
			"on: a stopped engine is started whatever the demand",
			with(func(s *ttsEngineSnapshot) { s.state, s.mode, s.windowChars = "stopped", ttsModeOn, 0 }),
			testControlCfg(), ttsActionStart, ttsReasonAdminOn,
		},
		{
			"on: an idle engine is never stopped",
			with(func(s *ttsEngineSnapshot) {
				s.state, s.desired, s.mode, s.lastDemand = "running", 1, ttsModeOn, ago(10*time.Hour)
			}),
			testControlCfg(), ttsActionNone, ttsReasonOn,
		},
	}
	for _, c := range cases {
		action, reason := decideEngineAction(now, c.snap, c.cfg)
		if action != c.wantAction || reason != c.wantReason {
			t.Errorf("%s: got %s/%s, want %s/%s", c.name, action, reason, c.wantAction, c.wantReason)
		}
	}
}

// TestTTSDemandIntent — demand is what the request wanted, not what answered it.
func TestTTSDemandIntent(t *testing.T) {
	cases := []struct {
		name             string
		pref, lang, mode string
		want             bool
	}{
		{"auto Japanese", "auto", "ja", ttsModeOnDemand, true},
		{"unset provider, lang auto", "", "auto", ttsModeOnDemand, true},
		{"explicit polly never counts", "polly", "ja", ttsModeOnDemand, false},
		{"English through auto goes to Polly anyway", "auto", "en", ttsModeOnDemand, false},
		{"an explicit voicevox pin counts even in English", "voicevox", "en", ttsModeOnDemand, true},
		{"nothing counts while the mode is off", "auto", "ja", ttsModeOff, false},
		{"mode on counts the same as ondemand", "auto", "ja", ttsModeOn, true},
	}
	for _, c := range cases {
		if got := ttsDemandIntent(c.pref, c.lang, c.mode); got != c.want {
			t.Errorf("%s: ttsDemandIntent(%q,%q,%q) = %v, want %v", c.name, c.pref, c.lang, c.mode, got, c.want)
		}
	}
}

func TestTTSEngineMode(t *testing.T) {
	cases := []struct {
		stored  string
		managed bool
		want    string
	}{
		{"off", true, ttsModeOff},
		{"on", true, ttsModeOn},
		{"ondemand", true, ttsModeOnDemand},
		{"", true, ttsModeOnDemand},  // a managed engine defaults to on-demand
		{"", false, ttsModeOn},       // an engine somebody else runs is simply on
		{"garbage", true, ttsModeOn}, // a typo must not silence speech
	}
	for _, c := range cases {
		if got := ttsEngineMode(c.stored, c.managed); got != c.want {
			t.Errorf("ttsEngineMode(%q, managed=%v) = %q, want %q", c.stored, c.managed, got, c.want)
		}
	}
}

// TestTTSDemandWindow — the rolling window forgets, the timestamp is persisted at most
// once a minute, and a restarted CP reads the stored mark rather than "no demand".
func TestTTSDemandWindow(t *testing.T) {
	st := testSettingsStore(t)
	now := time.Date(2026, 9, 6, 9, 0, 0, 0, time.UTC)
	d := newTTSDemand(st, 5*time.Minute)
	d.now = func() time.Time { return now }

	d.record(t.Context(), 300)
	if got := d.chars(); got != 300 {
		t.Fatalf("chars = %d, want 300", got)
	}
	// Inside the window the characters add up; the store is written only once a minute.
	now = now.Add(30 * time.Second)
	d.record(t.Context(), 700)
	if got := d.chars(); got != 1000 {
		t.Errorf("chars after a second request = %d, want 1000", got)
	}
	if v, _ := st.GetSetting(t.Context(), ttsDemandSetting); v != strconv.FormatInt(now.Add(-30*time.Second).Unix(), 10) {
		t.Errorf("stored demand = %q, want the first request's time (writes are throttled to one a minute)", v)
	}
	// Past the window the old characters are gone, and the throttle has expired.
	now = now.Add(6 * time.Minute)
	d.record(t.Context(), 50)
	if got := d.chars(); got != 50 {
		t.Errorf("chars after the window rolled = %d, want 50", got)
	}
	if v, _ := st.GetSetting(t.Context(), ttsDemandSetting); v != strconv.FormatInt(now.Unix(), 10) {
		t.Errorf("stored demand = %q, want %d", v, now.Unix())
	}

	// A fresh process (empty memory) reads the stored mark: this is what keeps a CP
	// restart from stopping the engine out from under somebody who is listening.
	fresh := newTTSDemand(st, 5*time.Minute)
	if got := fresh.lastAt(t.Context()); !got.Equal(time.Unix(now.Unix(), 0)) {
		t.Errorf("lastAt on a fresh process = %v, want the stored %v", got, now)
	}
	if got := fresh.chars(); got != 0 {
		t.Errorf("chars on a fresh process = %d, want 0 (the window is not persisted)", got)
	}
}

// TestTTSSynthesizeRecordsDemand — the wiring, not the rule: a synthesis request that
// wanted the engine leaves a demand mark, and one pinned to Polly does not. Tested through
// the route because the failure this guards against is the handler reading the wrong field
// or not calling the counter at all, which no test of ttsDemandIntent can see.
func TestTTSSynthesizeRecordsDemand(t *testing.T) {
	clearTTSEnv(t)
	t.Setenv("AF_TTS_ECS_CONTROL_INTERVAL_SEC", "0") // no background goroutine in a test
	srv, _ := fakeVoicevox(t)
	st := testSettingsStore(t)
	f := &fakeTTSECS{svc: &ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}}
	orig := newTTSEngine
	newTTSEngine = func() *ttsEngineECS { return &ttsEngineECS{api: f, cluster: "c", service: "voicevox"} }
	t.Cleanup(func() { newTTSEngine = orig })

	mux := http.NewServeMux()
	registerTTSRoutes(mux, config{voicevoxURL: srv.URL, mgr: &manager{store: st}})
	post := func(body string) {
		t.Helper()
		mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/tts/synthesize", strings.NewReader(body)))
	}

	post(`{"text":"読み上げてほしい。","provider":"polly","pollyVoice":"Takumi"}`)
	if v, _ := st.GetSetting(t.Context(), ttsDemandSetting); v != "" {
		t.Errorf("a request pinned to Polly recorded demand (%q); it never wanted the engine", v)
	}

	post(`{"text":"読み上げてほしい。","voice":"3"}`)
	if v, _ := st.GetSetting(t.Context(), ttsDemandSetting); v == "" {
		t.Error("an auto request in Japanese should have recorded demand")
	}
}

// fakeTTSAudit records what the controller wrote to the ledger.
type fakeTTSAudit struct{ logs []store.AuditLog }

func (f *fakeTTSAudit) InsertAudit(_ context.Context, l store.AuditLog) error {
	f.logs = append(f.logs, l)
	return nil
}

func testSettingsStore(t *testing.T) store.Store {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(t.Context()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return st
}

// TestTTSControllerTick drives the shell end to end against the ECS fake and a fake
// engine: the first pass only stamps, demand starts the service, and both movements land
// in the audit ledger — an automatic charge nobody can explain is one nobody trusts.
func TestTTSControllerTick(t *testing.T) {
	srv, _ := fakeVoicevox(t)
	st := testSettingsStore(t)
	f := &fakeTTSECS{svc: &ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}}
	eng := &ttsEngineECS{api: f, cluster: "c", service: "voicevox"}
	now := time.Date(2026, 9, 6, 10, 0, 0, 0, time.UTC)
	eng.now = func() time.Time { return now }
	vv := &voicevoxProvider{base: srv.URL}
	demand := newTTSDemand(st, 5*time.Minute)
	demand.now = func() time.Time { return now }
	audit := &fakeTTSAudit{}
	c := newTTSController(eng, vv, demand, st, audit, testControlCfg())
	c.now = func() time.Time { return now }

	// Nothing stored yet: the first pass stamps and decides nothing.
	c.tick(t.Context())
	if len(f.desired) != 0 {
		t.Fatalf("first pass moved the desired count to %v, want no movement", f.desired)
	}
	if v, _ := st.GetSetting(t.Context(), ttsDemandSetting); v == "" {
		t.Fatal("first pass should have stamped the demand mark")
	}

	// One answer's worth of intent, and the next tick starts the engine.
	demand.record(t.Context(), 2100)
	now = now.Add(time.Second)
	c.tick(t.Context())
	if len(f.desired) != 1 || f.desired[0] != 1 {
		t.Fatalf("desired calls = %v, want one start", f.desired)
	}
	if len(audit.logs) != 1 || audit.logs[0].Target != "start" || audit.logs[0].ActorKind != "system" {
		t.Fatalf("audit = %+v, want one system start", audit.logs)
	}

	// The engine comes up: the controller warms it, and only then is it Ready.
	f.svc = &ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 1, RunningCount: 1}
	eng.invalidate()
	if vv.Ready(t.Context()) {
		t.Error("a running but unwarmed engine must not read as ready")
	}
	now = now.Add(time.Second)
	c.tick(t.Context())
	if !c.warmed() {
		t.Fatal("the controller should have warmed the engine up")
	}

	// Nobody wants it for longer than the idle window: it stops, with a reason.
	now = now.Add(31 * time.Minute)
	eng.invalidate()
	c.tick(t.Context())
	if len(f.desired) != 2 || f.desired[1] != 0 {
		t.Fatalf("desired calls = %v, want a stop after the idle window", f.desired)
	}
	if len(audit.logs) != 2 || audit.logs[1].Target != "stop" || audit.logs[1].Detail != ttsReasonIdle {
		t.Fatalf("audit = %+v, want a stop recorded as %q", audit.logs, ttsReasonIdle)
	}
	if c.warmed() {
		t.Error("a stopped engine must not stay warm")
	}
}

// TestTTSControllerStartDeadline — a start that never becomes running is given up on, the
// failure carries the reason ECS wrote into the service events, and the cooldown then
// refuses to buy another 2 GB pull straight away.
func TestTTSControllerStartDeadline(t *testing.T) {
	st := testSettingsStore(t)
	now := time.Date(2026, 9, 6, 10, 0, 0, 0, time.UTC)
	started := now.Add(-10 * time.Minute)
	f := &fakeTTSECS{svc: &ecstypes.Service{
		Status: aws.String("ACTIVE"), DesiredCount: 1, RunningCount: 0,
		Deployments: []ecstypes.Deployment{{Status: aws.String("PRIMARY"), CreatedAt: &started}},
		Events: []ecstypes.ServiceEvent{
			{Message: aws.String("(service voicevox) failed to place a task: CannotPullContainerError")},
		},
	}}
	eng := &ttsEngineECS{api: f, cluster: "c", service: "voicevox", now: func() time.Time { return now }}
	demand := newTTSDemand(st, 5*time.Minute)
	demand.now = func() time.Time { return now }
	demand.record(t.Context(), 5000)
	audit := &fakeTTSAudit{}
	c := newTTSController(eng, &voicevoxProvider{base: "http://127.0.0.1:1"}, demand, st, audit, testControlCfg())
	c.now = func() time.Time { return now }

	c.tick(t.Context())
	if len(f.desired) != 1 || f.desired[0] != 0 {
		t.Fatalf("desired calls = %v, want the failed start returned to 0", f.desired)
	}
	if len(audit.logs) != 1 || audit.logs[0].Target != "stop" {
		t.Fatalf("audit = %+v, want the failure recorded", audit.logs)
	}
	// The reason ECS gives is the only place a pull failure is written down.
	if want := "CannotPullContainerError"; !strings.Contains(audit.logs[0].Detail, want) {
		t.Errorf("audit detail = %q, want it to carry %q", audit.logs[0].Detail, want)
	}

	// Still plenty of demand, but the cooldown holds: no second start.
	f.svc = &ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}
	eng.invalidate()
	now = now.Add(time.Minute)
	c.tick(t.Context())
	if len(f.desired) != 1 {
		t.Errorf("desired calls = %v, want no restart inside the cooldown", f.desired)
	}

	// An administrator pressing a button clears the streak; the cooldown is there to stop
	// a retry loop, not to refuse a person.
	c.noteAdminAction()
	now = now.Add(time.Minute)
	eng.invalidate()
	c.tick(t.Context())
	if len(f.desired) != 2 || f.desired[1] != 1 {
		t.Errorf("desired calls = %v, want a start once the streak was cleared", f.desired)
	}
}
