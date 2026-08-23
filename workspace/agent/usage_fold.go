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
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// usageFoldMark は1セッション分の watermark。
//
// **件数（Groups）だけでは足りない。** claude は1つの sid に対して兄弟 jsonl を持ちうり
// （cwd 変更・CLAUDE_CONFIG_DIR 切替・Remote Control のスタブ）、読む1本は **mtime で
// 選ばれる**（internal/agents/claude/transcript.go）。選択が入れ替わると件数ベースの差分は
// 二通りに壊れる — 短い方へ入れ替わると `len(rows) <= Groups` で**折り込みが永久に止まり**、
// 長い方へ入れ替わると別の会話の**先頭ターンを既折り込み扱いで落とす**。そこで
// **件数と時刻の両方**で「まだ折り込んでいないターン」を判定する（LastTS は診断用ではなく
// 判定に効く値）。
type usageFoldMark struct {
	// Groups は折り込み済みの論理ターン数（＝到達した最大の通し番号）。転写は追記のみなので、
	// 論理ターンの通し番号は kind に依らず安定。各 kind の Turn.Idx（行番号）を使わないのは、
	// 番号体系が kind ごとに違って watermark が混ざるから。
	Groups int `json:"groups"`
	// LastTS は折り込み済みの最大ターン時刻。転写が入れ替わっても「これより新しいターンは
	// まだ数えていない」が言える。
	LastTS string `json:"lastTS,omitempty"`
	// LastIdx は最後に折り込んだイベントの転写行番号（診断用）。
	LastIdx int `json:"lastIdx,omitempty"`
}

type usageFoldState struct {
	Sessions map[string]usageFoldMark `json:"sessions"`
}

// ロックは3つに分けてある。まとめると fold-on-read を非同期にした意味が消えるため:
//
//   - usageFoldMu は state.json の読み書きと **1セッション分** の折り込みを直列化する。
//     一括折り込みはセッションの切れ目で必ず手放すので、並行する /usage/series・
//     /sessions/usage・削除時の確定が待つのは最大でも1セッション分になる。以前はパス
//     全体（実測 158 セッションで ~20s）を握っていて、Console が1画面で3本撃つ
//     /usage/series の2本目以降がまるごとブロックされていた。
//   - usageFoldRunning は多重起動ガード。**ロックを取らずに読める**必要がある — 走行中か
//     を聞くだけの呼び出しが、走行中の折り込みの後ろに並んではいけない。
//   - usageFoldGate はスロットル時刻だけを守る極小ロック（保持は数命令）。
var (
	usageFoldMu      sync.Mutex  // state.json の読み書きと1セッション分の折り込み
	usageFoldRunning atomic.Bool // 走行中の一括折り込み（ロック不要で読める多重起動ガード）
	usageFoldGate    sync.Mutex  // usageFoldedAt 専用
	usageFoldedAt    time.Time   // 最後の fold-on-read（スロットル用・usageFoldGate 保持で触る）
	usageFoldPeriod  = time.Minute
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

// writeUsageFoldState は watermark を tmp+rename で置き換える。**エラーは握り潰さない** —
// 書けていないのに成功として進むと、既に追記した行の分だけ次回パスが再追記する（＝二重計上）。
func writeUsageFoldState(st usageFoldState) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(usageDir(), 0o700); err != nil {
		return err
	}
	tmp := usageStatePath() + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, usageStatePath()) // 途中で落ちても壊れた state を残さない
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
	case turnSourceAutoResume:
		return usageTriggerRecovery // 中断からの自動再開（docs/47 §4-6）は自己修復の消費
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
func foldSessionUsageLocked(m session.Meta, st *usageFoldState, includeTrailing bool) (int, error) {
	if !agentOf(m.Kind).Caps().CanTranscript {
		return 0, nil // shell/ssm には転写が無い
	}
	return foldSessionUsageWithTurns(m, st, usageTurns(m), includeTrailing)
}

// foldSessionUsageWithTurns は転写ロードを切り離した本体（実転写なしで冪等性を検証できる）。
// 戻り値の int は台帳へ追記した行数で、**0 なら st は書き換わっていない**（呼び出し元は
// watermark を書き直さなくてよい）。
func foldSessionUsageWithTurns(m session.Meta, st *usageFoldState, turns []transcript.Turn, includeTrailing bool) (int, error) {
	mark := st.Sessions[m.Name]
	rows := foldTurnRows(turns, includeTrailing)
	fresh := unfoldedTurnRows(rows, mark)
	if len(fresh) == 0 {
		// 転写が縮んだ（アーカイブ復元・手動編集・兄弟 jsonl への入れ替わり）ケースを含む。
		// watermark は下げない — 下げると同じターンをもう一度数えてしまう。
		return 0, nil
	}
	if len(rows) < mark.Groups {
		// 短い転写へ入れ替わった。件数だけで差分を採っていた頃はここで永久に止まっていた。
		log.Printf("usage: fold %s: 転写が %d → %d 論理ターンへ入れ替わった（時刻で差分を採る）",
			m.Name, mark.Groups, len(rows))
	}
	origin, originConv := session.OriginOf(m), m.OriginConv
	measured := usageMeasuredForKind(m.Kind)
	out := make([]usageRecord, 0, len(fresh))
	for _, r := range fresh {
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
	// 行が書けなかったら watermark は進めない。次回パスで同じターンが差分として出てくるので、
	// **取りこぼしは復旧後に必ず回収される**（部分的に書けていた分は集計側の (ref, idx)
	// 重複排除が落とす — usage_dedup.go）。進めてしまうと、この分は次のパスでも差分に出てこず
	// 二度と台帳へ入らない（台帳側に取りこぼしを拾い直す口は無い）。
	if err := appendUsageRows(out); err != nil {
		return 0, err
	}
	last := fresh[len(fresh)-1]
	// watermark は上げるだけ（入れ替わった短い転写で下げない）。
	st.Sessions[m.Name] = usageFoldMark{
		Groups:  max(mark.Groups, len(rows)),
		LastTS:  laterUsageTS(mark.LastTS, last.TS),
		LastIdx: last.LastIdx,
	}
	return len(out), nil
}

// unfoldedTurnRows は「まだ折り込んでいない論理ターン」を返す。件数と時刻の **両方** で
// 判定するのがここの肝（usageFoldMark のコメント）:
//
//   - 通常（追記のみで転写が伸びた）: 通し番号が watermark を超えた分＝末尾の差分。
//     従来と同じ結果になる。
//   - 転写が入れ替わった: 通し番号が重なっていても **watermark より新しい時刻のターンは
//     まだ数えていない**ので拾う。逆に古い時刻のターンは拾わない（別の会話の焼き直しを
//     数えない）。取り違えて二重に拾った分は集計側の (ref, idx) 重複排除が受け止める。
func unfoldedTurnRows(rows []usageTurnRow, mark usageFoldMark) []usageTurnRow {
	var out []usageTurnRow
	for _, r := range rows {
		if r.Idx > mark.Groups || usageTSAfter(r.TS, mark.LastTS) {
			out = append(out, r)
		}
	}
	return out
}

// usageTSAfter は a が b より後か。**解けない時刻は「後ではない」に倒す** — 判定できない
// ものを新しい扱いにすると、転写を読むたびに同じターンを積み増してしまう。
func usageTSAfter(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	ta, err := time.Parse(time.RFC3339, a)
	if err != nil {
		return false
	}
	tb, err := time.Parse(time.RFC3339, b)
	if err != nil {
		return false
	}
	return ta.After(tb)
}

// laterUsageTS は新しい方の時刻を返す（watermark を巻き戻さないため）。
func laterUsageTS(cur, next string) string {
	if usageTSAfter(next, cur) || cur == "" {
		return next
	}
	return cur
}

// commitSessionUsageFold は1セッション分を「state 読み → 行の追記 → watermark 書き」まで
// **1つのクリティカルセクションで閉じる**。パス全体を1トランザクションにしない理由は2つ:
//
//  1. **二重計上の窓を1セッションに縮める。** 行を追記した後 watermark を書く前に落ちると、
//     その分は次回パスで再追記される（追記先と watermark は別ファイルで、原子的には
//     書けない）。全セッション分をパス末尾でまとめて1回書いていた頃は、~20 秒のパスの
//     どこで落ちても、それまでに畳んだ全セッションが丸ごと重複しえた。窓は消せないので、
//     **残る1セッション分は集計側が (ref, idx) で落とす**（usage_dedup.go）。
//  2. **ロックを長時間握らない**（上の usageFoldMu の注記）。
func commitSessionUsageFold(m session.Meta, includeTrailing bool) (int, error) {
	usageFoldMu.Lock()
	defer usageFoldMu.Unlock()
	st := readUsageFoldState()
	n, err := foldSessionUsageLocked(m, &st, includeTrailing)
	if err != nil || n == 0 {
		return 0, err // n==0 なら st は無傷 — 書き直す必要が無い
	}
	if err := writeUsageFoldState(st); err != nil {
		// 行は既にディスクにある。ここで黙って成功にすると、次回パスが同じターンを
		// もう一度追記したことに誰も気づけない。
		return n, err
	}
	return n, nil
}

// foldAllSessionUsage は全セッションの差分を折り込む。導入直後の1回目は watermark 0 から
// 走るので、**過去分のセッション消費がそのまま遡って入る**（バックフィルの専用経路は要らない）。
// アーカイブ済みも対象 — 転写は残っており、消費は実際に起きているので。
func foldAllSessionUsage() int {
	if !usageEnabled() {
		return 0
	}
	n := 0
	for _, m := range session.ListMetas() {
		// 1セッションが失敗しても残りは畳む（1つの壊れた転写でフリート全体の計測が
		// 止まる方が悪い）。取りこぼしは黙って消さず、必ずログに残す。
		c, err := commitSessionUsageFold(m, false)
		if err != nil {
			log.Printf("usage: fold %s: %v", m.Name, err)
		}
		n += c
	}
	return n
}

// maybeFoldSessionUsage は fold-on-read。使用量が読まれた時にだけ、最短 60 秒間隔で走る。
func maybeFoldSessionUsage() { startFoldSessionUsage(false) }

// startFoldSessionUsage は fold-on-read の起動。
//
// 全セッションの転写を読み直すので実測で十数秒かかる（158 セッションで ~20s）。呼び出し元
// （GET /sessions/usage）は既に同じ転写を読んでいて重いので、**折り込みは非同期に回して
// 応答レイテンシを増やさない**。スロットルと走行中フラグで多重起動しない。
//
// force=true は 60 秒スロットルを飛ばす（利用者が明示的に「再取得」を押した時だけ）。
// 押した直後にもう一度押しても意味が無い状態を作らないため — 押す前に終わったターンは
// この1回で必ず台帳へ入る。
//
// 戻り値は「**この読み出しの時点で折り込みが走っている**」＝呼び出し元が今から読む値は
// 直近のターンをまだ含まないかもしれない、の申告。非同期にした代償を黙って利用者に
// 押し付けない（＝最新になるまで再取得を何度も押させない）ための唯一の手掛かりなので、
// 応答へ必ず載せること（usage_series.go の folding）。
func startFoldSessionUsage(force bool) bool {
	if !usageEnabled() {
		return false
	}
	// 走行中の判定に usageFoldMu を使わない。使うと「走っているか?」を聞くだけの呼び出しが
	// 折り込み本体のロック解放を待つことになり、非同期にした意味が無くなる（Console の
	// 使用量ビューは1画面で /usage/series を3本撃つので、2本目以降がまるごと待たされていた）。
	if usageFoldRunning.Load() {
		return true
	}
	usageFoldGate.Lock()
	skip := !force && !usageFoldedAt.IsZero() && time.Since(usageFoldedAt) < usageFoldPeriod
	if !skip {
		// CAS で勝った1本だけが走る（Load からここまでの隙間で別の呼び出しが起動しうる）。
		skip = !usageFoldRunning.CompareAndSwap(false, true)
	}
	if !skip {
		usageFoldedAt = time.Now()
	}
	usageFoldGate.Unlock()
	if skip {
		// スロットルで見送った＝走っていない、CAS 負け＝別の1本が走っている。
		return usageFoldRunning.Load()
	}
	go func() {
		defer usageFoldRunning.Store(false)
		foldAllSessionUsage()
	}()
	return true
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
	if _, err := foldSessionUsageLocked(m, &st, true); err != nil {
		log.Printf("usage: finalize %s: %v", m.Name, err)
	}
	// 台帳側は ref に焼き込み済みなので、消えたセッションの watermark は持ち続けない
	// （残すと state.json が「もう存在しないセッション」の分だけ単調に増える）。
	delete(st.Sessions, m.Name)
	if err := writeUsageFoldState(st); err != nil {
		log.Printf("usage: finalize %s: watermark: %v", m.Name, err)
	}
}
