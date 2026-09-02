package main

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// scheduler_live_test.go — end-to-end verification of the scheduled-execution pipeline
// (docs/log/38 P5 pre-flight). Where scheduler_test.go drives sc.tick by hand and writes the
// store directly, these tests exercise the REAL wiring the fleet uses: the operator
// create/run_now HTTP handlers write the store, and the actual ticker goroutine (sc.run,
// not a hand-called tick) picks the row up and fires it. Only the side-effecting firer
// (wake + create_session) is stubbed, since a real workspace wake needs the full fleet.
// This is the closest deterministic proof-of-fire available without a running deployment.

// recordingFirer captures fires from the live ticker goroutine; guarded because run() and
// the test read it from different goroutines. `fires` は発火のたびに 1 つ入る通知
// （バッファ付き・**送信はノンブロッキング**なので、誰も待っていなくても発火を妨げない）。
type recordingFirer struct {
	mu     sync.Mutex
	fired  []store.Schedule
	status string
	fires  chan struct{}
}

func newRecordingFirer() *recordingFirer {
	return &recordingFirer{fires: make(chan struct{}, 16)}
}

func (f *recordingFirer) fire(_ context.Context, sch store.Schedule, _ time.Time) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fired = append(f.fired, sch)
	if f.fires != nil {
		select {
		case f.fires <- struct{}{}:
		default: // 誰も待っていない / バッファ満杯 —— 記録は fired に残るので落として構わない
		}
	}
	if f.status != "" {
		return f.status, "", nil
	}
	return "fired", "", nil
}

func (f *recordingFirer) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.fired)
}

func (f *recordingFirer) first() (store.Schedule, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.fired) == 0 {
		return store.Schedule{}, false
	}
	return f.fired[0], true
}

// waitFire は発火を **イベントで** 待つ。以前はここが「2 秒まで 10 ms 間隔でポーリング」
// だったが、それは**負荷で落ちる形**である —— 30 ms の ticker が 2 秒の間に何回回るかは
// 機械の忙しさ次第で、遅れたことと発火しないことを区別できない。
//
// ⚠️ **これは「待ち時間を伸ばして緩めた」のではない。** 緑になる速さは変わらない（発火した
// 瞬間に返る）。締切は「起きなかった」と**断定する**ためだけにあるので、長くしても検査は
// 弱くならず、短いと嘘をつく。
func (f *recordingFirer) waitFire(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case <-f.fires:
	case <-time.After(d):
		t.Fatalf("%v 待っても発火しなかった (count=%d)", d, f.count())
	}
}

// liveFireDeadline は「発火しなかった」と断定するまでの上限。共有ホストで他のセッションが
// ビルドを回していても、発火そのものが起きないことは無い。
const liveFireDeadline = 30 * time.Second

// TestLiveOperatorCreateThenTickerFires: an operator registers a due `once` schedule
// through the real create handler, and the real ticker goroutine fires it, advances the
// ledger, and appends a run — no hand-driven tick anywhere.
func TestLiveOperatorCreateThenTickerFires(t *testing.T) {
	st, ctx := newSchedTestStore(t)
	api := newScheduleAPI(&manager{store: st})
	mv := store.MembershipView{MembershipID: "m1", TenantID: "default"}

	// Mark the scheduler enabled so create's response mirrors a real firing deployment
	// (no "scheduler disabled" warning). Restore after — it is a package global.
	prev := schedulerRunning
	schedulerRunning = true
	t.Cleanup(func() { schedulerRunning = prev })

	// A `once` whose instant is the current second is due immediately (jitter is 0 for
	// once). RFC3339 has second resolution, so "now" is the earliest imminent trigger.
	spec := time.Now().UTC().Format(time.RFC3339)
	body := `{"spec_kind":"once","spec":"` + spec + `","tz":"Asia/Tokyo","spec_label":"検証ワンショット","prompt":"日次レビュー {{date}} {{time}}"}`
	rec := doJSON(api.create, mv, "POST", body, "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("create code=%d body=%s", rec.Code, rec.Body.String())
	}
	var dto scheduleDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if dto.Warning != "" {
		t.Fatalf("unexpected disabled warning with scheduler enabled: %q", dto.Warning)
	}
	if dto.ID == "" {
		t.Fatal("create returned no id")
	}

	// Start the REAL ticker goroutine at a fast interval.
	ff := newRecordingFirer()
	sc := newScheduler(st, ff, 30*time.Millisecond)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go sc.run(runCtx)

	ff.waitFire(t, liveFireDeadline)
	fired, _ := ff.first()
	if fired.ID != dto.ID {
		t.Fatalf("fired id=%q want %q", fired.ID, dto.ID)
	}

	// Ledger + history reflect the fire, and a spent `once` disabled itself.
	got, ok, err := st.GetSchedule(ctx, dto.ID)
	if err != nil || !ok {
		t.Fatalf("get after fire: err=%v ok=%v", err, ok)
	}
	if got.LastStatus != "fired" {
		t.Fatalf("last_status=%q want fired", got.LastStatus)
	}
	if got.Enabled || got.NextRun != "" {
		t.Fatalf("spent once not disabled: enabled=%v next_run=%q", got.Enabled, got.NextRun)
	}
	runs, err := st.ListScheduleRuns(ctx, dto.ID, mv.MembershipID, 50)
	if err != nil || len(runs) != 1 || runs[0].Status != "fired" {
		t.Fatalf("run history = %+v err=%v", runs, err)
	}

	// The ticker must not re-fire the now-disabled row.
	//
	// ⚠️ ここは 150 ms 眠って数を見ていた。**眠っている間に tick が 1 度も回らなくても
	// 通る**ので、検査が空振りしていても分からない（負荷が上がるほど空振りしやすい＝
	// 一番効いてほしいときに効かない）。ループを止め、tick を**同期で 2 回**踏む。
	cancel()
	firstCount := ff.count()
	sc.tickAt(ctx, time.Now().UTC())
	sc.tickAt(ctx, time.Now().UTC())
	if ff.count() != firstCount {
		t.Fatalf("disabled schedule re-fired: %d -> %d", firstCount, ff.count())
	}
}

// TestLiveRunNowFiresThroughTicker: run_schedule_now on a not-yet-due interval schedule
// makes it due immediately and the real ticker fires it through the same path as a timed
// fire (ADR0021 decision 8). Proves the manual-fire affordance the operator uses to smoke
// test a schedule actually reaches the firer.
func TestLiveRunNowFiresThroughTicker(t *testing.T) {
	st, ctx := newSchedTestStore(t)
	api := newScheduleAPI(&manager{store: st})
	mv := store.MembershipView{MembershipID: "m1", TenantID: "default"}
	prev := schedulerRunning
	schedulerRunning = true
	t.Cleanup(func() { schedulerRunning = prev })

	// interval=3600s: next_run is an hour out, so it is NOT due until run_now moves it.
	rec := doJSON(api.create, mv, "POST",
		`{"spec_kind":"interval","spec":"3600","prompt":"hourly check"}`, "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("create code=%d body=%s", rec.Code, rec.Body.String())
	}
	var dto scheduleDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &dto)

	ff := newRecordingFirer()
	sc := newScheduler(st, ff, 30*time.Millisecond)

	// It must NOT fire on its own (next_run is an hour away).
	// ⚠️ 眠って「たぶん ticker が回ったはず」で確かめない —— **tick を同期で 1 回踏む**。
	// 眠るだけだと、その 200 ms に ticker が 1 度も回らなくても緑になる（＝この行が何も
	// 検査していない状態を、誰も見分けられない）。
	sc.tickAt(ctx, time.Now().UTC())
	if ff.count() != 0 {
		t.Fatalf("interval fired before run_now: %d", ff.count())
	}

	// ここからが主題: run_now を **ticker 経由で**拾わせる。
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go sc.run(runCtx)

	// Operator hits run_now -> due immediately.
	rn := doJSON(api.runNow, mv, "POST", "", dto.ID)
	if rn.Code != http.StatusOK {
		t.Fatalf("run_now code=%d body=%s", rn.Code, rn.Body.String())
	}
	ff.waitFire(t, liveFireDeadline)

	// interval stays enabled with a fresh future next_run after the fire.
	got, _, _ := st.GetSchedule(ctx, dto.ID)
	if !got.Enabled {
		t.Fatal("interval schedule should stay enabled after run_now")
	}
	if got.NextRun == "" || got.NextRun <= store.NowTS() {
		t.Fatalf("next_run not advanced to the future: %q", got.NextRun)
	}
}

// TestLeapDayCronResolves guards the P4.1 horizon fix: a Feb-29-only cron must resolve to
// a real leap-day instant instead of being rejected (a 1-year search horizon wrongly
// failed it). Covers the review finding that previously had no test.
func TestLeapDayCronResolves(t *testing.T) {
	utc := time.UTC
	// From mid-2026 (a non-leap year), the next 29 Feb is 2028-02-29.
	after := time.Date(2026, 7, 22, 0, 0, 0, 0, utc)
	got, err := nextCron("0 0 29 2 *", after, utc)
	if err != nil {
		t.Fatalf("leap-day cron rejected (horizon too small?): %v", err)
	}
	want := time.Date(2028, 2, 29, 0, 0, 0, 0, utc)
	if !got.Equal(want) {
		t.Fatalf("got %s want %s", got.UTC().Format(time.RFC3339), want.Format(time.RFC3339))
	}
}
