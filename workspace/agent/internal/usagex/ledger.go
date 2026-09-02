package usagex

// 使用量台帳（docs/log/46 / ADR0029 P1）。1行 = LLM 呼び出し1回、または折り込んだ
// セッションの論理ターン1回。
//
// 非交渉の原則: プロンプト本文・応答本文は一切記録しない（トークン数とメタのみ）。
//
// 保存は ~/.local/share/agent-fleet/usage/raw/YYYY-MM-DD.jsonl（追記のみ・日次ローテ・
// 既定90日保持）。~/.local は Workspace の recreate を跨いで残る（Workspace Guide）。
// 1行 ≈ 200B なので、補助 100 呼び出し/日 + セッション 2,000 ターン/日 でも ~420KB/日。

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// feature — 消費源の列挙（ADR0029 §2 で凍結。Console 側で i18n する）。
const (
	FeatureAssistantChat    = "assistant.chat"     // 利用者のチャット1ターン
	FeatureAssistantAsk     = "assistant.ask"      // 単発アドバイザリ（非永続）
	FeatureAssistantAutoTur = "assistant.autoturn" // セッション完了報告への自動ターン
	FeatureAssistantBridge  = "assistant.bridge"   // Discord/Slack からのオペレーター応答
	FeatureCompact          = "compact"            // 要約引き継ぎ（docs/log/33）
	FeaturePlanUpdate       = "plan.update"        // 作業計画の明示更新（docs/log/33 第5段）
	FeatureTitleSession     = "title.session"      // セッション件名の提案
	FeatureTitleChat        = "title.chat"         // 会話タイトルの提案
	FeatureBranchSuggest    = "branch.suggest"     // ブランチ名の提案
	FeatureSuggestSession   = "suggest.session"    // ミラーの ✨ 返信候補
	FeatureSuggestChat      = "suggest.chat"       // チャットの ✨ 返信候補
	FeatureSuggestEdit      = "suggest.edit"       // エディタの ✨ AI変更提案（docs/log/44 Phase 4）
	FeatureSession          = "session"            // 対話セッション本体（転写から折り込み）
	// FeatureUnknown はタグの付いていない呼び出し。新しい補助機能がタグを付け忘れても
	// 必ず1行残す（無記録＝見えない消費、を作らないことをタグの正しさより優先する）。
	FeatureUnknown = "unknown"
)

// trigger — ターンの注入元。
const (
	TriggerUser     = "user"
	TriggerAuto     = "auto"
	TriggerManual   = "manual"
	TriggerSchedule = "schedule"
	TriggerOperator = "operator"
	TriggerBridge   = "bridge"
	TriggerRecovery = "recovery"
)

// model_src — モデル次元をどこから得たかの自己申告（ADR0029 §4）。
const (
	ModelReported = "reported"        // 実行側が報告した（claude / 転写の Turn.Model）
	ModelRequest  = "requested"       // こちらが要求した値しか分からない
	ModelUnknown  = "default_unknown" // CLI 側の既定に委ねた＝解決後のモデル不明
)

// measured — 「0」と「未計測」を絶対に混同させないための自己申告。
const (
	MeasuredExact   = "exact"   // in/out/cache すべて取れた
	MeasuredPartial = "partial" // 一部だけ（copilot の outTok のみ 等）
	MeasuredNone    = "none"    // トークンを報告しない CLI — 回数だけ数える
)

// Record は台帳1行。JSON タグは Console/API と対の凍結ワイヤ（ADR0029 §1）。
type Record struct {
	TS   string `json:"ts"`   // 呼び出し完了時刻（UTC・RFC3339）
	Call string `json:"call"` // 呼び出し ID: 1呼び出しが複数モデル行に割れる時に束ねる
	// Feature/Trigger/Ref/Verb は ctx の usageTag 由来（usage_tag.go）。
	Feature string `json:"feature"`
	Trigger string `json:"trigger,omitempty"`
	// Origin/OriginConv はセッションの出自（ADR0029 §6）。ref から解決して行へ焼き込む
	// ので、セッションが削除されても集計が壊れない。
	Origin     string `json:"origin,omitempty"`
	OriginConv string `json:"origin_conv,omitempty"`
	// Kind は実際に実行したエージェント種別（要求ではなく実行結果）。
	Kind     string `json:"kind"`
	Model    string `json:"model,omitempty"`     // 正規モデル名（版を畳んだ系列キー）
	ModelRaw string `json:"model_raw,omitempty"` // 報告された生 id（版込み）
	ModelReq string `json:"model_req,omitempty"` // 要求した値（食い違い＝フォールバック検知）
	ModelSrc string `json:"model_src,omitempty"`
	Ref      string `json:"ref,omitempty"`  // セッション名 or 会話 id
	Verb     string `json:"verb,omitempty"` // assistant.chat のサブ次元（translate|summarize）
	// Sidechain は feature=session のサブ次元（サブエージェント / Workflow の消費）。
	Sidechain bool `json:"sidechain,omitempty"`
	// Idx は feature=session の論理ターン通し番号（1始まり）。書き手側の冪等性は
	// usage/state.json の watermark が担保するが、**追記と watermark は別ファイルで
	// 原子的に書けない**（間で落ちると再追記される）ので、集計側は (ref, Idx) を読んで
	// 重複を落とす（usage_dedup.go）。次元ではないので usageKey には入らない。
	Idx         int     `json:"idx,omitempty"`
	In          int     `json:"in"`
	Out         int     `json:"out"`
	CacheRead   int     `json:"cread"`
	CacheCreate int     `json:"ccreate"`
	Spend       int     `json:"spend"`              // = in + ccreate + out（cache_read を含めない）
	CostUSD     float64 `json:"cost_usd,omitempty"` // 実測が取れた時だけ（claude）
	MS          int     `json:"ms,omitempty"`
	OK          bool    `json:"ok"`
	Measured    string  `json:"measured"`
}

// Spend は主指標。cache_read を含めないのは既存の get_session_usage / ミラーの
// ContextBar と同じ定義に揃えるため（二つの画面が食い違わない方が、理論的な正しさより重い）。
func Spend(in, create, out int) int { return in + create + out }

// Enabled — 記録の全体スイッチ。AF_USAGE_RECORD=0 で完全に止める（P5 の設定 UI が
// 書き換える口）。既定 ON。
func Enabled() bool { return os.Getenv("AF_USAGE_RECORD") != "0" }

// Dir は台帳のルート。AF_USAGE_DIR はテスト用の差し替え口。
func Dir() string {
	if v := os.Getenv("AF_USAGE_DIR"); v != "" {
		return v
	}
	return filepath.Join(paths.AgentDataDir(), "usage")
}

func RawDir() string { return filepath.Join(Dir(), "raw") }

// RetentionDays — raw の保持日数（rollup は無期限・ADR0029 §7-3）。
func RetentionDays() int {
	if v := os.Getenv("AF_USAGE_RETENTION_DAYS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 90
}

var (
	// Mu は追記と prune を直列化する（同一プロセス内の並行ターン）。公開しているのは
	// usage_rollup.go の readUsageDayForRollup が「追記と競合しない形で 1 日分を読む」ために
	// 同じロックを取るため。
	//
	// 🔥 main 側で受けるときは **必ずポインタ**（alias_usagex.go の `var usageMu = &usagex.Mu`）。
	// `var usageMu = usagex.Mu` と書くと **mutex ごと写されて別物になり**、追記側と読み側が
	// 違う錠を掛けて直列化が無言で消える。
	Mu sync.Mutex
	// prunedAt は最後に保持期間 prune を走らせた時刻。追記のたびにディレクトリを
	// 走査しないための節流 — 台帳は追記の方が桁違いに多い。
	prunedAt time.Time
)

// AppendRows は行群を当日のファイルへ追記する。1呼び出しが複数モデルに割れた行は
// 同じ Call を共有した状態で渡ってくる（呼び出し回数を二重に数えないため）。
//
// 書けなかったことは error で返す。呼び出し元の扱いは2種類に分かれる:
//   - 補助呼び出しの記録（recordUsageCall）は**ベストエフォート**。台帳が書けないことで
//     チャットやタイトル提案が失敗してはならないので、握り潰す。
//   - セッション折り込み（usage_fold.go）は**失敗を伝播させる**。書けていないのに
//     watermark を進めると、その分の消費が二度と入らない。
func AppendRows(rows []Record) error {
	if len(rows) == 0 || !Enabled() {
		return nil
	}
	Mu.Lock()
	defer Mu.Unlock()
	dir := RawDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	day := time.Now().UTC().Format("2006-01-02")
	f, err := os.OpenFile(filepath.Join(dir, day+".jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	for _, r := range rows {
		if err := enc.Encode(r); err != nil {
			f.Close()
			return err
		}
	}
	// Close のエラーまで見る — 追記は書き込みが遅延しうるので、Close が最後の関門になる。
	if err := f.Close(); err != nil {
		return err
	}
	pruneRawLocked()
	return nil
}

// pruneRawLocked は保持期間を過ぎた日次ファイルを消す。Mu 保持前提。
// 1時間に1回までしか走らない（追記のホットパスに ReadDir を積まない）。
func pruneRawLocked() {
	now := time.Now()
	if !prunedAt.IsZero() && now.Sub(prunedAt) < time.Hour {
		return
	}
	prunedAt = now
	dir := RawDir()
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := now.UTC().AddDate(0, 0, -RetentionDays())
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".jsonl" {
			continue
		}
		// ファイル名の日付だけで判定する（mtime は copy/restore でずれる）。
		day, err := time.Parse("2006-01-02", name[:len(name)-len(".jsonl")])
		if err != nil {
			continue // 想定外の名前には触らない
		}
		if day.Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
}

// RawDays は台帳に存在する日（UTC の YYYY-MM-DD）を昇順で返す。
func RawDays() []string {
	ents, err := os.ReadDir(RawDir())
	if err != nil {
		return nil
	}
	days := make([]string, 0, len(ents))
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || filepath.Ext(n) != ".jsonl" {
			continue
		}
		day := n[:len(n)-len(".jsonl")]
		if _, err := time.Parse("2006-01-02", day); err == nil {
			days = append(days, day)
		}
	}
	sort.Strings(days) // 日付名なので辞書順＝時系列
	return days
}

// ReadDay は1日分の行を追記順で読む。順序を保つことが重要 — 1呼び出しが複数モデル行に
// 割れたとき、同じ call の最初の行を「呼び出しを数える行」とみなすため（usage_rollup.go）。
func ReadDay(day string) []Record {
	b, err := os.ReadFile(filepath.Join(RawDir(), day+".jsonl"))
	if err != nil {
		return nil
	}
	var out []Record
	for _, ln := range bytes.Split(b, []byte("\n")) {
		if len(bytes.TrimSpace(ln)) == 0 {
			continue
		}
		var r Record
		if json.Unmarshal(ln, &r) == nil && r.Feature != "" {
			out = append(out, r)
		}
	}
	return out
}

// ReadRows は台帳の全行を時系列で読む（テストと小規模な走査用）。
func ReadRows() []Record {
	var out []Record
	for _, day := range RawDays() {
		out = append(out, ReadDay(day)...)
	}
	return out
}

// PruneRawNow は保持期間 prune を今すぐ回す（テスト専用の口）。節流は効いたままなので、
// 直前に ResetPruneClock を呼んでおくこと。移送前は main のテストが usageMu を自分で取って
// pruneUsageRawLocked() を直接呼んでいたが、どちらも未公開になったのでここに 1 つ口を開ける。
func PruneRawNow() {
	Mu.Lock()
	pruneRawLocked()
	Mu.Unlock()
}

// ResetPruneClock は保持期間 prune の節流時計を戻す（テスト専用の口）。
// 移送前は main のテストヘルパ useTempUsageDir が `usageMu` / `usagePrunedAt` を直接
// 触っていたが、どちらもこのパッケージの未公開状態になったので、その 1 点だけを開ける。
func ResetPruneClock() {
	Mu.Lock()
	prunedAt = time.Time{}
	Mu.Unlock()
}
