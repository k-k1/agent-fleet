package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// writeUsageDay は指定日の raw ファイルを直接作る（時計を進めずに「昨日以前」を作れる）。
func writeUsageDay(t *testing.T, day string, rows ...usageRecord) {
	t.Helper()
	dir := usageRawDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(dir, day+".jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, r := range rows {
		if err := enc.Encode(r); err != nil {
			t.Fatal(err)
		}
	}
}

func row(call, feature, kind, model string, spend int) usageRecord {
	return usageRecord{
		Call: call, Feature: feature, Kind: kind, Model: model, ModelSrc: usageModelReported,
		Trigger: usageTriggerUser, In: spend, Spend: spend, OK: true, Measured: usageMeasuredExact,
	}
}

// at は行の消費時刻を指定日の 12:00 に置く（バケットは ts で刻まれる）。
func at(r usageRecord, day string) usageRecord {
	r.TS = day + "T12:00:00Z"
	return r
}

func daysAgo(n int) string { return time.Now().UTC().AddDate(0, 0, -n).Format("2006-01-02") }

// claude は1呼び出しがモデル別行に割れる。行数で数えると呼び出し回数が水増しされるので、
// distinct call で数える（docs/46 §4）。どの軸で足しても合計が壊れないことまで見る。
func TestAggregateUsageRowsCountsDistinctCalls(t *testing.T) {
	rows := []usageRecord{
		row("c1", usageFeatureTitleSession, session.KindClaude, "claude-haiku-4-5", 100),
		row("c1", usageFeatureTitleSession, session.KindClaude, "claude-sonnet-4-6", 50), // 同じ呼び出しの2モデル目
		row("c2", usageFeatureTitleSession, session.KindClaude, "claude-haiku-4-5", 20),
	}
	agg := aggregateUsageRows(rows, map[string]bool{})
	if len(agg) != 2 {
		t.Fatalf("キー数 = %d, want 2（モデル別）", len(agg))
	}
	total, calls := 0, 0
	for _, a := range agg {
		total += a.Spend
		calls += a.Calls
	}
	if total != 170 {
		t.Fatalf("spend 合計 = %d, want 170", total)
	}
	if calls != 2 {
		t.Fatalf("calls 合計 = %d, want 2（行数 3 ではなく distinct call）", calls)
	}
}

// 完了した日だけを畳む。当日は行がまだ増えるので raw のまま。
func TestEnsureUsageRollupsOnlyFoldsCompletedDays(t *testing.T) {
	useIsolatedUsageDir(t)
	yesterday, today := daysAgo(1), daysAgo(0)
	writeUsageDay(t, yesterday, at(row("a", usageFeatureTitleSession, session.KindClaude, "haiku", 10), yesterday))
	writeUsageDay(t, today, at(row("b", usageFeatureTitleSession, session.KindClaude, "haiku", 20), today))

	ensureUsageRollups()
	m := readUsageRollup(yesterday[:7])
	if _, ok := m.Days[yesterday]; !ok {
		t.Fatalf("昨日が畳まれていない: %+v", m.Days)
	}
	if _, ok := m.Days[today]; ok {
		t.Fatal("当日を畳んでしまった（まだ行が増える）")
	}

	// 冪等: 2回目で内容が変わらない。
	before, _ := json.Marshal(readUsageRollup(yesterday[:7]))
	ensureUsageRollups()
	after, _ := json.Marshal(readUsageRollup(yesterday[:7]))
	if string(before) != string(after) {
		t.Fatalf("2回目で rollup が変わった:\n%s\n%s", before, after)
	}
}

// rollup は raw が保持期間で消えた後も残る（無期限・ADR0029 §7-4）。
func TestRollupSurvivesRawPrune(t *testing.T) {
	dir := useIsolatedUsageDir(t)
	old := daysAgo(2)
	writeUsageDay(t, old, at(row("a", usageFeatureSession, session.KindClaude, "haiku", 500), old))
	ensureUsageRollups()
	if err := os.Remove(filepath.Join(dir, "raw", old+".jsonl")); err != nil {
		t.Fatal(err)
	}
	entries := readUsageRollup(old[:7]).Days[old].Entries
	if len(entries) != 1 || entries[0].Agg.Spend != 500 {
		t.Fatalf("raw 削除後の集計 = %+v", entries)
	}
	// クエリからも見えること（rollup が正の日は raw を読まない）。
	samples, _ := collectUsageSamples(time.Now().UTC().AddDate(0, 0, -3), time.Now().UTC(), "day")
	sum := 0
	for _, s := range samples {
		sum += s.Agg.Spend
	}
	if sum != 500 {
		t.Fatalf("クエリ経由の spend = %d, want 500", sum)
	}
}

// 畳んだ日を raw からも二重に読まない（rollup と raw が両方ある状態での回帰）。
func TestCollectUsageSamplesDoesNotDoubleCount(t *testing.T) {
	useIsolatedUsageDir(t)
	yesterday := daysAgo(1)
	writeUsageDay(t, yesterday, at(row("a", usageFeatureTitleSession, session.KindClaude, "haiku", 300), yesterday))
	ensureUsageRollups() // raw はそのまま残っている
	samples, _ := collectUsageSamples(time.Now().UTC().AddDate(0, 0, -2), time.Now().UTC(), "day")
	sum, calls := 0, 0
	for _, s := range samples {
		sum += s.Agg.Spend
		calls += s.Agg.Calls
	}
	if sum != 300 || calls != 1 {
		t.Fatalf("spend = %d / calls = %d, want 300 / 1（rollup と raw で二重計上していないか）", sum, calls)
	}
}

// ★実機で最初に踏んだ穴の回帰。セッション折り込みのバックフィルは、過去数か月分の行を
// **今日の raw ファイル**へ一度に書く。バケットを追記先のファイル日で刻むと、過去の消費が
// 全部「導入日」に積み上がって時系列が無意味になる。行の ts で刻むこと。
func TestUsageSeriesBucketsByConsumptionTimeNotFileDay(t *testing.T) {
	useIsolatedUsageDir(t)
	today := daysAgo(0)
	old1, old2 := daysAgo(40), daysAgo(41)
	// 3行とも「今日」のファイルに追記されるが、消費が起きたのは別の日。
	writeUsageDay(t, today,
		at(row("c1", usageFeatureSession, session.KindClaude, "haiku", 100), old1),
		at(row("c2", usageFeatureSession, session.KindClaude, "haiku", 200), old2),
		at(row("c3", usageFeatureSession, session.KindClaude, "haiku", 300), today),
	)
	got := getSeries(t, "from="+old2+"&to="+today)
	hit := nonEmptyBuckets(got)
	if len(hit) != 3 {
		t.Fatalf("消費のあるバケット数 = %d, want 3（消費日ごとに分かれるはず）: %+v", len(hit), got.Buckets)
	}
	// 期間はゼロ埋めして返す（41日空いていることが絵から消えない）。
	if len(got.Buckets) != 42 {
		t.Fatalf("bucket 数 = %d, want 42（%s〜%s のゼロ埋め）", len(got.Buckets), old2, today)
	}
	want := map[string]int{
		old2 + "T00:00:00Z":  200,
		old1 + "T00:00:00Z":  100,
		today + "T00:00:00Z": 300,
	}
	for _, b := range hit {
		if b.Series[usageFeatureSession].Spend != want[b.T] {
			t.Fatalf("bucket %s = %d, want %d", b.T, b.Series[usageFeatureSession].Spend, want[b.T])
		}
	}
	if got.Totals.Spend != 600 {
		t.Fatalf("totals = %+v", got.Totals)
	}
}

// 上と同じ形で、畳んだ後も消費日が保たれること（rollup のキーが ts の日であること）。
func TestRollupKeysByConsumptionDay(t *testing.T) {
	useIsolatedUsageDir(t)
	fileDay, consumed := daysAgo(1), daysAgo(30)
	writeUsageDay(t, fileDay, at(row("c1", usageFeatureSession, session.KindClaude, "haiku", 700), consumed))
	ensureUsageRollups()
	m := readUsageRollup(consumed[:7])
	day, ok := m.Days[consumed]
	if !ok {
		t.Fatalf("消費日 %s のキーが無い: %+v", consumed, m.Days)
	}
	if len(day.Entries) != 1 || day.Entries[0].Agg.Spend != 700 {
		t.Fatalf("entries = %+v", day.Entries)
	}
	if len(day.Src) != 1 || day.Src[0] != fileDay {
		t.Fatalf("寄与元ファイル日が記録されていない: %+v", day.Src)
	}
	// やり直しても足し込まない（Src が弾く＝クラッシュ後の再実行が安全）。
	if merged, ok := mergeRollupDay(day, fileDay, map[usageKey]usageAgg{{Kind: "x"}: {Spend: 1}}); ok {
		t.Fatalf("同じファイル日を二度足してしまった: %+v", merged)
	}
}

func TestParseUsageFilter(t *testing.T) {
	f, bad := parseUsageFilter("kind:claude,feature:title.*")
	if bad != "" {
		t.Fatalf("bad = %q", bad)
	}
	// 違う軸は AND
	if !f.match(usageKey{Kind: "claude", Feature: "title.session"}) {
		t.Fatal("claude かつ title.* が一致しない")
	}
	if f.match(usageKey{Kind: "codex", Feature: "title.session"}) {
		t.Fatal("kind が違うのに一致した")
	}
	if f.match(usageKey{Kind: "claude", Feature: "compact"}) {
		t.Fatal("feature が違うのに一致した")
	}
	// 同じ軸は OR
	f2, _ := parseUsageFilter("kind:claude,kind:codex")
	if !f2.match(usageKey{Kind: "codex"}) || !f2.match(usageKey{Kind: "claude"}) {
		t.Fatal("同じ軸の複数指定が OR になっていない")
	}
	if f2.match(usageKey{Kind: "cursor"}) {
		t.Fatal("列挙外が一致した")
	}
	// 未知の軸はエラーにする（黙って無視すると「指定したのに効かない」が起きる）
	if _, bad := parseUsageFilter("nope:1"); bad != "nope:1" {
		t.Fatalf("未知の軸を弾いていない: %q", bad)
	}
}

// nonEmptyBuckets は実データの乗ったバケットだけを返す。応答は要求期間をゼロ埋めする
// （空バケットを落とすと離れた日が隣接した棒として描かれ、時間軸として読めない）ので、
// 「何日分の消費があったか」を見るテストはこちらを使う。
func nonEmptyBuckets(r usageSeriesResp) []usageBucketWire {
	var out []usageBucketWire
	for _, b := range r.Buckets {
		if len(b.Series) > 0 {
			out = append(out, b)
		}
	}
	return out
}

func getSeries(t *testing.T, query string) usageSeriesResp {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/usage/series?"+query, nil)
	rec := httptest.NewRecorder()
	handleUsageSeries(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got usageSeriesResp
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v — body=%s", err, rec.Body.String())
	}
	return got
}

func TestUsageSeriesAggregation(t *testing.T) {
	useIsolatedUsageDir(t)
	day := daysAgo(1)
	writeUsageDay(t, day,
		at(row("c1", usageFeatureTitleSession, session.KindClaude, "claude-haiku-4-5", 100), day),
		at(row("c2", usageFeatureAssistantChat, session.KindClaude, "claude-sonnet-4-6", 900), day),
		at(row("c3", usageFeatureSession, session.KindCodex, "", 5000), day),
		// モデルもトークンも報告しない CLI: 回数だけ数える。
		usageRecord{TS: day + "T12:00:00Z", Call: "c4", Feature: usageFeatureTitleSession,
			Kind: session.KindAgy, ModelSrc: usageModelUnknown, OK: true, Measured: usageMeasuredNone},
	)

	got := getSeries(t, "from="+day+"&to="+day+"&by=feature")
	if len(got.Buckets) != 1 || got.Buckets[0].T != day+"T00:00:00Z" {
		t.Fatalf("buckets = %+v", got.Buckets)
	}
	if got.Totals.Spend != 6000 || got.Totals.Calls != 4 {
		t.Fatalf("totals = %+v", got.Totals)
	}
	s := got.Buckets[0].Series
	if s[usageFeatureSession].Spend != 5000 || s[usageFeatureAssistantChat].Spend != 900 {
		t.Fatalf("series = %+v", s)
	}
	if s[usageFeatureTitleSession].Calls != 2 { // claude 1 + agy 1
		t.Fatalf("title.session calls = %d", s[usageFeatureTitleSession].Calls)
	}
	// 「0」と「未計測」を混同しない。
	if got.UnmeasuredCalls != 1 {
		t.Fatalf("unmeasured_calls = %d, want 1", got.UnmeasuredCalls)
	}
	// coverage はデータから自動生成する（手書きの表はドリフトする）。
	if got.Coverage[session.KindClaude].Tokens != usageMeasuredExact ||
		got.Coverage[session.KindClaude].Model != usageModelReported {
		t.Fatalf("claude coverage = %+v", got.Coverage[session.KindClaude])
	}
	if got.Coverage[session.KindAgy].Tokens != usageMeasuredNone ||
		got.Coverage[session.KindAgy].Model != "none" {
		t.Fatalf("agy coverage = %+v", got.Coverage[session.KindAgy])
	}

	// include=aux でセッション本体を外せる（§9-3 の「含めてフィルタで絞る」形）。
	aux := getSeries(t, "from="+day+"&to="+day+"&include=aux")
	if aux.Totals.Spend != 1000 {
		t.Fatalf("include=aux totals = %+v", aux.Totals)
	}
	only := getSeries(t, "from="+day+"&to="+day+"&include=session")
	if only.Totals.Spend != 5000 {
		t.Fatalf("include=session totals = %+v", only.Totals)
	}

	// 「機能 × モデル」の表（本命のビュー）。
	mx := getSeries(t, "from="+day+"&to="+day+"&by=feature&split=model")
	if mx.Matrix[usageFeatureAssistantChat]["claude-sonnet-4-6"].Spend != 900 {
		t.Fatalf("matrix = %+v", mx.Matrix)
	}
	if _, ok := mx.Matrix[usageFeatureTitleSession][""]; !ok {
		t.Fatalf("モデル不明（agy）の枠が表に出ていない: %+v", mx.Matrix[usageFeatureTitleSession])
	}

	// フィルタ（前方一致）。
	f := getSeries(t, "from="+day+"&to="+day+"&filter=kind:claude")
	if f.Totals.Spend != 1000 || len(f.Coverage) != 1 {
		t.Fatalf("filter=kind:claude → totals %+v coverage %+v", f.Totals, f.Coverage)
	}
}

func TestUsageSeriesHourBucket(t *testing.T) {
	useIsolatedUsageDir(t)
	day := daysAgo(0) // 当日は raw のまま＝時間粒度が取れる
	r1 := row("c1", usageFeatureAssistantChat, session.KindClaude, "haiku", 10)
	r1.TS = day + "T01:30:00Z"
	r2 := row("c2", usageFeatureAssistantChat, session.KindClaude, "haiku", 20)
	r2.TS = day + "T01:45:00Z"
	r3 := row("c3", usageFeatureAssistantChat, session.KindClaude, "haiku", 40)
	r3.TS = day + "T05:00:00Z"
	writeUsageDay(t, day, r1, r2, r3)

	got := getSeries(t, "from="+day+"&to="+day+"&bucket=hour")
	hit := nonEmptyBuckets(got)
	if len(hit) != 2 {
		t.Fatalf("消費のあるバケット = %+v", got.Buckets)
	}
	if hit[0].T != day+"T01:00:00Z" || hit[0].Series[usageFeatureAssistantChat].Spend != 30 {
		t.Fatalf("bucket0 = %+v", hit[0])
	}
	if hit[1].T != day+"T05:00:00Z" {
		t.Fatalf("bucket1 = %+v", hit[1])
	}
	// 1日分の hour バケットはゼロ埋めして 24 本（空き時間が絵から消えない）。
	if len(got.Buckets) != 24 {
		t.Fatalf("bucket 数 = %d, want 24（ゼロ埋め）", len(got.Buckets))
	}
	if got.Totals.Spend != 70 {
		t.Fatalf("totals = %+v", got.Totals)
	}
}

// 畳んだ日を hour で要求されたら「消費が無かった」ではなく truncated と言う。
func TestUsageSeriesHourReportsTruncationAfterPrune(t *testing.T) {
	dir := useIsolatedUsageDir(t)
	old := daysAgo(2)
	writeUsageDay(t, old, at(row("a", usageFeatureAssistantChat, session.KindClaude, "haiku", 100), old))
	ensureUsageRollups()
	if err := os.Remove(filepath.Join(dir, "raw", old+".jsonl")); err != nil {
		t.Fatal(err)
	}
	got := getSeries(t, "from="+old+"&to="+old+"&bucket=hour")
	if !got.Truncated {
		t.Fatal("raw が消えた期間を hour で要求したのに truncated が立っていない")
	}
	if hit := nonEmptyBuckets(got); len(hit) != 0 {
		t.Fatalf("消費のあるバケットが出た: %+v", hit)
	}
}

func TestUsageSeriesRejectsBadParams(t *testing.T) {
	useIsolatedUsageDir(t)
	for _, q := range []string{
		"bucket=week", "by=nope", "split=nope", "filter=nope:1",
		"from=2026-13-99", "to=nope", "from=2026-07-10&to=2026-07-01", "include=nothing",
	} {
		req := httptest.NewRequest(http.MethodGet, "/usage/series?"+q, nil)
		rec := httptest.NewRecorder()
		handleUsageSeries(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%q: status = %d, want 400 (body=%s)", q, rec.Code, rec.Body.String())
		}
	}
}

// 集計 API は生ログを返さない（本文は元々記録していないが、セッション名や会話 id も
// バケットの外へ出さない）。
func TestUsageSeriesDoesNotLeakRefs(t *testing.T) {
	useIsolatedUsageDir(t)
	day := daysAgo(0)
	r := at(row("c1", usageFeatureTitleSession, session.KindClaude, "haiku", 10), day)
	r.Ref = "slot-secret"
	writeUsageDay(t, day, r)
	req := httptest.NewRequest(http.MethodGet, "/usage/series?from="+day+"&to="+day, nil)
	rec := httptest.NewRecorder()
	handleUsageSeries(rec, req)
	if body := rec.Body.String(); strings.Contains(body, "slot-secret") {
		t.Fatalf("ref が応答に漏れている: %s", body)
	}
}

// セッション本体の折り込みは非同期なので、走っている間の応答は直近ターンを含まない。
// **その事実を folding で申告する。** 黙って古い数字を返すと、Console は最新のつもりで
// 描き、利用者は当たるまで「再取得」を連打することになる（実際の苦情がこれ）。
func TestUsageSeriesReportsFolding(t *testing.T) {
	useIsolatedUsageDir(t)
	resetUsageFold(t)
	day := daysAgo(0)

	// 1本目の読み出しが折り込みを起動する＝この応答は「まだ追いついていない」。
	if got := getSeries(t, "from="+day+"&to="+day); !got.Folding {
		t.Fatal("折り込みを起動した応答が folding を立てていない")
	}
	waitUsageFoldIdle(t)

	// 折り込みが終わった後の読み出しはスロットルに当たる＝走っていない。ここで folding を
	// 立て続けると Console の自動取り直しが止まらない。
	if got := getSeries(t, "from="+day+"&to="+day); got.Folding {
		t.Fatal("折り込みが終わっているのに folding が立っている")
	}

	// 明示的な再取得（fold=force）はスロットルを飛ばして必ず走らせる。
	if got := getSeries(t, "from="+day+"&to="+day+"&fold=force"); !got.Folding {
		t.Fatal("fold=force がスロットルに当たって起動しなかった")
	}
	waitUsageFoldIdle(t)
}

// --- レビュー P2/P3 の回帰 -----------------------------------------------------

// modelRow は claude の「1呼び出しがモデル別行に割れた」1行。
func modelRow(call, modelRaw, model string, spend int) usageRecord {
	r := row(call, usageFeatureAssistantChat, session.KindClaude, model, spend)
	r.ModelRaw = modelRaw
	return r
}

// P2-5: 呼び出し回数は「その call で最も食ったモデル行」に付く。行の並びは生 id の
// 綴り順でしかないので、先頭行で数えると主力モデルが calls=0 と出る。
func TestCallsGoToTheDominantModelRow(t *testing.T) {
	day := daysAgo(0)
	rows := []usageRecord{
		// 綴り順では a-model が先、実際に食ったのは z-model。
		at(modelRow("c1", "a-model-20260101", "a-model", 100), day),
		at(modelRow("c1", "z-model-20260101", "z-model", 9000), day),
	}
	agg := aggregateUsageRows(rows, map[string]bool{})
	byModel := map[string]usageAgg{}
	calls := 0
	for k, a := range agg {
		byModel[k.Model] = a
		calls += a.Calls
	}
	if calls != 1 {
		t.Fatalf("calls 合計 = %d, want 1（distinct call）", calls)
	}
	if byModel["z-model"].Calls != 1 || byModel["a-model"].Calls != 0 {
		t.Fatalf("主力モデルに回数が付いていない: %+v", byModel)
	}
	// 同点は決定的に決める（同じ入力から同じ帰属＝集計が再現する）。
	tie := []usageRecord{
		at(modelRow("c2", "b-raw", "b", 50), day),
		at(modelRow("c2", "a-raw", "a", 50), day),
	}
	for i := 0; i < 3; i++ {
		got := aggregateUsageRows(tie, map[string]bool{})
		for k, a := range got {
			if a.Calls == 1 && k.Model != "a" {
				t.Fatalf("同点の代表が model=%q になった（生 id 昇順で決めるはず）", k.Model)
			}
		}
	}
}

// 同じことを API 越しに。`by=model` で主力モデルの calls が 0 に見えないこと。
func TestUsageSeriesCallsFollowDominantModel(t *testing.T) {
	useIsolatedUsageDir(t)
	day := daysAgo(0)
	writeUsageDay(t, day,
		at(modelRow("c1", "a-model-20260101", "a-model", 100), day),
		at(modelRow("c1", "z-model-20260101", "z-model", 9000), day),
	)
	got := getSeries(t, "from="+day+"&to="+day+"&by=model")
	series := nonEmptyBuckets(got)[0].Series
	if series["z-model"].Calls != 1 || series["a-model"].Calls != 0 {
		t.Fatalf("by=model の calls = %+v", series)
	}
	if got.Totals.Calls != 1 || got.Totals.Spend != 9100 {
		t.Fatalf("totals = %+v, want calls 1 / spend 9100", got.Totals)
	}
}

// P2-4: 月ファイルが書けなかったら state を進めない。進めると、その月へ寄与するはずだった
// 消費は「畳み済み」扱いのまま集計から消え、raw が prune された時点で二度と戻らない。
func TestRollupKeepsStateWhenMonthWriteFails(t *testing.T) {
	useIsolatedUsageDir(t)
	day := daysAgo(1)
	writeUsageDay(t, day, at(row("c1", usageFeatureSession, session.KindClaude, "haiku", 500), day))
	// 月ファイルの置き場所をディレクトリで塞ぐ（rename が必ず失敗する）。
	blocked := usageRollupPath(day[:7])
	if err := os.MkdirAll(filepath.Join(blocked, "x"), 0o700); err != nil {
		t.Fatal(err)
	}

	ensureUsageRollups()
	if st := readUsageRollupState(); len(st.Rolled) != 0 {
		t.Fatalf("月ファイルを書けていないのに畳み済みにした: %+v", st.Rolled)
	}

	// 塞ぎを外せば次のパスで畳み直せる（取りこぼしが復旧後に回収される）。
	if err := os.RemoveAll(blocked); err != nil {
		t.Fatal(err)
	}
	ensureUsageRollups()
	entries := readUsageRollup(day[:7]).Days[day].Entries
	if len(entries) != 1 || entries[0].Agg.Spend != 500 {
		t.Fatalf("復旧後の集計 = %+v, want spend 500", entries)
	}
	if _, ok := readUsageRollupState().Rolled[day]; !ok {
		t.Fatal("復旧後に畳み済みとして記録されていない")
	}
}

// P3-12: 追記と畳み込みが別ロックなので、UTC 日跨ぎ直前に日を決めた追記が畳み込みの後に
// 着地しうる。畳む側は usageMu を保持して「その日はもう伸びないか」を確かめてから読む。
func TestRollupRefusesToReadTheOpenDay(t *testing.T) {
	useIsolatedUsageDir(t)
	today, yesterday := daysAgo(0), daysAgo(1)
	writeUsageDay(t, today, at(row("c1", usageFeatureSession, session.KindClaude, "haiku", 10), today))
	writeUsageDay(t, yesterday, at(row("c2", usageFeatureSession, session.KindClaude, "haiku", 20), yesterday))

	if rows, closed := readUsageDayForRollup(today); closed || len(rows) != 0 {
		t.Fatalf("当日のファイルを畳み込み対象として読んでしまった（rows=%d closed=%v）", len(rows), closed)
	}
	rows, closed := readUsageDayForRollup(yesterday)
	if !closed || len(rows) != 1 {
		t.Fatalf("完了した日を読めていない（rows=%d closed=%v）", len(rows), closed)
	}
}

// P3-9: 消費の無い日もゼロで埋めて返す。落とすと「離れた2日」が隣り合う棒になり、
// 空白期間が絵から消える。
func TestUsageSeriesFillsEmptyBuckets(t *testing.T) {
	useIsolatedUsageDir(t)
	from, gap, to := daysAgo(4), daysAgo(2), daysAgo(0)
	writeUsageDay(t, to,
		at(row("c1", usageFeatureSession, session.KindClaude, "haiku", 100), from),
		at(row("c2", usageFeatureSession, session.KindClaude, "haiku", 200), to),
	)
	got := getSeries(t, "from="+from+"&to="+to)
	if len(got.Buckets) != 5 {
		t.Fatalf("bucket 数 = %d, want 5（%s〜%s の全日）", len(got.Buckets), from, to)
	}
	if len(nonEmptyBuckets(got)) != 2 {
		t.Fatalf("消費のあるバケット = %+v", nonEmptyBuckets(got))
	}
	var mid *usageBucketWire
	for i, b := range got.Buckets {
		if b.T == gap+"T00:00:00Z" {
			mid = &got.Buckets[i]
		}
	}
	if mid == nil || len(mid.Series) != 0 {
		t.Fatalf("空の日が位置として残っていない: %+v", got.Buckets)
	}
	// 埋めても合計は動かない。
	if got.Totals.Spend != 300 || got.Totals.Calls != 2 {
		t.Fatalf("totals = %+v", got.Totals)
	}
}

// 埋めるのが無意味な密度（90日 × hour）では埋めない。切り詰めるのではなく埋めるのをやめる
// ＝実データは必ず全部返る。
func TestUsageSeriesDoesNotFillAbsurdRanges(t *testing.T) {
	useIsolatedUsageDir(t)
	day := daysAgo(0)
	writeUsageDay(t, day, at(row("c1", usageFeatureSession, session.KindClaude, "haiku", 10), day))
	got := getSeries(t, "from="+daysAgo(89)+"&to="+day+"&bucket=hour")
	if len(got.Buckets) != 1 {
		t.Fatalf("bucket 数 = %d, want 1（上限を超える密度は埋めない）", len(got.Buckets))
	}
	if got.Totals.Spend != 10 {
		t.Fatalf("totals = %+v", got.Totals)
	}
}
