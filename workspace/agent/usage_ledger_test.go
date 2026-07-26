package main

import (
	"context"
	"os"
	"path/filepath"
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
	usageFoldMu.Lock()
	usageFoldedAt = time.Time{}
	usageFoldMu.Unlock()
	return dir
}

// useIsolatedUsageDir は台帳に加えて HOME も差し替える。集計 API のテストはハンドラ経由で
// fold-on-read を踏むので、HOME を分けないと**実ワークスペースのセッションを畳んで**
// 期待値が実データで壊れる（実際に踏んだ — 合計が数百万トークンになった）。実 CLI を撃つ
// ライブテストでは使えない（認証が HOME 配下にある）ので、そちらは useTempUsageDir のまま。
func useIsolatedUsageDir(t *testing.T) string {
	t.Helper()
	dir := useTempUsageDir(t)
	t.Setenv("HOME", t.TempDir())
	return dir
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
		usageFoldMu.Lock()
		defer usageFoldMu.Unlock()
		st := readUsageFoldState()
		n := foldSessionUsageWithTurns(m, &st, turns, includeTrailing)
		writeUsageFoldState(st)
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
