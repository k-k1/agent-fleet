package memconsol

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/memberorigin"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/memberorigin/memberorigintest"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/memengine"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/memoryx"
)

func queued(t *testing.T, f *fakeStore) Proposal {
	t.Helper()
	f.wrong("branches", "trunk is develop\nrest", 3)
	mustRun(t, input(&scriptModel{classify: classifyAs("patch", "branches"), patch: patchEdit("trunk is develop", "trunk is main")}, nil, t0))
	return pendingList(t, t0)[0]
}

// Approval re-validates against the current revision, then publishes through the store's patch path
// (one call per publishable item, with the revision the engine saw).
func TestApprovePublishesThroughTheStore(t *testing.T) {
	f := env(t)
	p := queued(t, f)
	got, err := Approve(p.ID, t0.Add(time.Hour))
	if err != nil || got.Status != StatusApproved || got.Items[0].Commit == "" {
		t.Fatalf("approve = %+v, %v", got, err)
	}
	if len(f.published) != 1 || f.published[0].Op != "patch" || f.published[0].Name != "branches" || f.published[0].Revision != 3 ||
		len(f.published[0].Edits) != 1 || f.published[0].Edits[0].New != "trunk is main" {
		t.Fatalf("published = %+v", f.published)
	}
	if _, err := Approve(p.ID, t0.Add(2*time.Hour)); err != ErrNotPending || len(f.published) != 1 {
		t.Fatalf("a second approval: %v, published %d", err, len(f.published))
	}
}

// A memory that changed after the proposal was queued refuses the approval and leaves the store
// alone; the same goes for text that is gone.
func TestApproveRefusesWhenTheMemoryMovedOn(t *testing.T) {
	f := env(t)
	p := queued(t, f)
	f.put("project", testProject, memoryx.ConsolMemory{Name: "branches", Description: "d", Type: "project", Revision: 4, Body: "trunk is develop\nrest"})
	_, err := Approve(p.ID, t0.Add(time.Hour))
	var pr *Problem
	if !errors.As(err, &pr) || pr.Kind != ProbStale || f.nPublished() != 0 {
		t.Fatalf("err = %v, published %d", err, f.nPublished())
	}
	if got, _ := loadProposal(p.ID); got.Status != StatusRefused || got.Reason == "" {
		t.Fatalf("proposal = %+v", got)
	}
	// Same revision but the anchored text changed underneath (a hand edit that kept the number).
	f2 := env(t)
	p2 := queued(t, f2)
	f2.put("project", testProject, memoryx.ConsolMemory{Name: "branches", Description: "d", Type: "project", Revision: 3, Body: "something else"})
	if _, err := Approve(p2.ID, t0.Add(time.Hour)); !errors.As(err, &pr) || pr.Kind != ProbStale || f2.nPublished() != 0 {
		t.Fatalf("err = %v", err)
	}
}

// A store refusal on approval (the store's own no-revival rule, a secret) surfaces as the proposal's
// refusal, with nothing published.
func TestApproveCarriesTheStoresRefusal(t *testing.T) {
	f := env(t)
	p := queued(t, f)
	f.pubErr = &memoryx.ReviewRefusal{Kind: memoryx.ReviewForgotten, Msg: "no"}
	_, err := Approve(p.ID, t0.Add(time.Hour))
	var pr *Problem
	if !errors.As(err, &pr) || pr.Kind != memoryx.ReviewForgotten {
		t.Fatalf("err = %v", err)
	}
	if got, _ := loadProposal(p.ID); got.Status != StatusRefused {
		t.Fatalf("status = %s", got.Status)
	}
}

// A create from an exported proposal is revalidated at approval: a name forgotten since is refused,
// the store untouched.
func TestApproveCreateIsRevalidatedForRevival(t *testing.T) {
	f := env(t)
	f.put("project", testProject, memoryx.ConsolMemory{Name: "anchor", Description: "d", Type: "project", Revision: 1, Body: "b"})
	writeExport(t, exported(1, "create", "fresh", "lesson", "fresh text", t0.Add(-48*time.Hour)))
	mustRun(t, input(&scriptModel{classify: classifyAs("add")}, nil, t0))
	ps := pendingList(t, t0)
	if len(ps) != 1 || ps[0].Items[0].Op != OpCreate {
		t.Fatalf("pending = %+v", ps)
	}
	f.forgotten["project:"+testProject+"/fresh"] = true // forgotten between queueing and approval
	_, err := Approve(ps[0].ID, t0.Add(time.Hour))
	var pr *Problem
	if !errors.As(err, &pr) || pr.Kind != ProbForgotten || f.nPublished() != 0 {
		t.Fatalf("err = %v, published %d", err, f.nPublished())
	}
}

func TestRejectAndTheViewDiff(t *testing.T) {
	f := env(t)
	p := queued(t, f)
	v, err := View(p.ID, t0)
	if err != nil || len(v.Views) != 1 || !strings.Contains(v.Views[0].Diff, "-trunk is develop") || !strings.Contains(v.Views[0].Diff, "+trunk is main") || v.Views[0].Stale != "" {
		t.Fatalf("view = %+v, %v", v, err)
	}
	f.put("project", testProject, memoryx.ConsolMemory{Name: "branches", Description: "d", Type: "project", Revision: 9, Body: "trunk is develop\nrest"})
	if v, _ := View(p.ID, t0); v.Views[0].Stale == "" {
		t.Fatal("a moved memory is not flagged stale in the view")
	}
	got, err := Reject(p.ID, t0.Add(time.Hour))
	if err != nil || got.Status != StatusRejected || f.nPublished() != 0 {
		t.Fatalf("reject = %+v, %v", got, err)
	}
	if _, err := Reject(p.ID, t0.Add(2*time.Hour)); err != ErrNotPending {
		t.Fatalf("second reject: %v", err)
	}
}

// The decision routes answer only the Control Plane's member-origin mark. Without it an agent (which
// holds the same token) cannot approve, reject or retry, and the store is untouched.
func TestDecisionRoutesRequireTheMemberOriginMark(t *testing.T) {
	f := env(t)
	p := queued(t, f)
	body := `{"id":"` + p.ID + `"}`
	routes := map[string]http.HandlerFunc{"approve": HandleApprove, "reject": HandleReject, "retry": HandleRetry}
	for name, h := range routes {
		r := httptest.NewRequest(http.MethodPost, "/agents/memory-consolidation/proposals/"+name, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		for label, handler := range map[string]http.Handler{"bare": h, "wrapped": memberorigin.Require(DecideMaxRequest, h)} {
			r := r.Clone(r.Context())
			r.Body = io.NopCloser(strings.NewReader(body))
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != http.StatusForbidden && w.Code != http.StatusUnauthorized && w.Code != http.StatusServiceUnavailable {
				t.Errorf("%s (%s) without a mark: status %d: %s", name, label, w.Code, w.Body)
			}
		}
	}
	if f.nPublished() != 0 {
		t.Fatal("an unmarked request reached the store")
	}
	if got, _ := loadProposal(p.ID); got.Status != StatusPending {
		t.Fatalf("status after unmarked requests = %s", got.Status)
	}
	// With the mark, the same request approves.
	s := memberorigintest.Install(t)
	r := httptest.NewRequest(http.MethodPost, "/agents/memory-consolidation/proposals/approve", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	s.Sign(r, []byte(body))
	w := httptest.NewRecorder()
	memberorigin.Require(DecideMaxRequest, http.HandlerFunc(HandleApprove)).ServeHTTP(w, r)
	if w.Code != http.StatusOK || f.nPublished() != 1 {
		t.Fatalf("marked approve: %d %s (published %d)", w.Code, w.Body, f.nPublished())
	}
}

func TestReadRoutes(t *testing.T) {
	f := env(t)
	p := queued(t, f)
	w := httptest.NewRecorder()
	HandleList(w, httptest.NewRequest(http.MethodGet, "/agents/memory-consolidation/proposals?status=pending", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), p.ID) {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	HandleGet(w, httptest.NewRequest(http.MethodGet, "/agents/memory-consolidation/proposal?id="+p.ID, nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"diff"`) {
		t.Fatalf("get: %d %s", w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	HandleGet(w, httptest.NewRequest(http.MethodGet, "/agents/memory-consolidation/proposal?id=../x", nil))
	if w.Code != 400 {
		t.Fatalf("bad id: %d", w.Code)
	}
	w = httptest.NewRecorder()
	HandleStatus(w, httptest.NewRequest(http.MethodGet, "/agents/memory-consolidation", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"counts"`) {
		t.Fatalf("status: %d %s", w.Code, w.Body)
	}
}

// ---- the trigger -------------------------------------------------------------------------------

func TestDecide(t *testing.T) {
	c := DefaultConfig()
	idle := t0.Add(-16 * time.Minute)
	cases := []struct {
		name    string
		enabled bool
		last    time.Time
		act     time.Time
		busy    bool
		want    string
	}{
		{"switch off", false, time.Time{}, idle, false, "disabled"},
		{"a session is working", true, time.Time{}, idle, true, "busy"},
		{"a turn ended 14 minutes ago", true, time.Time{}, t0.Add(-14 * time.Minute), false, "not-idle"},
		{"idle but ran 2h ago", true, t0.Add(-2 * time.Hour), idle, false, "too-soon"},
		{"idle, last run 3h ago", true, t0.Add(-3 * time.Hour), idle, false, "idle"},
		{"idle, never ran, never any activity", true, time.Time{}, time.Time{}, false, "idle"},
	}
	for _, tc := range cases {
		d := Decide(tc.enabled, t0, tc.last, tc.act, tc.busy, c)
		if d.Reason != tc.want || d.Run != (tc.want == "idle") {
			t.Errorf("%s: %+v, want %s", tc.name, d, tc.want)
		}
	}
}

func tickEnv(t *testing.T, enabled *bool, busy *bool, last *time.Time, small, large *scriptModel) {
	t.Helper()
	old := currentDeps()
	Configure(Deps{
		Enabled:  func() bool { return *enabled },
		Activity: func() (bool, time.Time) { return *busy, *last },
		Models: func() (Models, map[string]string, error) {
			return Models{Small: small, Large: large}, map[string]string{"small": "claude:haiku"}, nil
		},
	})
	t.Cleanup(func() { Configure(old) })
}

// The trigger never bypasses the idle check: switched off, busy and not-yet-idle all make no model
// call; once idle it runs, and not again within the minimum interval.
func TestTickRunsOnlyWhenIdleAndAtMostEveryInterval(t *testing.T) {
	f := env(t)
	f.wrong("a", "x", 1)
	small := &scriptModel{classify: classifyAs("no-op")}
	enabled, busy, last := false, false, t0.Add(-time.Hour)
	tickEnv(t, &enabled, &busy, &last, small, nil)
	ctx := context.Background()

	if _, ran := Tick(ctx, t0); ran || small.calls() != 0 {
		t.Fatal("ran with the switch off")
	}
	enabled, busy = true, true
	if _, ran := Tick(ctx, t0.Add(time.Minute)); ran || small.calls() != 0 {
		t.Fatal("ran while a session was working")
	}
	busy, last = false, t0.Add(time.Minute) // a turn just ended
	if _, ran := Tick(ctx, t0.Add(2*time.Minute)); ran || small.calls() != 0 {
		t.Fatal("ran 1 minute after a turn")
	}
	if got := stateOf(t).Trigger.BusyTicks; got != 2 {
		t.Fatalf("busy ticks = %d", got)
	}
	if st, err := BuildStatus(t0.Add(3 * time.Minute)); err != nil || len(st.Waiting) != 1 || st.Waiting[0].Cause != CauseNeverIdle {
		t.Fatalf("status = %+v, %v", st, err)
	}
	now := t0.Add(20 * time.Minute)
	rep, ran := Tick(ctx, now)
	if !ran || rep.Status != RunOK || small.calls() != 1 {
		t.Fatalf("idle tick: ran=%v report=%+v calls=%d", ran, rep, small.calls())
	}
	f.wrong("b", "y", 1)
	if _, ran := Tick(ctx, now.Add(2*time.Hour)); ran {
		t.Fatal("ran again inside the minimum interval")
	}
	if _, ran := Tick(ctx, now.Add(3*time.Hour+time.Minute)); !ran {
		t.Fatal("did not run once the interval had passed")
	}
	if s := stateOf(t).Trigger; s.BusyTicks != 0 || s.LastRun.IsZero() {
		t.Fatalf("trigger state = %+v", s)
	}
}

// A retired or unmapped model fails the run before any call, and the run still counts as the look
// (the next one is 3 h away), so a dead model is not hammered every minute.
func TestRetiredModelFailsTheTickSafely(t *testing.T) {
	f := env(t)
	f.wrong("a", "x", 1)
	enabled, busy, last := true, false, time.Time{}
	old := currentDeps()
	Configure(Deps{Enabled: func() bool { return enabled }, Activity: func() (bool, time.Time) { return busy, last },
		Models: func() (Models, map[string]string, error) { return Models{}, nil, errors.New("no runner") }})
	t.Cleanup(func() { Configure(old) })
	rep, ran := Tick(context.Background(), t0)
	if !ran || rep.Status != RunFailed || len(pendingList(t, t0)) != 0 {
		t.Fatalf("ran=%v report=%+v", ran, rep)
	}
	if _, ran := Tick(context.Background(), t0.Add(time.Hour)); ran {
		t.Fatal("retried a failed run inside the interval")
	}
}

// A tick after a long silence marks the gap, and a ripe topic then reads as delayed by the stopped
// workspace.
func TestStoppedGapIsReported(t *testing.T) {
	f := env(t)
	f.wrong("a", "x", 1)
	enabled, busy, last := false, false, time.Time{}
	tickEnv(t, &enabled, &busy, &last, &scriptModel{}, nil)
	Tick(context.Background(), t0)
	Tick(context.Background(), t0.Add(9*time.Hour))
	st, err := BuildStatus(t0.Add(9 * time.Hour))
	if err != nil || len(st.Waiting) != 1 || st.Waiting[0].Cause != CauseStopped {
		t.Fatalf("status = %+v, %v", st, err)
	}
}

// The daily notice hook: nothing without a hook, at most once per NoticeEvery while any proposal is
// pending, never with none pending.
func TestNoticeHookIsGatedDaily(t *testing.T) {
	f := env(t)
	queued(t, f)
	cfg := DefaultConfig()
	calls := 0
	d := Deps{Notice: func(n int) { calls += n }}
	maybeNotice(Deps{}, cfg, t0)
	maybeNotice(d, cfg, t0)
	maybeNotice(d, cfg, t0.Add(time.Hour))
	if calls != 1 {
		t.Fatalf("notice calls = %d", calls)
	}
	maybeNotice(d, cfg, t0.Add(25*time.Hour))
	if calls != 2 {
		t.Fatalf("after a day: %d", calls)
	}
}

func TestLoadConfigDefaultsAndOverride(t *testing.T) {
	env(t)
	if c := LoadConfig(); c != DefaultConfig() {
		t.Fatalf("defaults = %+v", c)
	}
	if err := writeJSONAtomic(Dir()+"/config.json", map[string]any{"idleAfter": "5m", "newObservations": 7, "tokenBudget": -1, "maxWait": "junk"}); err != nil {
		t.Fatal(err)
	}
	c := LoadConfig()
	d := DefaultConfig()
	if c.IdleAfter != 5*time.Minute || c.NewObservations != 7 || c.TokenBudget != d.TokenBudget || c.MaxWait != d.MaxWait {
		t.Fatalf("config = %+v", c)
	}
}

func TestEngineConfigMapsTheRolesFromData(t *testing.T) {
	env(t)
	ec, err := LoadEngineConfig()
	if err != nil || ec.Roles["small"] != "claude:haiku" || ec.Roles["large"] != "claude:sonnet" {
		t.Fatalf("roles = %+v, %v", ec.Roles, err)
	}
	m, roles, err := configuredModels(DefaultConfig())
	if err != nil || m.Small == nil || m.Large == nil || roles["small"] != "claude:haiku" {
		t.Fatalf("models = %+v %v %v", m, roles, err)
	}
	cmd, ok := m.Small.(memengine.Command)
	argv := strings.Join(cmd.Argv, " ")
	if !ok || !strings.Contains(argv, "CLAUDE_CODE_DISABLE_CLAUDE_MDS=1") || !strings.Contains(argv, "--model haiku") {
		t.Fatalf("small runner argv = %q", argv)
	}
}
