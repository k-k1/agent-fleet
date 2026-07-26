package main

// セッション本体の使用量を転写から台帳へ折り込む（docs/46 §3-b / ADR0029 §5）。
//
// セッションの消費は別プロセス（CLI）が出すので、補助呼び出しのように実行時に記録できない。
// 転写を読んで差分だけを折り込み、watermark で冪等にする。
//
// 二重計上しない前提が2つある:
//  1. 折り込み対象は登録済みセッション（session.Meta）だけ。アシスタント会話は claude の
//     projects ツリーに転写を書くが、Meta を持たないので混ざらない。
//  2. 「開いている末尾の論理ターン」は折り込まない。折り込んだ後に同じターンへイベントが
//     追加されると、入力スナップショット（置換セマンティクス）を二重に数えてしまう。
//     次のユーザーターンで閉じた時か、セッション削除時（includeTrailing）に確定させる。
//
// 常駐タイマーは増やさない（メモリ制約ホスト・docs/26 の教訓）。契機は GET /sessions/usage
// を間借りした fold-on-read（60 秒スロットル）と、削除時の確定。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// usageFoldMark は1セッション分の watermark。
type usageFoldMark struct {
	// Groups は折り込み済みの論理ターン数。転写は追記のみなので、論理ターンの通し番号は
	// kind に依らず安定で、(session, idx) の冪等キーになる。各 kind の Turn.Idx（行番号）を
	// 使わないのは、番号体系が kind ごとに違って watermark が混ざるから。
	Groups int    `json:"groups"`
	LastTS string `json:"lastTS,omitempty"`
	// LastIdx は最後に折り込んだイベントの転写行番号（診断用）。
	LastIdx int `json:"lastIdx,omitempty"`
}

type usageFoldState struct {
	Sessions map[string]usageFoldMark `json:"sessions"`
}

var (
	usageFoldMu     sync.Mutex // state.json の読み書きと折り込みを直列化
	usageFoldedAt   time.Time  // 最後の fold-on-read（スロットル用）
	usageFoldInWork bool       // 走行中の一括折り込み（多重起動ガード）
	usageFoldPeriod = time.Minute
)

func usageStatePath() string { return filepath.Join(usageDir(), "state.json") }

func readUsageFoldState() usageFoldState {
	st := usageFoldState{Sessions: map[string]usageFoldMark{}}
	b, err := os.ReadFile(usageStatePath())
	if err != nil {
		return st
	}
	if json.Unmarshal(b, &st) != nil || st.Sessions == nil {
		st.Sessions = map[string]usageFoldMark{}
	}
	return st
}

func writeUsageFoldState(st usageFoldState) {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return
	}
	if os.MkdirAll(usageDir(), 0o700) != nil {
		return
	}
	tmp := usageStatePath() + ".tmp"
	if os.WriteFile(tmp, append(b, '\n'), 0o600) != nil {
		return
	}
	_ = os.Rename(tmp, usageStatePath()) // 途中で落ちても壊れた state を残さない
}

// usageTurnRow は転写から折り込む論理ターン1件。純関数の出力なので単体テストできる。
type usageTurnRow struct {
	Idx       int // 論理ターン通し番号（1始まり）
	TS        string
	Model     string
	Trigger   string // 直前のユーザーターンの注入元由来
	Sidechain bool
	Tokens    usageTokens
	LastIdx   int // 最後のイベントの転写行番号（診断用）
}

// foldTurnRows は転写のイベント列を論理ターンへ畳む。集計の流儀は aggregateUsage
// （session_usage.go）と同一でなければならない — 出力は加算、入力/キャッシュは各イベントが
// 毎回コンテキスト全量を再報告するので置換。ここがずれると台帳と get_session_usage が
// 食い違い、「二つの画面で数字が違う」が起きる。
//
// includeTrailing=false のとき、最後まで閉じなかったグループは返さない（進行中のターン）。
func foldTurnRows(turns []transcript.Turn, includeTrailing bool) []usageTurnRow {
	var rows []usageTurnRow
	var cur usageTurnRow
	inGroup, sidechain := false, false
	trigger := usageTriggerUser
	fold := func() {
		if !inGroup {
			return
		}
		cur.Idx = len(rows) + 1
		rows = append(rows, cur)
		cur, inGroup = usageTurnRow{}, false
	}
	for _, t := range turns {
		if t.Role != "assistant" {
			fold()
			if t.Role == "user" && !t.Compact {
				trigger = usageTriggerFromTurnSource(t.Source)
			}
			continue
		}
		if t.Sidechain != sidechain {
			fold() // サブエージェントは自分のコンテキストを報告する — 決して跨いで畳まない
			sidechain = t.Sidechain
		}
		if !inGroup {
			cur = usageTurnRow{Trigger: trigger, Sidechain: sidechain}
			inGroup = true
		}
		cur.Tokens.Out += t.OutTok
		if t.InTok+t.CacheRead+t.CacheCreate > 0 {
			cur.Tokens.In, cur.Tokens.CacheRead, cur.Tokens.CacheCreate = t.InTok, t.CacheRead, t.CacheCreate
		}
		if t.TS != "" {
			cur.TS = t.TS
		}
		if t.Model != "" {
			cur.Model = t.Model
		}
		cur.LastIdx = t.Idx
	}
	if includeTrailing {
		fold()
	}
	return rows
}

// usageTriggerFromTurnSource は注入元（transcript.Turn.Source）を台帳の trigger 語彙へ写す。
// 「人が打ったターン」と「オペレーター/ブリッジ/定時が突っ込んだターン」を消費として
// 分けるための対応表。
func usageTriggerFromTurnSource(src string) string {
	switch src {
	case turnSourceOperator:
		return usageTriggerOperator
	case turnSourceDiscord, turnSourceSlack:
		return usageTriggerBridge
	case turnSourceSchedule, turnSourceScheduleManual:
		return usageTriggerSchedule
	}
	return usageTriggerUser
}

// usageMeasuredForKind は kind ごとの計測精度（docs/46 §1-c）。トークンを報告しない
// エージェントの 0 を「消費 0」と読ませないための自己申告で、UI はこれで未計測分を
// 別枠表示する。
func usageMeasuredForKind(kind string) string {
	switch kind {
	case session.KindClaude, session.KindCodex, session.KindOpencode:
		return usageMeasuredExact
	case session.KindCopilot:
		return usageMeasuredPartial // 転写に outTok しかない
	}
	return usageMeasuredNone // kiro / cursor / agy: 転写にトークンが無い
}

// foldSessionUsage は1セッションの未折り込み分を台帳へ書き、折り込んだ論理ターン数を返す。
// includeTrailing=true は「転写がもう伸びない」と分かっている時（削除・アーカイブ）だけ。
// usageFoldMu 保持前提。
func foldSessionUsageLocked(m session.Meta, st *usageFoldState, includeTrailing bool) int {
	if !agentOf(m.Kind).Caps().CanTranscript {
		return 0 // shell/ssm には転写が無い
	}
	return foldSessionUsageWithTurns(m, st, usageTurns(m), includeTrailing)
}

// foldSessionUsageWithTurns は転写ロードを切り離した本体（実転写なしで冪等性を検証できる）。
func foldSessionUsageWithTurns(m session.Meta, st *usageFoldState, turns []transcript.Turn, includeTrailing bool) int {
	mark := st.Sessions[m.Name]
	rows := foldTurnRows(turns, includeTrailing)
	if len(rows) <= mark.Groups {
		// 転写が縮んだ（アーカイブ復元・手動編集）ケースを含む。watermark は下げない —
		// 下げると同じターンをもう一度数えてしまう。
		return 0
	}
	origin, originConv := session.OriginOf(m), m.OriginConv
	measured := usageMeasuredForKind(m.Kind)
	out := make([]usageRecord, 0, len(rows)-mark.Groups)
	for _, r := range rows[mark.Groups:] {
		rec := usageRecord{
			TS: r.TS, Call: randUUID(), Feature: usageFeatureSession, Trigger: r.Trigger,
			Origin: origin, OriginConv: originConv, Kind: m.Kind,
			Ref: m.Name, Sidechain: r.Sidechain, Idx: r.Idx,
			In: r.Tokens.In, Out: r.Tokens.Out,
			CacheRead: r.Tokens.CacheRead, CacheCreate: r.Tokens.CacheCreate,
			Spend:    usageSpend(r.Tokens.In, r.Tokens.CacheCreate, r.Tokens.Out),
			OK:       true,
			Measured: measured,
		}
		if rec.TS == "" {
			rec.TS = time.Now().UTC().Format(time.RFC3339)
		}
		if r.Model != "" {
			// 転写が報告したモデルはそのまま実測値。版込みの生 id なので model_raw にも残す。
			rec.Model, rec.ModelRaw, rec.ModelSrc = r.Model, r.Model, usageModelReported
		} else {
			rec.Model, rec.ModelSrc = usageModelFallback(m.Model)
		}
		out = append(out, rec)
	}
	appendUsageRows(out)
	last := rows[len(rows)-1]
	st.Sessions[m.Name] = usageFoldMark{Groups: len(rows), LastTS: last.TS, LastIdx: last.LastIdx}
	return len(out)
}

// foldAllSessionUsage は全セッションの差分を折り込む。導入直後の1回目は watermark 0 から
// 走るので、**過去分のセッション消費がそのまま遡って入る**（バックフィルの専用経路は要らない）。
// アーカイブ済みも対象 — 転写は残っており、消費は実際に起きているので。
func foldAllSessionUsage() int {
	if !usageEnabled() {
		return 0
	}
	usageFoldMu.Lock()
	defer usageFoldMu.Unlock()
	st := readUsageFoldState()
	n := 0
	for _, m := range session.ListMetas() {
		n += foldSessionUsageLocked(m, &st, false)
	}
	writeUsageFoldState(st)
	return n
}

// maybeFoldSessionUsage は fold-on-read。使用量が読まれた時にだけ、最短 60 秒間隔で走る。
//
// 全セッションの転写を読み直すので実測で十数秒かかる（158 セッションで ~20s）。呼び出し元
// （GET /sessions/usage）は既に同じ転写を読んでいて重いので、**折り込みは非同期に回して
// 応答レイテンシを増やさない**。スロットルと走行中フラグで多重起動しない。
func maybeFoldSessionUsage() {
	if !usageEnabled() {
		return
	}
	usageFoldMu.Lock()
	skip := usageFoldInWork || (!usageFoldedAt.IsZero() && time.Since(usageFoldedAt) < usageFoldPeriod)
	if !skip {
		usageFoldedAt, usageFoldInWork = time.Now(), true
	}
	usageFoldMu.Unlock()
	if skip {
		return
	}
	go func() {
		defer func() {
			usageFoldMu.Lock()
			usageFoldInWork = false
			usageFoldMu.Unlock()
		}()
		foldAllSessionUsage()
	}()
}

// finalizeSessionUsage は転写が消える直前に呼ぶ確定（fold-on-delete）。末尾の開いた
// ターンまで含めて折り込む — この後もう転写は伸びないので、二重計上の心配が無い。
// 呼ばないと「最後の1ターン」が永久に台帳へ入らない。
func finalizeSessionUsage(m session.Meta) {
	if !usageEnabled() {
		return
	}
	usageFoldMu.Lock()
	defer usageFoldMu.Unlock()
	st := readUsageFoldState()
	foldSessionUsageLocked(m, &st, true)
	// 台帳側は ref に焼き込み済みなので、消えたセッションの watermark は持ち続けない。
	delete(st.Sessions, m.Name)
	writeUsageFoldState(st)
}
