package main

// (ref, idx) 重複排除の回帰（usage_dedup.go / docs/log/46 §7-4）。
//
// 塞ぐ穴は1つ: 折り込みが「行を追記 → watermark を書く」の間で落ちると、そのセッションの
// 数ターン分が次のパスで再追記される。書き手側では閉じられない（別ファイルなので原子的に
// 書けない）ので、集計側が落とせていることを見る。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// sessionRow は折り込み1行を作る。**重複行でも call は別 ID になる**（folder が行ごとに
// UUID を振るため）— だから call の重複排除では捕まらず、(ref, idx) が要る。
func sessionRow(call, ref string, idx int, day string, spend int) usagex.Record {
	r := at(row(call, usagex.FeatureSession, session.KindClaude, "claude-haiku-4-5", spend), day)
	r.Ref, r.Idx = ref, idx
	return r
}

// 同じ raw ファイル内の再追記（クラッシュ直後に fold-on-read が走った形）を落とす。
func TestUsageSeriesDropsDuplicateSessionRows(t *testing.T) {
	useIsolatedUsageDir(t)
	day := daysAgo(0)
	writeUsageDay(t, day,
		sessionRow("c1", "slot01", 1, day, 100),
		sessionRow("c2", "slot01", 2, day, 200),
		// ここで落ちた（watermark 未更新）→ 次のパスが同じ2ターンを再追記し、続きも書く
		sessionRow("c3", "slot01", 1, day, 100),
		sessionRow("c4", "slot01", 2, day, 200),
		sessionRow("c5", "slot01", 3, day, 300),
	)
	got := getSeries(t, "from="+day+"&to="+day)
	if got.Totals.Spend != 600 || got.Totals.Calls != 3 {
		t.Fatalf("totals = %+v, want spend 600 / calls 3（重複排除前は 1200 / 5）", got.Totals)
	}
	// 生の台帳には重複行が残っていること（事後監査のため落とすのは集計側だけ）。
	if n := len(usagex.ReadRows()); n != 5 {
		t.Fatalf("raw = %d 行, want 5（台帳から行を消してはいけない）", n)
	}
}

// クラッシュ窓が日付を跨ぐと、重複は原本と**別のファイル日**へ落ちる。畳み込みは
// ファイル単位に走るので、水位をファイルを跨いで持ち回れていないと二重に入る。
func TestRollupDropsDuplicatesAcrossFileDays(t *testing.T) {
	useIsolatedUsageDir(t)
	d2, d1 := daysAgo(2), daysAgo(1)
	writeUsageDay(t, d2,
		sessionRow("c1", "slot01", 1, d2, 100),
		sessionRow("c2", "slot01", 2, d2, 200),
	)
	// 翌日のパスが idx 2 を再追記（消費時刻は転写のものなので、消費日は d2 のまま）。
	writeUsageDay(t, d1,
		sessionRow("c3", "slot01", 2, d2, 200),
		sessionRow("c4", "slot01", 3, d1, 300),
	)
	ensureUsageRollups()

	sum := func(day string) (spend, calls int) {
		for _, e := range readUsageRollup(day[:7]).Days[day].Entries {
			spend += e.Agg.Spend
			calls += e.Agg.Calls
		}
		return spend, calls
	}
	if spend, calls := sum(d2); spend != 300 || calls != 2 {
		t.Fatalf("消費日 %s = spend %d / calls %d, want 300 / 2（重複排除前は 500 / 3）", d2, spend, calls)
	}
	if spend, calls := sum(d1); spend != 300 || calls != 1 {
		t.Fatalf("消費日 %s = spend %d / calls %d, want 300 / 1", d1, spend, calls)
	}
}

// 原本が畳み済み（rollup が正・raw は読まない）で、重複だけが未畳みの raw に残っている形。
// 水位を rollup state から引き継げていないと、この重複は誰も落とせない。
func TestDedupSpansRolledAndRawBoundary(t *testing.T) {
	useIsolatedUsageDir(t)
	yesterday, today := daysAgo(1), daysAgo(0)
	writeUsageDay(t, yesterday,
		sessionRow("c1", "slot01", 1, yesterday, 100),
		sessionRow("c2", "slot01", 2, yesterday, 200),
	)
	ensureUsageRollups() // yesterday が畳まれ、(ref,idx) の水位が state に残る
	writeUsageDay(t, today,
		sessionRow("c3", "slot01", 2, yesterday, 200), // 再追記（消費日は昨日）
		sessionRow("c4", "slot01", 3, today, 300),
	)

	day := getSeries(t, "from="+yesterday+"&to="+today)
	if day.Totals.Spend != 600 || day.Totals.Calls != 3 {
		t.Fatalf("bucket=day totals = %+v, want spend 600 / calls 3", day.Totals)
	}
	// hour は rollup を使わず raw を全部読み直す。水位を空から積み直すので、原本の側まで
	// 落としていないこと（＝合計が day と一致すること）まで見る。
	hour := getSeries(t, "from="+yesterday+"&to="+today+"&bucket=hour")
	if hour.Totals.Spend != 600 || hour.Totals.Calls != 3 {
		t.Fatalf("bucket=hour totals = %+v, want spend 600 / calls 3", hour.Totals)
	}
}

// 落としてよいのは重複だけ。取りこぼし（消費が二度と戻らない）は重複より悪いので、
// 紛らわしい形が全部通ることを見る。
func TestUsageDedupKeepsLegitimateRows(t *testing.T) {
	dd := usageDedupIndex{}
	ts := func(min int) time.Time { return time.Date(2026, 7, 26, 0, min, 0, 0, time.UTC) }
	accept := func(ref string, idx int, min int) bool {
		return dd.accept(sessionRow("c", ref, idx, "2026-07-26", 10), ts(min))
	}
	if !accept("slot01", 1, 1) || !accept("slot01", 2, 2) {
		t.Fatal("連番のターンを落とした")
	}
	if !accept("slot02", 1, 3) {
		t.Fatal("別セッションの同じ idx を落とした（ref を見ていない）")
	}
	if accept("slot01", 2, 2) {
		t.Fatal("重複を通した")
	}
	if !accept("slot01", 3, 4) {
		t.Fatal("重複の後に続く新しいターンを落とした")
	}
	// 補助呼び出しには idx が無い。同じ形の行が何行来ても落とさない。
	aux := at(row("c9", usagex.FeatureTitleSession, session.KindClaude, "haiku", 10), "2026-07-26")
	aux.Ref = "slot01"
	if !dd.accept(aux, ts(5)) || !dd.accept(aux, ts(5)) {
		t.Fatal("補助呼び出しの行を重複扱いした（idx を持たないので判定できない）")
	}
	// slug の再利用（削除済みセッションの名前が再び払い出される）。idx は 1 に戻るが、
	// 消費時刻は必ず後になる — ここを落とすと新しいセッションの消費が静かに消える。
	if !accept("slot01", 1, 99) {
		t.Fatal("slug 再利用後の idx=1 を重複扱いした")
	}
}

// 版が上がった時、既に重複を畳み込んでいる可能性のある rollup を raw から作り直す。
// 集計は加算済みで引き算できないので、作り直す以外に落とす手が無い。
func TestRollupRebuildPurgesLegacyDuplicates(t *testing.T) {
	useIsolatedUsageDir(t)
	day := daysAgo(1)
	writeUsageDay(t, day,
		sessionRow("c1", "slot01", 1, day, 100),
		sessionRow("c2", "slot01", 1, day, 100), // v1 の頃に紛れ込んだ重複
	)
	// v1 の rollup（重複ごと畳み込まれた集計）と、版を持たない state を手で置く。
	k := usageKey{Feature: usagex.FeatureSession, Trigger: usagex.TriggerUser, Kind: session.KindClaude,
		Model: "claude-haiku-4-5", ModelSrc: usagex.ModelReported, Measured: usagex.MeasuredExact, OK: true}
	if err := writeUsageJSON(usageRollupPath(day[:7]), usageRollupMonth{Days: map[string]usageRollupDay{
		day: {Src: []string{day}, Entries: []usageRollupEntry{{Key: k, Agg: usageAgg{Spend: 200, In: 200, Calls: 2}}}},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := writeUsageJSON(usageRollupStatePath(), map[string]any{
		"rolled": map[string]usageRolledFile{day: {MinDay: day, MaxDay: day}},
	}); err != nil {
		t.Fatal(err)
	}

	ensureUsageRollups()

	entries := readUsageRollup(day[:7]).Days[day].Entries
	if len(entries) != 1 || entries[0].Agg.Spend != 100 || entries[0].Agg.Calls != 1 {
		t.Fatalf("作り直し後の集計 = %+v, want spend 100 / calls 1", entries)
	}
	if v := readUsageRollupState().Version; v != usageRollupVersion {
		t.Fatalf("版が %d のまま（作り直しを毎回試みてしまう）", v)
	}
}

// 寄与元の raw が prune 済みなら作り直さない（消えた分の集計を失う方が重い）。代わりに
// 見えている畳み済みファイルから水位を復元し、以後に入る重複は落とせるようにする。
func TestRollupRebuildKeepsDataWhenRawIsPruned(t *testing.T) {
	dir := useIsolatedUsageDir(t)
	gone, kept := daysAgo(3), daysAgo(2)
	writeUsageDay(t, gone, sessionRow("c0", "slot99", 1, gone, 700))
	writeUsageDay(t, kept, sessionRow("c1", "slot01", 1, kept, 100))
	ensureUsageRollups()
	if err := os.Remove(filepath.Join(dir, "raw", gone+".jsonl")); err != nil {
		t.Fatal(err)
	}
	// 版を巻き戻して「v1 の rollup が残っている」状態にする。
	st := readUsageRollupState()
	st.Version, st.Folded = 1, nil
	if err := writeUsageJSON(usageRollupStatePath(), st); err != nil {
		t.Fatal(err)
	}

	ensureUsageRollups()

	if e := readUsageRollup(gone[:7]).Days[gone].Entries; len(e) != 1 || e[0].Agg.Spend != 700 {
		t.Fatalf("raw が消えた日の集計を失った: %+v", e)
	}
	// 水位は残っている raw から復元されている＝以後の重複は落ちる。
	writeUsageDay(t, daysAgo(1), sessionRow("c2", "slot01", 1, kept, 100))
	ensureUsageRollups()
	spend := 0
	for _, e := range readUsageRollup(kept[:7]).Days[kept].Entries {
		spend += e.Agg.Spend
	}
	if spend != 100 {
		t.Fatalf("復元した水位で重複を落とせていない: spend = %d, want 100", spend)
	}
}

// 実際のクラッシュ窓を通しで再現する: 折り込み → watermark を書けずに落ちる → 次のパスが
// 同じターンを再追記。台帳には二重に入るが、系列は1回分しか数えない。
func TestFoldCrashWindowIsNotDoubleCounted(t *testing.T) {
	useIsolatedUsageDir(t)
	turns := []transcript.Turn{
		{Role: "user", Text: "1"},
		asst("claude-haiku-4-5", 100, 10, 0, 0, false),
		{Role: "user", Text: "2"},
		asst("claude-haiku-4-5", 200, 20, 0, 0, false),
		{Role: "user", Text: "3"}, // 直前のターンを閉じる
	}
	m := session.Meta{Name: "slot01", Kind: session.KindClaude}
	fold := func(persist bool) {
		t.Helper()
		usageFoldMu.Lock()
		defer usageFoldMu.Unlock()
		st := readUsageFoldState()
		if _, err := foldSessionUsageWithTurns(m, &st, turns, false); err != nil {
			t.Fatalf("折り込みに失敗: %v", err)
		}
		if persist {
			if err := writeUsageFoldState(st); err != nil {
				t.Fatal(err)
			}
		}
	}
	fold(false) // 行は書けたが watermark を書く前に落ちた
	fold(true)  // 次のパスが同じ2ターンを再追記する

	if n := len(usagex.ReadRows()); n != 4 {
		t.Fatalf("台帳 = %d 行, want 4（クラッシュ窓で二重に入る形を再現できていない）", n)
	}
	want := 0 // 論理ターン1回分ずつ（重複を数えれば倍になる）
	for _, r := range foldTurnRows(turns, false) {
		want += usagex.Spend(r.Tokens.In, r.Tokens.CacheCreate, r.Tokens.Out)
	}
	day := "2026-07-26" // asst() の TS（消費日）
	got := getSeries(t, "from="+day+"&to="+day)
	if got.Totals.Spend != want || got.Totals.Calls != 2 {
		t.Fatalf("totals = %+v, want spend %d / calls 2", got.Totals, want)
	}
}

// ハッシュキーは ref を平文で無期限領域へ残さないためのもの（ADR0029 §8 の「rollup に
// ref を入れない」）。索引ファイルにセッション名が出ないことを見る。
func TestUsageDedupIndexDoesNotStoreRefNames(t *testing.T) {
	useIsolatedUsageDir(t)
	day := daysAgo(1)
	writeUsageDay(t, day, sessionRow("c1", "slot-secret", 1, day, 100))
	ensureUsageRollups()
	b, err := os.ReadFile(usageRollupStatePath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "slot-secret") {
		t.Fatalf("索引に ref が平文で載っている: %s", b)
	}
	if len(readUsageRollupState().Folded) != 1 {
		t.Fatalf("水位が記録されていない: %+v", readUsageRollupState().Folded)
	}
}
