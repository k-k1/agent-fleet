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
//
// 🔴 **保証するのは「firer が呼ばれた」ことだけで、台帳が前進したことではない。**
// 通知は firer の内側（fire の中）から送られるのに対し、台帳の前進 RecordScheduleFire と
// 実行履歴の追記 AppendScheduleRun は **firer が戻ったあと**に ticker の goroutine が
// 走らせる（scheduler.go の runOne）。その間には advanceNextRun と SQLite への書き込みが
// 挟まるので、ここから戻った直後に台帳を読むと**まだ前進していないことがある**。
// 実測（2026-09-04・8 コア）: 200 反復に 1 回ほど `last_status="" want fired`。
// **待ち時間の問題ではないので締切を伸ばしても直らない。台帳を読む前に waitLedger を通すこと。**
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

// waitLedger は台帳が発火を書き終える（cond が真になる）まで待って、その行を返す。
//
// **waitFire と対で使う。** waitFire が保証するのは firer の呼び出しまでで、台帳の前進は
// その後に別の goroutine（ticker）が走らせるため、2 つの間には必ず窓がある。ここを
// 「発火した＝台帳も前進済み」と決め打つと、contention で破れる（waitFire の注記を参照）。
//
// ⚠️ **待ち時間を稼ぐための sleep ではない。** cond が真になった瞬間に返るので緑になる速さは
// 変わらず、締切は「前進しなかった」と**断定する**ためだけにある —— waitFire と同じ考え方。
//
// 🔴 **cond には「台帳が前進したか」だけを書き、期待する値そのものを書かないこと。**
// 例えば `LastStatus == "fired"` を条件にすると、scheduler が `error:...` を書いた場合に
// 締切まで待たされたうえ「前進しなかった」という**嘘の見出し**で落ちる。「前進したか」で
// 待ち、値の判定は呼び出し側のアサーションに残す —— そうすれば従来どおりの文言で落ちる。
func waitLedger(t *testing.T, ctx context.Context, st *store.SQL, id string, d time.Duration, cond func(store.Schedule) bool) store.Schedule {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		got, ok, err := st.GetSchedule(ctx, id)
		if err != nil {
			t.Fatalf("台帳の読み出しに失敗した (%s): %v", id, err)
		}
		if !ok {
			t.Fatalf("台帳から schedule %s が消えた", id)
		}
		if cond(got) {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("%v 待っても台帳が前進しなかった: last_status=%q next_run=%q enabled=%v",
				d, got.LastStatus, got.NextRun, got.Enabled)
		}
		time.Sleep(time.Millisecond)
	}
}

// waitRuns は実行履歴が n 行に達するまで待って、その一覧を返す。AppendScheduleRun は
// RecordScheduleFire の **さらに後**（best-effort）なので、台帳の前進を待っただけでは
// まだ書かれていないことがある。
func waitRuns(t *testing.T, ctx context.Context, st *store.SQL, id, membershipID string, n int, d time.Duration) []store.ScheduleRun {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		runs, err := st.ListScheduleRuns(ctx, id, membershipID, 50)
		if err != nil {
			t.Fatalf("実行履歴の読み出しに失敗した (%s): %v", id, err)
		}
		if len(runs) >= n {
			return runs
		}
		if time.Now().After(deadline) {
			t.Fatalf("%v 待っても実行履歴が %d 行に達しなかった: %+v", d, n, runs)
		}
		time.Sleep(time.Millisecond)
	}
}

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
	// 台帳の前進は firer の呼び出しより後なので、waitFire だけでは足りない（waitLedger の注記）。
	got := waitLedger(t, ctx, st, dto.ID, liveFireDeadline, func(s store.Schedule) bool {
		return s.LastStatus != "" // 「前進したか」だけを待つ。値の判定は下のアサーションが持つ
	})
	if got.LastStatus != "fired" {
		t.Fatalf("last_status=%q want fired", got.LastStatus)
	}
	if got.Enabled || got.NextRun != "" {
		t.Fatalf("spent once not disabled: enabled=%v next_run=%q", got.Enabled, got.NextRun)
	}
	runs := waitRuns(t, ctx, st, dto.ID, mv.MembershipID, 1, liveFireDeadline)
	if len(runs) != 1 || runs[0].Status != "fired" {
		t.Fatalf("run history = %+v", runs)
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
	// 🔴 ここも台帳の前進を待たないと破れる。run_now は next_run を「いま」に戻す
	// （schedule.go:371-372）ので、**前進する前に読むと next_run はまだ過去のまま**＝
	// 下の「未来へ進んだか」の判定が偽になる。waitFire から戻った時点では未確定。
	got := waitLedger(t, ctx, st, dto.ID, liveFireDeadline, func(s store.Schedule) bool {
		return s.LastStatus != ""
	})
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
