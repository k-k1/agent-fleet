package main

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// useTempUsageDir は台帳を一時ディレクトリへ向け、prune の節流状態も畳んでおく
// （プロセス内グローバルなので、テスト間で持ち越すと prune が走らない/走りすぎる）。
func useTempUsageDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("AF_USAGE_DIR", dir)
	usageMu.Lock()
	usagePrunedAt = time.Time{}
	usageMu.Unlock()
	usageFoldGate.Lock()
	usageFoldedAt = time.Time{}
	usageFoldGate.Unlock()
	usageFoldRunning.Store(false)
	return dir
}

// useIsolatedUsageDir は台帳に加えて HOME も差し替える。集計 API のテストはハンドラ経由で
// fold-on-read を踏むので、HOME を分けないと**実ワークスペースのセッションを畳んで**
// 期待値が実データで壊れる（実際に踏んだ — 合計が数百万トークンになった）。実 CLI を撃つ
// ライブテストでは使えない（認証が HOME 配下にある）ので、そちらは useTempUsageDir のまま。
// **HOME だけでは足りない**（memory: ci-runner-xdg-not-home）: 単価カタログは
// XDG_CACHE_HOME 配下の opencode キャッシュも見るので、そこも差し替えないと
// 開発機に置かれた実カタログで期待値が動く（＝環境を検査するテストになる）。
func useIsolatedUsageDir(t *testing.T) string {
	t.Helper()
	dir := useTempUsageDir(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("AF_USAGE_CATALOG", "")
	resetUsageCatalogCache(t)
	return dir
}

// resetUsageCatalogCache はプロセス内キャッシュを捨てる（前のテストのカタログが
// 次のテストへ漏れない）。前後どちらでも効くように Cleanup にも積む。
func resetUsageCatalogCache(t *testing.T) {
	t.Helper()
	clear := func() {
		usageCatalogMu.Lock()
		usageCatalogCache, usageCatalogSrc = nil, ""
		usageCatalogAt, usageCatalogChecked = time.Time{}, time.Time{}
		usageCatalogMu.Unlock()
	}
	clear()
	t.Cleanup(clear)
}

func TestRecordUsageCallSplitsClaudeModelRows(t *testing.T) {
	useTempUsageDir(t)
	ctx := withUsageTag(context.Background(), usageTag{
		Feature: usageFeatureTitleSession, Trigger: usageTriggerAuto, Ref: "slot01",
	})
	call := usageCall{
		Kind: session.KindClaude, ModelReq: "haiku", OK: true,
		CostUSD: 0.0084,
		Models: usageModelRows(map[string]claudeModelUsage{
			"claude-haiku-4-5-20251001": {
				InputTokens: 2, OutputTokens: 5, CacheCreationInputTokens: 4186,
				CostUSD: 0.0084, CanonicalModel: "claude-haiku-4-5",
			},
			"claude-sonnet-4-6-20260101": {
				InputTokens: 10, OutputTokens: 20, CacheReadInputTokens: 100,
				CostUSD: 0.02, CanonicalModel: "claude-sonnet-4-6",
			},
		}),
	}
	recordUsageCall(ctx, &call, time.Now())

	rows := readUsageRows()
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 (model 毎に1行)", len(rows))
	}
	// 1呼び出しが複数モデルに割れても呼び出しは1回 — call が束ねる（集計の calls は distinct call）。
	if rows[0].Call != rows[1].Call || rows[0].Call == "" {
		t.Fatalf("call ids = %q / %q, want the same non-empty id", rows[0].Call, rows[1].Call)
	}
	h := rows[0]
	if h.Model != "claude-haiku-4-5" || h.ModelRaw != "claude-haiku-4-5-20251001" {
		t.Fatalf("model = %q / raw %q", h.Model, h.ModelRaw)
	}
	if h.ModelSrc != usageModelReported || h.ModelReq != "haiku" {
		t.Fatalf("model_src = %q, model_req = %q", h.ModelSrc, h.ModelReq)
	}
	// spend = in + ccreate + out（cache_read を含めない）
	if want := 2 + 4186 + 5; h.Spend != want {
		t.Fatalf("spend = %d, want %d", h.Spend, want)
	}
	if h.CostUSD != 0.0084 || h.Measured != usageMeasuredExact || !h.OK {
		t.Fatalf("row = %+v", h)
	}
	if h.Feature != usageFeatureTitleSession || h.Trigger != usageTriggerAuto || h.Ref != "slot01" {
		t.Fatalf("tag not carried: %+v", h)
	}
}

// モデルを報告しない CLI は requested / default_unknown へ縮退する。既定モデル
// （通常フラッグシップ）で補助呼び出しが走っている状態を1列で見えるようにするのが狙い。
func TestRecordUsageCallModelFallback(t *testing.T) {
	useTempUsageDir(t)
	for _, tc := range []struct {
		name       string
		req        string
		wantModel  string
		wantSrc    string
		wantMeasrd string
	}{
		{"要求値あり", "gpt-5.4-mini", "gpt-5.4-mini", usageModelRequest, usageMeasuredExact},
		{"CLI 既定に委ねた", "", "", usageModelUnknown, usageMeasuredExact},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := useTempUsageDir(t)
			call := usageCall{Kind: session.KindCodex, ModelReq: tc.req, OK: true}
			call.setTotals(100, 20, 30, 40)
			recordUsageCall(context.Background(), &call, time.Now())
			rows := readUsageRows()
			if len(rows) != 1 {
				t.Fatalf("rows = %d in %s", len(rows), dir)
			}
			r := rows[0]
			if r.Model != tc.wantModel || r.ModelSrc != tc.wantSrc || r.Measured != tc.wantMeasrd {
				t.Fatalf("row = %+v", r)
			}
			// タグの無い呼び出しでも必ず1行残る（無記録＝見えない消費を作らない）。
			if r.Feature != usageFeatureUnknown {
				t.Fatalf("feature = %q, want %q", r.Feature, usageFeatureUnknown)
			}
			if want := 100 + 40 + 20; r.Spend != want {
				t.Fatalf("spend = %d, want %d", r.Spend, want)
			}
		})
	}
}

// トークンを報告しない CLI の 0 は「消費 0」ではない — measured=none で回数だけ数える。
func TestRecordUsageCallUnmeasuredCountsTheCall(t *testing.T) {
	useTempUsageDir(t)
	call := usageCall{Kind: session.KindAgy, Measured: usageMeasuredNone, OK: true}
	recordUsageCall(context.Background(), &call, time.Now())
	rows := readUsageRows()
	if len(rows) != 1 || rows[0].Measured != usageMeasuredNone || rows[0].Spend != 0 {
		t.Fatalf("rows = %+v", rows)
	}
}

// 失敗したターンも記録する（ok=false）。エラーで消えると「撃ったのに見えない」が生まれる。
func TestRecordUsageCallRecordsFailures(t *testing.T) {
	useTempUsageDir(t)
	call := usageCall{Kind: session.KindClaude, ModelReq: "haiku"} // OK は false のまま
	recordUsageCall(context.Background(), &call, time.Now())
	rows := readUsageRows()
	if len(rows) != 1 || rows[0].OK {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestUsageRecordingCanBeDisabled(t *testing.T) {
	useTempUsageDir(t)
	t.Setenv("AF_USAGE_RECORD", "0")
	call := usageCall{Kind: session.KindClaude, OK: true}
	call.setTotals(1, 2, 3, 4)
	recordUsageCall(context.Background(), &call, time.Now())
	if rows := readUsageRows(); len(rows) != 0 {
		t.Fatalf("rows = %d, want 0 when recording is off", len(rows))
	}
}

func TestPruneUsageRawDropsExpiredDays(t *testing.T) {
	dir := useTempUsageDir(t)
	t.Setenv("AF_USAGE_RETENTION_DAYS", "7")
	raw := filepath.Join(dir, "raw")
	if err := os.MkdirAll(raw, 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().AddDate(0, 0, -30).Format("2006-01-02") + ".jsonl"
	recent := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02") + ".jsonl"
	for _, n := range []string{old, recent, "notes.txt"} {
		if err := os.WriteFile(filepath.Join(raw, n), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	usageMu.Lock()
	pruneUsageRawLocked()
	usageMu.Unlock()
	if _, err := os.Stat(filepath.Join(raw, old)); !os.IsNotExist(err) {
		t.Fatalf("保持期間を過ぎた %s が残っている", old)
	}
	for _, keep := range []string{recent, "notes.txt"} {
		if _, err := os.Stat(filepath.Join(raw, keep)); err != nil {
			t.Fatalf("%s を消してしまった: %v", keep, err)
		}
	}
}

// ---- 折り込み（foldTurnRows）----

func asst(model string, in, out, read, create int, sidechain bool) transcript.Turn {
	return transcript.Turn{
		Role: "assistant", Model: model, InTok: in, OutTok: out,
		CacheRead: read, CacheCreate: create, Sidechain: sidechain, TS: "2026-07-26T00:00:00Z",
	}
}

func TestFoldTurnRowsMatchesAggregateUsage(t *testing.T) {
	// 同じイベント列を台帳と get_session_usage の両方に通し、spend の合計が一致することを
	// 見る（二つの画面で数字が食い違わないための回帰）。
	turns := []transcript.Turn{
		{Role: "user", Text: "1"},
		asst("claude-haiku-4-5", 100, 10, 5, 20, false),
		asst("claude-haiku-4-5", 150, 30, 5, 25, false), // 同一論理ターンの2イベント目
		{Role: "user", Text: "2", Source: turnSourceOperator},
		asst("claude-haiku-4-5", 200, 40, 10, 0, false),
		asst("claude-haiku-4-5", 300, 60, 0, 0, true), // サブエージェント（別グループ）
		{Role: "user", Text: "3"},
		asst("claude-haiku-4-5", 400, 80, 0, 0, false), // 末尾＝開いたまま
	}
	rows := foldTurnRows(turns, false)
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3（末尾の開いたターンは含めない）", len(rows))
	}
	// 入力は置換・出力は加算（aggregateUsage と同じ流儀）。
	if rows[0].Tokens.In != 150 || rows[0].Tokens.Out != 40 || rows[0].Tokens.CacheCreate != 25 {
		t.Fatalf("row0 = %+v", rows[0])
	}
	if !rows[2].Sidechain {
		t.Fatalf("サブエージェントのターンが sidechain で割れていない: %+v", rows[2])
	}
	if rows[1].Trigger != usageTriggerOperator {
		t.Fatalf("trigger = %q, want %q", rows[1].Trigger, usageTriggerOperator)
	}
	if rows[0].Idx != 1 || rows[1].Idx != 2 || rows[2].Idx != 3 {
		t.Fatalf("idx が通し番号でない: %+v", rows)
	}

	// 末尾まで含めれば cumulative（get_session_usage）の spend と一致する。
	all := foldTurnRows(turns, true)
	sum := 0
	for _, r := range all {
		sum += usageSpend(r.Tokens.In, r.Tokens.CacheCreate, r.Tokens.Out)
	}
	want := aggregateUsage(turns).Cumulative.Spend
	if sum != want {
		t.Fatalf("台帳 spend 合計 = %d, get_session_usage = %d", sum, want)
	}
	if len(all) != aggregateUsage(turns).Cumulative.Turns {
		t.Fatalf("論理ターン数 = %d, get_session_usage = %d", len(all), aggregateUsage(turns).Cumulative.Turns)
	}
}

// 折り込みは (session, idx) で冪等 — 同じ転写を何度読んでも行は増えない。
func TestFoldSessionUsageIsIdempotent(t *testing.T) {
	useTempUsageDir(t)
	turns := []transcript.Turn{
		{Role: "user", Text: "1"},
		asst("claude-haiku-4-5", 100, 10, 0, 20, false),
		{Role: "user", Text: "2"},
		asst("claude-haiku-4-5", 200, 20, 0, 0, false),
		{Role: "user", Text: "3"},
		asst("claude-haiku-4-5", 300, 30, 0, 0, false), // 開いたまま
	}
	m := session.Meta{Name: "slot01", Kind: session.KindClaude, Origin: session.OriginOperator, OriginConv: "a1b2c3d"}
	fold := func(includeTrailing bool) int {
		t.Helper()
		usageFoldMu.Lock()
		defer usageFoldMu.Unlock()
		st := readUsageFoldState()
		n, err := foldSessionUsageWithTurns(m, &st, turns, includeTrailing)
		if err != nil {
			t.Fatalf("折り込みに失敗: %v", err)
		}
		if err := writeUsageFoldState(st); err != nil {
			t.Fatalf("watermark の書き込みに失敗: %v", err)
		}
		return n
	}
	if n := fold(false); n != 2 {
		t.Fatalf("初回 = %d 行, want 2（末尾は開いている）", n)
	}
	if n := fold(false); n != 0 {
		t.Fatalf("再読み込みで %d 行増えた（冪等でない）", n)
	}
	// 末尾ターンが閉じた（＝新しいユーザーターンが来た）
	turns = append(turns, transcript.Turn{Role: "user", Text: "4"})
	if n := fold(false); n != 1 {
		t.Fatalf("閉じた末尾ターンの折り込み = %d 行, want 1", n)
	}
	rows := readUsageRows()
	if len(rows) != 3 {
		t.Fatalf("台帳 = %d 行, want 3", len(rows))
	}
	for i, r := range rows {
		if r.Feature != usageFeatureSession || r.Ref != "slot01" || r.Idx != i+1 {
			t.Fatalf("row%d = %+v", i, r)
		}
		// 出自は行へ焼き込む（セッションが消えても集計が壊れない）。
		if r.Origin != session.OriginOperator || r.OriginConv != "a1b2c3d" {
			t.Fatalf("row%d origin = %q/%q", i, r.Origin, r.OriginConv)
		}
		if r.ModelSrc != usageModelReported || r.Model != "claude-haiku-4-5" {
			t.Fatalf("row%d model = %q (%q)", i, r.Model, r.ModelSrc)
		}
	}
}

// TestFoldMatchesSessionUsageLive は実ワークスペースの実転写で、台帳へ折り込んだ合計が
// get_session_usage の cumulative と一致することを見る opt-in テスト（docs/46 P2 完了条件）。
// 合成ターンの単体テストでは、各 kind の実パーサが吐く形（sidechain の付き方・イベントの
// 刻み方）まではカバーできない。
// 実行例: AF_USAGE_FOLD_LIVE=1 go test -run TestFoldMatchesSessionUsageLive -v .
func TestFoldMatchesSessionUsageLive(t *testing.T) {
	if os.Getenv("AF_USAGE_FOLD_LIVE") != "1" {
		t.Skip("AF_USAGE_FOLD_LIVE=1 で実転写との突合を有効化")
	}
	useTempUsageDir(t)
	checked := 0
	for _, m := range session.ListMetas() {
		if !agentOf(m.Kind).Caps().CanTranscript {
			continue
		}
		turns := usageTurns(m)
		if len(turns) == 0 {
			continue
		}
		// 末尾まで含めた折り込み＝転写全体。cumulative と同じ範囲を見ていることになる。
		rows := foldTurnRows(turns, true)
		sum := 0
		for _, r := range rows {
			sum += usageSpend(r.Tokens.In, r.Tokens.CacheCreate, r.Tokens.Out)
		}
		cum := aggregateUsage(turns).Cumulative
		if sum != cum.Spend || len(rows) != cum.Turns {
			t.Errorf("%s(%s): 台帳 spend=%d turns=%d / get_session_usage spend=%d turns=%d",
				m.Name, m.Kind, sum, len(rows), cum.Spend, cum.Turns)
			continue
		}
		checked++
		t.Logf("%s(%s): turns=%d spend=%d 一致", m.Name, m.Kind, len(rows), cum.Spend)
	}
	if checked == 0 {
		t.Skip("転写を持つセッションがこのワークスペースに無い")
	}
	t.Logf("%d セッションで突合一致", checked)

	// 実データでのバックフィルと冪等性: 1回目で過去分がまとめて入り、2回目は増えない。
	n1 := foldAllSessionUsage()
	rows := readUsageRows()
	if n1 == 0 || len(rows) != n1 {
		t.Fatalf("初回バックフィル = %d 行 / 台帳 %d 行", n1, len(rows))
	}
	if n2 := foldAllSessionUsage(); n2 != 0 {
		t.Fatalf("2回目で %d 行増えた（watermark が効いていない）", n2)
	}
	t.Logf("バックフィル %d 行・2回目は 0 行（冪等）", n1)
}

func TestUsageMeasuredForKind(t *testing.T) {
	for kind, want := range map[string]string{
		session.KindClaude:  usageMeasuredExact,
		session.KindCodex:   usageMeasuredExact,
		session.KindCopilot: usageMeasuredPartial, // 転写に outTok しかない
		session.KindCursor:  usageMeasuredNone,    // 転写にトークンが無い
		session.KindKiro:    usageMeasuredNone,
		session.KindAgy:     usageMeasuredNone,
	} {
		if got := usageMeasuredForKind(kind); got != want {
			t.Errorf("%s: measured = %q, want %q", kind, got, want)
		}
	}
}

// ---- 出自（origin）----

func TestCreateOriginResolution(t *testing.T) {
	for _, tc := range []struct {
		name     string
		req      createReq
		want     string
		wantConv string
	}{
		{"Console（無印）", createReq{}, session.OriginUser, ""},
		{"MCP create_session", createReq{Origin: "operator", OriginConv: "conv-1"}, session.OriginOperator, "conv-1"},
		{"定時実行", createReq{Source: turnSourceSchedule}, session.OriginSchedule, ""},
		{"手動発火", createReq{Source: turnSourceScheduleManual}, session.OriginSchedule, ""},
		{"未知の値は user へ縮退", createReq{Origin: "hacked"}, session.OriginUser, ""},
		// origin=operator 以外では会話 slug に意味が無いので落とす。
		{"conv は operator の時だけ", createReq{Origin: "user", OriginConv: "conv-1"}, session.OriginUser, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, conv := createOrigin(&tc.req)
			if got != tc.want || conv != tc.wantConv {
				t.Fatalf("origin = %q/%q, want %q/%q", got, conv, tc.want, tc.wantConv)
			}
		})
	}
}

// 既存セッション（フィールド無し）は unknown。0 でも user でもない、を守る。
func TestOriginOfDefaultsToUnknown(t *testing.T) {
	if got := session.OriginOf(session.Meta{Name: "old"}); got != session.OriginUnknown {
		t.Fatalf("origin = %q, want %q", got, session.OriginUnknown)
	}
	if got := session.OriginOf(session.Meta{Name: "new", Origin: session.OriginSchedule}); got != session.OriginSchedule {
		t.Fatalf("origin = %q", got)
	}
}

func TestUsageTriggerFromTurnSource(t *testing.T) {
	for src, want := range map[string]string{
		"":                       usageTriggerUser,
		turnSourceOperator:       usageTriggerOperator,
		turnSourceDiscord:        usageTriggerBridge,
		turnSourceSlack:          usageTriggerBridge,
		turnSourceSchedule:       usageTriggerSchedule,
		turnSourceScheduleManual: usageTriggerSchedule,
	} {
		if got := usageTriggerFromTurnSource(src); got != want {
			t.Errorf("%q -> %q, want %q", src, got, want)
		}
	}
}

func TestCompactTriggerMapping(t *testing.T) {
	for reason, want := range map[string]string{
		compactReasonManual:   usageTriggerManual,
		compactReasonAuto:     usageTriggerAuto,
		compactReasonRecovery: usageTriggerRecovery,
	} {
		if got := compactTrigger(reason); got != want {
			t.Errorf("%q -> %q, want %q", reason, got, want)
		}
	}
}

// --- 折り込みの耐障害性（レビュー P1 の回帰）--------------------------------------

// 行が書けなかったら watermark を進めない。進めてしまうと、その分の消費は次のパスでも
// 差分に出てこず二度と台帳へ入らない（台帳側に取りこぼしを拾い直す口は無い）。
func TestFoldDoesNotAdvanceWatermarkWhenAppendFails(t *testing.T) {
	dir := useTempUsageDir(t)
	turns := []transcript.Turn{
		{Role: "user", Text: "1"},
		asst("claude-haiku-4-5", 100, 10, 0, 20, false),
		{Role: "user", Text: "2"},
		asst("claude-haiku-4-5", 200, 20, 0, 0, false),
		{Role: "user", Text: "3"}, // 末尾は閉じている＝2ターンとも折り込める
	}
	m := session.Meta{Name: "slot01", Kind: session.KindClaude}

	// raw/ の場所にファイルを置いて追記を失敗させる（MkdirAll が ENOTDIR で落ちる）。
	raw := filepath.Join(dir, "raw")
	if err := os.WriteFile(raw, []byte("not a dir"), 0o600); err != nil {
		t.Fatal(err)
	}
	st := readUsageFoldState()
	n, err := foldSessionUsageWithTurns(m, &st, turns, false)
	if err == nil {
		t.Fatal("追記が失敗したのに error が返っていない")
	}
	if n != 0 {
		t.Fatalf("失敗した折り込みが %d 行を報告した, want 0", n)
	}
	if mark, ok := st.Sessions[m.Name]; ok {
		t.Fatalf("追記に失敗したのに watermark が進んだ: %+v", mark)
	}

	// 書けるようになったら、取りこぼさず全部入る。
	if err := os.Remove(raw); err != nil {
		t.Fatal(err)
	}
	if n, err = foldSessionUsageWithTurns(m, &st, turns, false); err != nil || n != 2 {
		t.Fatalf("復旧後の折り込み = %d 行 / err=%v, want 2 / nil", n, err)
	}
	if rows := readUsageRows(); len(rows) != 2 {
		t.Fatalf("台帳 = %d 行, want 2", len(rows))
	}
	if st.Sessions[m.Name].Groups != 2 {
		t.Fatalf("watermark = %+v, want groups=2", st.Sessions[m.Name])
	}
}

// commitSessionUsageFold はセッション1件ごとに watermark まで書き切る。パス末尾で
// まとめて1回書いていた頃は、途中で落ちるとそれまでの全セッションが次回パスで重複した。
func TestCommitSessionUsageFoldPersistsWatermarkPerSession(t *testing.T) {
	useIsolatedUsageDir(t)
	m := session.Meta{Name: "slot01", Kind: session.KindShell} // 転写を持たない kind
	session.WriteMeta(m)
	if _, err := commitSessionUsageFold(m, false); err != nil {
		t.Fatalf("commit に失敗: %v", err)
	}
	// 転写が無いので行も watermark も増えない（空の state を書き散らかさない）。
	if rows := readUsageRows(); len(rows) != 0 {
		t.Fatalf("台帳 = %d 行, want 0", len(rows))
	}

	// watermark を持つセッションは、その1件を畳んだ時点でディスクに落ちている。
	usageFoldMu.Lock()
	st := readUsageFoldState()
	st.Sessions["slot02"] = usageFoldMark{Groups: 3}
	if err := writeUsageFoldState(st); err != nil {
		t.Fatalf("watermark の書き込みに失敗: %v", err)
	}
	usageFoldMu.Unlock()
	if got := readUsageFoldState().Sessions["slot02"].Groups; got != 3 {
		t.Fatalf("再読み込みした watermark = %d, want 3", got)
	}
}

// fold-on-read は「走行中か?」を聞くだけで、折り込み本体のロックを待ってはいけない。
// 待つと Console の使用量ビュー（1画面で /usage/series を3本撃つ）の2本目以降が、
// 実測 ~20 秒の一括折り込みが終わるまで丸ごとブロックされる。
func TestFoldOnReadDoesNotBlockOnRunningPass(t *testing.T) {
	useIsolatedUsageDir(t)
	usageFoldMu.Lock() // 走行中の折り込みが握っているロックを模す
	done := make(chan struct{})
	go func() {
		maybeFoldSessionUsage()
		close(done)
	}()
	select {
	case <-done:
		usageFoldMu.Unlock()
	case <-time.After(2 * time.Second):
		usageFoldMu.Unlock()
		t.Fatal("fold-on-read が折り込み本体のロックを待ってブロックした")
	}
	// 起動した非同期パスを回収してから抜ける（グローバル状態を次のテストへ持ち越さない）。
	for i := 0; usageFoldRunning.Load() && i < 200; i++ {
		time.Sleep(10 * time.Millisecond)
	}
}

// waitUsageFoldIdle は走行中の一括折り込みを回収する。グローバル（走行中フラグ・
// スロットル時刻）を共有するので、跨いで漏らすと次のテストが理由もなく skip される。
func waitUsageFoldIdle(t *testing.T) {
	t.Helper()
	for i := 0; usageFoldRunning.Load() && i < 500; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if usageFoldRunning.Load() {
		t.Fatal("一括折り込みが終わらない")
	}
}

// resetUsageFold は fold-on-read のスロットルを未使用の状態へ戻す（前後とも）。
func resetUsageFold(t *testing.T) {
	t.Helper()
	clear := func() {
		usageFoldGate.Lock()
		usageFoldedAt = time.Time{}
		usageFoldGate.Unlock()
	}
	waitUsageFoldIdle(t)
	clear()
	t.Cleanup(func() {
		waitUsageFoldIdle(t)
		clear()
	})
}

// 「再取得」はスロットルを飛ばせなければならない。飛ばせないと、押した時点で既に終わって
// いるターンが最大1分ぶん入ってこず、利用者は最新になるまでボタンを押し続けることになる
// （＝この画面で実際に起きていた「何度か押すまで最新にならない」）。
func TestStartFoldSessionUsageForceSkipsThrottle(t *testing.T) {
	useIsolatedUsageDir(t)
	resetUsageFold(t)

	if !startFoldSessionUsage(false) {
		t.Fatal("最初の fold-on-read が起動しなかった")
	}
	waitUsageFoldIdle(t)

	// 直後の通常読み出しはスロットルで見送る。走ってもいないので folding は立てない
	// （立てると Console が永久に取り直す）。
	if startFoldSessionUsage(false) {
		t.Fatal("スロットル中の読み出しが「折り込み中」を申告した")
	}
	// force はそのスロットルだけを飛ばす（多重起動ガードは残る）。
	if !startFoldSessionUsage(true) {
		t.Fatal("force がスロットルに当たって起動しなかった")
	}
	waitUsageFoldIdle(t)
}

// finalizeSessionUsage は消えるセッションの watermark を忘れる。残すと state.json が
// 「もう存在しないセッション」の分だけ単調に増える。
func TestFinalizeSessionUsageForgetsWatermark(t *testing.T) {
	useIsolatedUsageDir(t)
	m := session.Meta{Name: "slot01", Kind: session.KindShell}
	usageFoldMu.Lock()
	st := readUsageFoldState()
	st.Sessions[m.Name] = usageFoldMark{Groups: 5}
	if err := writeUsageFoldState(st); err != nil {
		t.Fatalf("watermark の書き込みに失敗: %v", err)
	}
	usageFoldMu.Unlock()

	finalizeSessionUsage(m)
	if mark, ok := readUsageFoldState().Sessions[m.Name]; ok {
		t.Fatalf("削除後も watermark が残っている: %+v", mark)
	}
}

// meta を忘れる経路は、忘れる前に必ず使用量を確定させること。忘れた瞬間に ListMetas から
// 外れて二度と折り込まれないので、開いた末尾ターンがここで確定しないと永久に台帳へ入らない。
// 実際に handleStopSession（Console の「削除」）がこれを落としていた。
func TestMetaRemovalPathsFinalizeUsage(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	checked := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, d := range af.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			removes, finalizes := false, false
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch fn := call.Fun.(type) {
				case *ast.SelectorExpr:
					if x, ok := fn.X.(*ast.Ident); ok && x.Name == "session" && fn.Sel.Name == "RemoveMeta" {
						removes = true
					}
				case *ast.Ident:
					if fn.Name == "finalizeSessionUsage" {
						finalizes = true
					}
				}
				return true
			})
			if !removes {
				continue
			}
			checked++
			if !finalizes {
				t.Errorf("%s: %s が session.RemoveMeta を呼ぶのに finalizeSessionUsage を呼んでいない"+
					"（docs/46 §3-b: meta を忘れる前に末尾ターンを確定させる）", name, fd.Name.Name)
			}
		}
	}
	if checked == 0 {
		t.Fatal("session.RemoveMeta の呼び出しを1つも見つけられなかった（走査が壊れている）")
	}
	t.Logf("meta 削除経路 %d 件を確認", checked)
}

// asstAt は時刻を指定した assistant イベント（転写の入れ替わりを組み立てるため）。
func asstAt(ts string, in, out int) transcript.Turn {
	t := asst("claude-haiku-4-5", in, out, 0, 0, false)
	t.TS = ts
	return t
}

// P2-8: claude は1つの sid に兄弟 jsonl を持ちうり、読む1本は mtime で選ばれる
// （internal/agents/claude/transcript.go）。件数だけで差分を採ると、選択が入れ替わった
// 瞬間に **折り込みが永久に止まる**（短い方へ）か、**別の会話の先頭ターンを既折り込み扱いで
// 落とす**（長い方へ）。件数と時刻の両方で判定すること。
func TestFoldSurvivesTranscriptFileSwitch(t *testing.T) {
	useTempUsageDir(t)
	m := session.Meta{Name: "slot01", Kind: session.KindClaude}
	fold := func(turns []transcript.Turn, st *usageFoldState) int {
		t.Helper()
		n, err := foldSessionUsageWithTurns(m, st, turns, false)
		if err != nil {
			t.Fatalf("折り込みに失敗: %v", err)
		}
		return n
	}
	st := readUsageFoldState()

	// 本体の転写: 3論理ターン（末尾のユーザーターンで閉じている）。
	main3 := []transcript.Turn{
		{Role: "user"}, asstAt("2026-07-26T01:00:00Z", 100, 10),
		{Role: "user"}, asstAt("2026-07-26T02:00:00Z", 200, 20),
		{Role: "user"}, asstAt("2026-07-26T03:00:00Z", 300, 30),
		{Role: "user"},
	}
	if n := fold(main3, &st); n != 3 {
		t.Fatalf("初回 = %d 行, want 3", n)
	}

	// mtime が兄弟ファイル（短い・**より新しい**）へ振れた。件数ベースだとここで永久に止まる。
	sibling := []transcript.Turn{
		{Role: "user"}, asstAt("2026-07-26T04:00:00Z", 400, 40),
		{Role: "user"}, asstAt("2026-07-26T05:00:00Z", 500, 50),
		{Role: "user"},
	}
	if n := fold(sibling, &st); n != 2 {
		t.Fatalf("入れ替わった転写の新しいターン = %d 行, want 2（件数だけで見ると 0 になる）", n)
	}
	if got := st.Sessions[m.Name]; got.Groups != 3 || got.LastTS != "2026-07-26T05:00:00Z" {
		t.Fatalf("watermark = %+v, want groups 3（下げない）/ lastTS 05:00", got)
	}

	// 古い兄弟（スタブ）へ振れた時は何も拾わない — 済んだ会話を数え直さない。
	oldStub := []transcript.Turn{
		{Role: "user"}, asstAt("2026-07-25T01:00:00Z", 900, 90),
		{Role: "user"},
	}
	if n := fold(oldStub, &st); n != 0 {
		t.Fatalf("古い転写から %d 行を拾った, want 0", n)
	}

	// 本体が伸びたら続きだけを拾う（通常の差分折り込みは変わらない）。
	grown := append(append([]transcript.Turn{}, main3...),
		asstAt("2026-07-26T06:00:00Z", 600, 60), transcript.Turn{Role: "user"})
	if n := fold(grown, &st); n != 1 {
		t.Fatalf("伸びた本体の続き = %d 行, want 1", n)
	}
	rows := readUsageRows()
	if len(rows) != 6 {
		t.Fatalf("台帳 = %d 行, want 6", len(rows))
	}
	spend := 0
	for _, r := range rows {
		spend += r.Spend
	}
	if want := 110 + 220 + 330 + 440 + 550 + 660; spend != want {
		t.Fatalf("spend 合計 = %d, want %d", spend, want)
	}
}

// P3-10: 追記が途中まで書けた状態でプロセスが落ちても、復旧後に **過不足なく** 回収される。
// watermark を進めないので次回パスが同じターンを再追記し、集計側の (ref, idx) 重複排除が
// 重なりを落とす — 書き手と読み手の二段構えが噛み合っていることの回帰。
func TestFoldRecoversAfterPartialAppend(t *testing.T) {
	useIsolatedUsageDir(t)
	m := session.Meta{Name: "slot01", Kind: session.KindClaude}
	turns := []transcript.Turn{
		{Role: "user"}, asstAt("2026-07-26T01:00:00Z", 100, 10),
		{Role: "user"}, asstAt("2026-07-26T02:00:00Z", 200, 20),
		{Role: "user"},
	}
	// 1ターン目だけがディスクに載って落ちた状態を作る（watermark は書かれていない）。
	st := readUsageFoldState()
	partial := usageFoldState{Sessions: map[string]usageFoldMark{}}
	if _, err := foldSessionUsageWithTurns(m, &partial, turns[:2], true); err != nil {
		t.Fatal(err)
	}
	if n := len(readUsageRows()); n != 1 {
		t.Fatalf("前提が崩れている: 台帳 = %d 行, want 1", n)
	}

	// 復旧後のパスは watermark 0 から走るので、1ターン目を再追記する。
	if n, err := foldSessionUsageWithTurns(m, &st, turns, false); err != nil || n != 2 {
		t.Fatalf("復旧後の折り込み = %d 行 / err=%v, want 2 / nil", n, err)
	}
	if n := len(readUsageRows()); n != 3 {
		t.Fatalf("台帳 = %d 行, want 3（1件は重複として残る）", n)
	}
	got := getSeries(t, "from=2026-07-26&to=2026-07-26")
	if want := 110 + 220; got.Totals.Spend != want || got.Totals.Calls != 2 {
		t.Fatalf("集計 = %+v, want spend %d / calls 2（重複を数えていないか）", got.Totals, want)
	}
}
