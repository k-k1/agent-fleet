package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// --- pure rotation helpers ------------------------------------------------------

func TestParseRotateDuration(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"", 0, false},
		{"7d", 7 * 24 * time.Hour, true},
		{"12h", 12 * time.Hour, true},
		{"30m", 30 * time.Minute, true},
		{"0d", 0, false},   // non-positive day count rejected
		{"abc", 0, false},  // unparseable
		{"1d2h", 0, false}, // the "d" suffix path only accepts a pure day count
	}
	for _, c := range cases {
		got, ok := parseRotateDuration(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("parseRotateDuration(%q) = (%v,%v), want (%v,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestCalendarCrossed(t *testing.T) {
	base := time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC) // Wed 2026-07-22, ISO week 30
	cases := []struct {
		name     string
		slot     time.Time
		calendar string
		want     bool
	}{
		{"daily same day", base.Add(2 * time.Hour), "daily", false},
		{"daily next day", base.Add(20 * time.Hour), "daily", true},
		{"weekly same week (Fri)", time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC), "weekly", false},
		{"weekly next week (Mon)", time.Date(2026, 7, 27, 9, 0, 0, 0, time.UTC), "weekly", true},
		{"monthly same month", time.Date(2026, 7, 31, 10, 0, 0, 0, time.UTC), "monthly", false},
		{"monthly next month", time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), "monthly", true},
		{"unknown granularity", base.Add(100 * time.Hour), "yearly", false},
	}
	for _, c := range cases {
		if got := calendarCrossed(base, c.slot, c.calendar); got != c.want {
			t.Errorf("%s: calendarCrossed = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestRotationDue(t *testing.T) {
	loc := time.UTC
	started := "2026-07-20T09:00:00Z"
	slot := time.Date(2026, 7, 27, 9, 0, 0, 0, time.UTC) // 7 days later, a Monday
	cases := []struct {
		name     string
		rotation string
		runCount int
		want     bool
	}{
		{"empty never rotates", "", 100, false},
		{"every_runs not reached", `{"every_runs":5}`, 3, false},
		{"every_runs reached", `{"every_runs":5}`, 5, true},
		{"after not elapsed", `{"after":"30d"}`, 0, false},
		{"after elapsed", `{"after":"7d"}`, 0, true},
		{"calendar weekly crossed", `{"calendar":"weekly"}`, 0, true},
		{"calendar monthly not crossed", `{"calendar":"monthly"}`, 0, false},
		{"OR: only every_runs hits", `{"every_runs":1,"after":"30d","calendar":"monthly"}`, 1, true},
		{"OR: none hits", `{"every_runs":50,"after":"30d","calendar":"monthly"}`, 2, false},
	}
	for _, c := range cases {
		sch := store.Schedule{Rotation: c.rotation, ReuseStartedAt: started, ReuseRunCount: c.runCount}
		if got := rotationDue(sch, slot, loc); got != c.want {
			t.Errorf("%s: rotationDue = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestRotationDueNoBaseline(t *testing.T) {
	// A period/calendar trigger cannot fire without a reuse_started_at baseline; only
	// every_runs (which needs no time baseline) can.
	slot := time.Date(2026, 7, 27, 9, 0, 0, 0, time.UTC)
	if rotationDue(store.Schedule{Rotation: `{"after":"1h"}`, ReuseStartedAt: ""}, slot, time.UTC) {
		t.Error("after should not trigger without a baseline")
	}
	if !rotationDue(store.Schedule{Rotation: `{"every_runs":2}`, ReuseStartedAt: "", ReuseRunCount: 2}, slot, time.UTC) {
		t.Error("every_runs should trigger regardless of baseline")
	}
}

func TestValidateRotation(t *testing.T) {
	good := []string{"", `{"every_runs":10}`, `{"after":"7d"}`, `{"calendar":"weekly"}`,
		`{"every_runs":5,"after":"12h","calendar":"daily"}`, `{"context_pct":80}`}
	for _, s := range good {
		if err := validateRotation(s); err != nil {
			t.Errorf("validateRotation(%q) unexpected err: %v", s, err)
		}
	}
	bad := []string{`{`, `{"every_runs":-1}`, `{"after":"soon"}`, `{"calendar":"hourly"}`}
	for _, s := range bad {
		if err := validateRotation(s); err == nil {
			t.Errorf("validateRotation(%q) expected err, got nil", s)
		}
	}
}

func TestReusePolicyDefaults(t *testing.T) {
	if reuseOverlap(store.Schedule{}) != "skip" {
		t.Error("overlap default should be skip")
	}
	if reuseOverlap(store.Schedule{OverlapPolicy: "queue"}) != "queue" {
		t.Error("overlap should honor explicit value")
	}
	if reuseMissingPolicy(store.Schedule{}) != "recreate" {
		t.Error("missing default should be recreate")
	}
	if reuseMissingPolicy(store.Schedule{MissingTargetPolicy: "fail"}) != "fail" {
		t.Error("missing should honor explicit value")
	}
	if !sessionBusy("working") || !sessionBusy("question") || sessionBusy("idle") {
		t.Error("sessionBusy mismatch")
	}
	sessions := []sessionWire{{Name: "a"}, {Name: "b"}}
	if findSessionByName(sessions, "b") == nil || findSessionByName(sessions, "x") != nil || findSessionByName(sessions, "") != nil {
		t.Error("findSessionByName mismatch")
	}
}

// reuseSendBody must request delivery confirmation (docs/log/38 配達検証): without
// confirm the Agent answers 200 on mere keystroke delivery, and a swallowed prompt
// records a bogus "fired" (the 2026-07-24 sbk7oej recurrence).
func TestReuseSendBodyRequestsConfirm(t *testing.T) {
	var body struct {
		Prompt   string `json:"prompt"`
		ReportTo string `json:"report_to"`
		Confirm  bool   `json:"confirm"`
		Source   string `json:"source"`
	}
	raw := reuseSendBody(store.Schedule{Prompt: "/scout", OwnerConv: "conv-1", Report: true}, time.Date(2026, 7, 24, 11, 0, 0, 0, time.UTC))
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if body.Prompt != "/scout" || body.ReportTo != "conv-1" {
		t.Fatalf("body mismatch: %+v", body)
	}
	// report=false (the default) must NOT carry a report_to — the fire runs silently.
	raw = reuseSendBody(store.Schedule{Prompt: "/scout", OwnerConv: "conv-1"}, time.Date(2026, 7, 24, 11, 0, 0, 0, time.UTC))
	_ = json.Unmarshal(raw, &body)
	if body.ReportTo != "" {
		t.Fatalf("report_to = %q, want empty when report is off (default)", body.ReportTo)
	}
	if !body.Confirm {
		t.Fatal("reuse send must set confirm:true — keystroke-200 is not delivery")
	}
	if body.Source != "schedule" {
		t.Fatalf("source = %q, want schedule (mirror badge, docs/log/38)", body.Source)
	}
	raw = reuseSendBody(store.Schedule{Prompt: "/scout", OwnerConv: "conv-1", ManualFirePending: true}, time.Date(2026, 7, 24, 11, 0, 0, 0, time.UTC))
	_ = json.Unmarshal(raw, &body)
	if body.Source != "schedule-manual" {
		t.Fatalf("manual-fire source = %q, want schedule-manual", body.Source)
	}
}

// --- fireReuse integration (fake Agent) -----------------------------------------

// fakeAgent records the create/input/archive/start calls a reuse fire makes and serves a
// configurable session list, so fireReuse can be driven end-to-end without a real Agent.
type fakeAgent struct {
	mu        sync.Mutex
	sessions  []sessionWire
	newName   string // name returned by POST /sessions
	inputs    []string
	archived  []string
	created   int
	startedAt []string
	// unready names report alive-but-not-input-ready on /status (a booting/zombie pane),
	// so awaitSessionReady never clears for them — models the sbk7oej silent-drop.
	unready map[string]bool
}

func (a *fakeAgent) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		defer a.mu.Unlock()
		p := r.URL.Path
		switch {
		case r.Method == http.MethodGet && p == "/sessions":
			_ = json.NewEncoder(w).Encode(map[string]any{"sessions": a.sessions})
		case r.Method == http.MethodGet && strings.HasSuffix(p, "/status"):
			name := sessionSeg(p)
			alive := false
			for _, s := range a.sessions {
				if s.Name == name && s.Alive {
					alive = true
				}
			}
			for _, s := range a.startedAt {
				if s == name { // a resumed session reports alive on the next status read
					alive = true
				}
			}
			ready := alive && !a.unready[name]
			_ = json.NewEncoder(w).Encode(map[string]any{"alive": alive, "ready": ready})
		case r.Method == http.MethodPost && p == "/sessions":
			a.created++
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"name": a.newName})
		case r.Method == http.MethodPost && strings.HasSuffix(p, "/input"):
			a.inputs = append(a.inputs, sessionSeg(p))
		case r.Method == http.MethodPost && strings.HasSuffix(p, "/archive"):
			a.archived = append(a.archived, sessionSeg(p))
		case r.Method == http.MethodPost && strings.HasSuffix(p, "/start"):
			a.startedAt = append(a.startedAt, sessionSeg(p))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

// sessionSeg extracts the {name} from /sessions/{name}/verb.
func sessionSeg(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) >= 2 {
		return parts[1]
	}
	return ""
}

func newReuseFixture(t *testing.T, a *fakeAgent, sch store.Schedule) (*wakeFirer, *resolved, *store.SQL, context.Context) {
	t.Helper()
	st, ctx := newSchedTestStore(t)
	sch.MembershipID, sch.TenantID = "m1", "default"
	sch.CreatedAt, sch.UpdatedAt = store.NowTS(), store.NowTS()
	if err := st.CreateSchedule(ctx, sch); err != nil {
		t.Fatalf("seed schedule: %v", err)
	}
	srv := httptest.NewServer(a.handler())
	t.Cleanup(srv.Close)
	f := &wakeFirer{mgr: &manager{store: st}}
	res := &resolved{rt: stubRuntime{endpoint: srv.URL}}
	return f, res, st, ctx
}

func TestFireReusePinnedSendsToExisting(t *testing.T) {
	a := &fakeAgent{sessions: []sessionWire{{Name: "my-sess", Alive: true, State: "idle"}}}
	sch := store.Schedule{ID: "sch_p", SessionMode: "reuse", ReuseTarget: "my-sess", OwnerConv: "conv1", Prompt: "go"}
	f, res, st, ctx := newReuseFixture(t, a, sch)

	status, _, err := f.fireReuse(ctx, res, sch, time.Now().UTC())
	if err != nil || status != "fired" {
		t.Fatalf("status=%q err=%v, want fired/nil", status, err)
	}
	if len(a.inputs) != 1 || a.inputs[0] != "my-sess" {
		t.Fatalf("inputs=%v, want [my-sess]", a.inputs)
	}
	if a.created != 0 {
		t.Errorf("should not create when target exists (created=%d)", a.created)
	}
	got, _, _ := st.GetSchedule(ctx, "sch_p")
	if got.ReuseSession != "my-sess" || got.ReuseRunCount != 1 {
		t.Errorf("ledger = %q/%d, want my-sess/1", got.ReuseSession, got.ReuseRunCount)
	}
}

func TestFireReusePinnedMissingRecreate(t *testing.T) {
	a := &fakeAgent{sessions: nil, newName: "fresh-1"}
	sch := store.Schedule{ID: "sch_r", SessionMode: "reuse", ReuseTarget: "gone", MissingTargetPolicy: "recreate", Prompt: "go"}
	f, res, st, ctx := newReuseFixture(t, a, sch)

	status, _, err := f.fireReuse(ctx, res, sch, time.Now().UTC())
	if err != nil || status != "fired" {
		t.Fatalf("status=%q err=%v, want fired/nil", status, err)
	}
	if a.created != 1 {
		t.Fatalf("created=%d, want 1", a.created)
	}
	got, _, _ := st.GetSchedule(ctx, "sch_r")
	if got.ReuseSession != "fresh-1" || got.ReuseRunCount != 1 {
		t.Errorf("ledger = %q/%d, want fresh-1/1", got.ReuseSession, got.ReuseRunCount)
	}
}

func TestFireReusePinnedMissingFail(t *testing.T) {
	a := &fakeAgent{sessions: nil}
	sch := store.Schedule{ID: "sch_f", SessionMode: "reuse", ReuseTarget: "gone", MissingTargetPolicy: "fail", Prompt: "go"}
	f, res, _, ctx := newReuseFixture(t, a, sch)

	status, _, err := f.fireReuse(ctx, res, sch, time.Now().UTC())
	if err != nil || status != "skipped_target_missing" {
		t.Fatalf("status=%q err=%v, want skipped_target_missing/nil", status, err)
	}
	if a.created != 0 || len(a.inputs) != 0 {
		t.Errorf("fail policy must not create/send (created=%d inputs=%v)", a.created, a.inputs)
	}
}

func TestFireReuseManagedFirstFireCreates(t *testing.T) {
	a := &fakeAgent{sessions: nil, newName: "managed-1"}
	sch := store.Schedule{ID: "sch_m", SessionMode: "reuse", Prompt: "go"} // reuse_target empty => managed
	f, res, st, ctx := newReuseFixture(t, a, sch)

	status, _, err := f.fireReuse(ctx, res, sch, time.Now().UTC())
	if err != nil || status != "fired" {
		t.Fatalf("status=%q err=%v", status, err)
	}
	if a.created != 1 {
		t.Fatalf("created=%d, want 1", a.created)
	}
	got, _, _ := st.GetSchedule(ctx, "sch_m")
	if got.ReuseSession != "managed-1" || got.ReuseRunCount != 1 {
		t.Errorf("ledger = %q/%d", got.ReuseSession, got.ReuseRunCount)
	}
}

func TestFireReuseManagedRotates(t *testing.T) {
	a := &fakeAgent{
		sessions: []sessionWire{{Name: "old-sess", Alive: true, State: "idle"}},
		newName:  "new-sess",
	}
	// every_runs=1 with run_count already at 1 => the next fire rotates.
	sch := store.Schedule{
		ID: "sch_rot", SessionMode: "reuse", Prompt: "go",
		ReuseSession: "old-sess", ReuseRunCount: 1, ReuseStartedAt: "2026-07-20T00:00:00Z",
		Rotation: `{"every_runs":1}`,
	}
	f, res, st, ctx := newReuseFixture(t, a, sch)

	status, _, err := f.fireReuse(ctx, res, sch, time.Now().UTC())
	if err != nil || status != "fired_rotated" {
		t.Fatalf("status=%q err=%v, want fired_rotated/nil", status, err)
	}
	if len(a.archived) != 1 || a.archived[0] != "old-sess" {
		t.Errorf("archived=%v, want [old-sess]", a.archived)
	}
	if a.created != 1 {
		t.Errorf("created=%d, want 1 (new session)", a.created)
	}
	got, _, _ := st.GetSchedule(ctx, "sch_rot")
	if got.ReuseSession != "new-sess" || got.ReuseRunCount != 1 {
		t.Errorf("ledger = %q/%d, want new-sess/1", got.ReuseSession, got.ReuseRunCount)
	}
}

func TestFireReuseOverlapSkip(t *testing.T) {
	a := &fakeAgent{sessions: []sessionWire{{Name: "busy", Alive: true, State: "working"}}}
	sch := store.Schedule{ID: "sch_o", SessionMode: "reuse", ReuseTarget: "busy", OverlapPolicy: "skip", Prompt: "go"}
	f, res, st, ctx := newReuseFixture(t, a, sch)

	status, _, err := f.fireReuse(ctx, res, sch, time.Now().UTC())
	if err != nil || status != "skipped_overlap" {
		t.Fatalf("status=%q err=%v, want skipped_overlap/nil", status, err)
	}
	if len(a.inputs) != 0 {
		t.Errorf("skip overlap must not send (inputs=%v)", a.inputs)
	}
	// Ledger untouched on a skip.
	got, _, _ := st.GetSchedule(ctx, "sch_o")
	if got.ReuseRunCount != 0 {
		t.Errorf("run count advanced on skip: %d", got.ReuseRunCount)
	}
}

func TestFireReuseOverlapQueueSends(t *testing.T) {
	a := &fakeAgent{sessions: []sessionWire{{Name: "busy", Alive: true, State: "working"}}}
	sch := store.Schedule{ID: "sch_q", SessionMode: "reuse", ReuseTarget: "busy", OverlapPolicy: "queue", Prompt: "go"}
	f, res, _, ctx := newReuseFixture(t, a, sch)

	status, _, err := f.fireReuse(ctx, res, sch, time.Now().UTC())
	if err != nil || status != "fired" {
		t.Fatalf("status=%q err=%v, want fired/nil", status, err)
	}
	if len(a.inputs) != 1 {
		t.Errorf("queue overlap should send to the busy session (inputs=%v)", a.inputs)
	}
}

// A stopped reuse target (WS running, session stopped — the sbk7oej shape) is resumed and
// only THEN sent, gated on input-readiness rather than mere aliveness: the fire must
// /start the session, wait for /status ready, and deliver exactly one /input.
func TestFireReuseResumesStoppedTargetThenSends(t *testing.T) {
	a := &fakeAgent{sessions: []sessionWire{{Name: "pinned", Alive: false, State: "stopped"}}}
	sch := store.Schedule{ID: "sch_stop", SessionMode: "reuse", ReuseTarget: "pinned", OwnerConv: "conv1", Prompt: "go"}
	f, res, st, ctx := newReuseFixture(t, a, sch)
	f.readyInterval = time.Millisecond // no need to slow the poll

	status, _, err := f.fireReuse(ctx, res, sch, time.Now().UTC())
	if err != nil || status != "fired" {
		t.Fatalf("status=%q err=%v, want fired/nil", status, err)
	}
	if len(a.startedAt) != 1 || a.startedAt[0] != "pinned" {
		t.Fatalf("startedAt=%v, want [pinned] (stopped target must be resumed)", a.startedAt)
	}
	if len(a.inputs) != 1 || a.inputs[0] != "pinned" {
		t.Fatalf("inputs=%v, want [pinned] (send after resume)", a.inputs)
	}
	got, _, _ := st.GetSchedule(ctx, "sch_stop")
	if got.ReuseRunCount != 1 {
		t.Errorf("run count = %d, want 1 (advanced only after a real delivery)", got.ReuseRunCount)
	}
}

// Regression for the sbk7oej silent-drop: a reuse target that is alive but never becomes
// input-ready (a booting or zombie pane whose CLI can't accept the prompt) must NOT be
// recorded as a successful "fired" send. Before the readiness gate, /input returned 200 for
// keystrokes typed into that pane and reuse_run_count advanced even though nothing ran.
// Now the fire errors (surfaced to the operator) and the ledger does not advance.
func TestFireReuseUnreadyTargetErrorsNotSilentFire(t *testing.T) {
	a := &fakeAgent{
		sessions: []sessionWire{{Name: "zombie", Alive: true, State: "idle"}},
		unready:  map[string]bool{"zombie": true},
	}
	sch := store.Schedule{ID: "sch_zomb", SessionMode: "reuse", ReuseTarget: "zombie", OwnerConv: "conv1", Prompt: "go"}
	f, res, st, ctx := newReuseFixture(t, a, sch)
	f.readyTimeout = 150 * time.Millisecond // bound the not-ready wait for the test
	f.readyInterval = 5 * time.Millisecond

	status, _, err := f.fireReuse(ctx, res, sch, time.Now().UTC())
	if err == nil {
		t.Fatalf("want an error for an unready target, got status=%q nil err", status)
	}
	if len(a.inputs) != 0 {
		t.Errorf("must not POST /input to an unready pane (inputs=%v)", a.inputs)
	}
	got, _, _ := st.GetSchedule(ctx, "sch_zomb")
	if got.ReuseRunCount != 0 {
		t.Errorf("run count advanced on a swallowed send: %d, want 0", got.ReuseRunCount)
	}
}
