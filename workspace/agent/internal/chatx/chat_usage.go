package chatx

// アシスタントチャットのコンテキスト使用量の捕捉と逼迫通知（docs/log/33）。
//
// 各プロバイダの headless 実行が返す usage イベントをターン毎に拾い、会話へ
// 「直近のコンテキスト占有」スナップショット（contextUsage — get_session_usage /
// ミラーの ContextBar と同じ形）を永続化する。resume 駆動のチャットはコンテキスト
// がプロバイダ側 transcript に無限に積み上がるため、まず占有を可視化し、閾値超過
// 時には notice を1回だけ追記して新スレッドへの引き継ぎを促す — これが肥大対策の
// 第1段（可視化）。要約引き継ぎ（自前コンパクション）は後続。
//
// イベント形状は 3 プロバイダとも実測で確認済み（2026-07: claude-code 2.1.x /
// codex-cli 0.144 / opencode 1.18）。usage の取れなかったターンでは前回値を保持する。

import (
	"sort"
	"strconv"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/notice"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// chatCtxWarnPct — コンテキスト使用率がこの割合(%)以上になったら notice で引き継ぎを
// 促す。ContextBar の「near」帯（80%）に合わせる: バーが警告色になるのと同じタイミング
// で文字でも知らせ、プロバイダ側の自動コンパクションやウィンドウ超過エラーより先に
// 利用者へ届くようにする。
const chatCtxWarnPct = 80.0

// claudeUsage は claude -p の usage ブロック（result イベント/assistant イベントの
// message.usage 共通の形）。iterations は 1 ターン内の API 呼び出し毎のスナップ
// ショット（実測: 最後の要素が最終的なコンテキスト占有。ツール多段ターンでも
// トップレベルの合算値ではなく末尾要素を使えば正確）。
type ClaudeUsage struct {
	InputTokens              int `json:"input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	// OutputTokens はコンテキスト占有には効かない（出力は次ターンの入力として戻る）が、
	// 使用量台帳の spend には要る（docs/log/46 §2）。実測で存在を確認済み。
	OutputTokens int           `json:"output_tokens"`
	Iterations   []ClaudeUsage `json:"iterations,omitempty"`
}

// contextTokens は入力側スナップショットの合計 = コンテキスト占有量。
func (u ClaudeUsage) contextTokens() int {
	return u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
}

// ledgerTokens は使用量台帳のトークン内訳（docs/log/46）。**modelUsage が取れなかった時の
// 縮退用**で、モデル別の内訳は失われるが総量は残る。トップレベルの値を使うのは、台帳が
// 見たいのが「この呼び出しで実際に課金された量」だから — コンテキスト占有（iterations
// 末尾のスナップショット）とは別の量。
func (u ClaudeUsage) LedgerTokens() usagex.Tokens {
	return usagex.Tokens{
		In: u.InputTokens, Out: u.OutputTokens,
		CacheRead: u.CacheReadInputTokens, CacheCreate: u.CacheCreationInputTokens,
	}
}

// claudeModelUsage は result イベントの modelUsage エントリのうち必要な分。マップの
// キーは版込みの生モデル id（claude-haiku-4-5-20251001）で、CanonicalModel が版を畳んだ
// 系列キー（claude-haiku-4-5）— 版が上がっても台帳の系列が分断されないよう両方を持つ。
// contextWindow はモデルの実ウィンドウ（recorded として使える）。トークン4種と CostUSD は
// 使用量台帳のモデル別行（ADR 0029 §1）。claude は1呼び出しが複数モデルに割れることが
// あるので、ここが唯一「モデル毎の内訳」を実測で持つ経路。全フィールド実測確認済み。
type ClaudeModelUsage struct {
	ContextWindow            int     `json:"contextWindow"`
	InputTokens              int     `json:"inputTokens"`
	OutputTokens             int     `json:"outputTokens"`
	CacheReadInputTokens     int     `json:"cacheReadInputTokens"`
	CacheCreationInputTokens int     `json:"cacheCreationInputTokens"`
	CostUSD                  float64 `json:"costUSD"`
	CanonicalModel           string  `json:"canonicalModel"`
}

// usageModelRows は modelUsage を台帳のモデル別行へ変換する。キー（生 id）を model_raw、
// canonicalModel を系列キーにする。順序はキー昇順に固定 — マップ反復順のままだと同じ
// 呼び出しでも行順が揺れ、テストと台帳の読み口が不安定になる。
func UsageModelRows(mu map[string]ClaudeModelUsage) []usagex.ModelRow {
	if len(mu) == 0 {
		return nil
	}
	raws := make([]string, 0, len(mu))
	for raw := range mu {
		raws = append(raws, raw)
	}
	sort.Strings(raws)
	rows := make([]usagex.ModelRow, 0, len(raws))
	for _, raw := range raws {
		m := mu[raw]
		rows = append(rows, usagex.ModelRow{
			Model: m.CanonicalModel, ModelRaw: raw, CostUSD: m.CostUSD,
			Tokens: usagex.Tokens{
				In: m.InputTokens, Out: m.OutputTokens,
				CacheRead: m.CacheReadInputTokens, CacheCreate: m.CacheCreationInputTokens,
			},
		})
	}
	return rows
}

// claudeCtx は 1 回の claude 実行のイベント列からコンテキスト占有を追跡する。
// stream では assistant イベント毎の message.usage を、非 stream では result の
// usage.iterations 末尾を最終スナップショットとして採る。
type claudeCtx struct {
	snap   ClaudeUsage
	window int
	model  string
}

// observeAssistant は stream の assistant イベント（message.usage）を反映する。
// 同一メッセージのイベントは同じ usage を重複して運ぶので、単純に最後勝ちでよい。
func (t *claudeCtx) observeAssistant(model string, u ClaudeUsage) {
	if u.contextTokens() <= 0 {
		return
	}
	t.snap = u
	if model != "" {
		t.model = model // イベントが運ぶ実モデル名を優先
	}
}

// observeResult は result イベントの usage / modelUsage を反映する。iterations が
// あれば末尾が最終スナップショット（assistant イベント由来の値とも一致する）。
func (t *claudeCtx) observeResult(u ClaudeUsage, modelUsage map[string]ClaudeModelUsage) {
	if n := len(u.Iterations); n > 0 {
		t.snap = u.Iterations[n-1]
	} else if t.snap.contextTokens() == 0 {
		t.snap = u
	}
	// modelUsage からウィンドウ実測値を拾う。サブエージェント禁止のチャットでは通常
	// 1 エントリ。万一複数あるときは解決済みモデル名と一致するものだけ信用する。
	if len(modelUsage) == 1 {
		for k, mu := range modelUsage {
			if t.model == "" {
				t.model = k
			}
			t.window = mu.ContextWindow
		}
		return
	}
	if mu, ok := modelUsage[t.model]; ok {
		t.window = mu.ContextWindow
	}
}

// apply は追跡結果を会話へ格納する（成功ターンの saveConv 前に呼ぶ）。claude は
// イベントに実モデルを載せる数少ないプロバイダなので、ここがそのターンのモデル
// （--model に渡したエイリアスではなく API が名乗った版込み id）の記録点でもある。
func (t *claudeCtx) apply(c *ChatConversation) {
	setChatContext(c, t.snap.InputTokens, t.snap.CacheReadInputTokens,
		t.snap.CacheCreationInputTokens, t.window, t.model)
	c.noteTurnModel(t.model)
}

// codexUsage は codex exec --json の turn.completed が運ぶ usage。input_tokens は
// cached_input_tokens を含む（rollout の token_count と同じ流儀）。ターン合算値
// なので、ツール多段ターンではコンテキスト占有として過大側の近似になる — チャット
// の大半（ツールなし 1 呼び出し）では正確で、警告用途には安全側。
type CodexUsage struct {
	InputTokens       int `json:"input_tokens"`
	CachedInputTokens int `json:"cached_input_tokens"`
	// 使用量台帳向け（docs/log/46 §2）。実測（codex-cli 0.144.x）で turn.completed が
	// cache_write_input_tokens / output_tokens / reasoning_output_tokens も運ぶことを確認。
	// reasoning は output に含まれる内訳なので spend では足さない。
	CacheWriteInputTokens int `json:"cache_write_input_tokens"`
	OutputTokens          int `json:"output_tokens"`
}

// ledgerTokens は codex のターン合算 usage を台帳の内訳へ写す。input_tokens は cached を
// 含む（rollout の token_count と同じ流儀）ので、fresh = input - cached。
func (u CodexUsage) LedgerTokens() usagex.Tokens {
	fresh := u.InputTokens - u.CachedInputTokens
	if fresh < 0 {
		fresh = 0
	}
	return usagex.Tokens{
		In: fresh, Out: u.OutputTokens,
		CacheRead: u.CachedInputTokens, CacheCreate: u.CacheWriteInputTokens,
	}
}

// opencodeUsage は opencode run --format json の step_finish が運ぶ part.tokens。
// input はキャッシュ分を含まない（SQLite ストアの message.data.tokens と同じ形）。
type opencodeUsage struct {
	Input int `json:"input"`
	// Output は使用量台帳向け（docs/log/46 §2）。このワークスペースは opencode 未ログインで
	// ライブ検証できていないので、取れなければ 0 のまま — 推測で埋めない（ADR 0029 §4）。
	Output int `json:"output"`
	Cache  struct {
		Read  int `json:"read"`
		Write int `json:"write"`
	} `json:"cache"`
}

// ledgerTokens は opencode の内訳を台帳の形へ写す（input はキャッシュ分を含まない）。
func (u opencodeUsage) LedgerTokens() usagex.Tokens {
	return usagex.Tokens{In: u.Input, Out: u.Output, CacheRead: u.Cache.Read, CacheCreate: u.Cache.Write}
}

// setChatContext は共通の格納口。スナップショットが空のターン（usage の取れない
// プロバイダ経路・空応答）では何もせず前回値を残す。window が取れなければモデル名
// から推定する（contextWindowGuess — get_session_usage と同じ）。
func setChatContext(c *ChatConversation, fresh, read, create, window int, model string) {
	if fresh < 0 {
		fresh = 0
	}
	tokens := fresh + read + create
	if tokens <= 0 {
		return
	}
	source := "recorded"
	if window <= 0 {
		window, source = usagex.WindowGuess(model, tokens), "estimated"
	}
	u := &usagex.ContextUsage{
		Tokens: tokens, Read: read, Create: create, Fresh: fresh,
		Window: window, WindowSource: source, Model: model,
	}
	if window > 0 {
		u.Pct = float64(tokens) / float64(window) * 100
	}
	c.Context = u
}

// chatCtxModelFor はウィンドウ推定に使うモデル名: そのターンを実際に回したバックエンド
// 基準で解決した固定値があればそれ（chatModelFor — 認証フォールバックや途中切替では会話の
// ピン留めと別 CLI が回るので、kind を渡さないと他 CLI のモデル名でウィンドウを推定して
// しまう）、なければバックエンド毎の既定。
func chatCtxModelFor(c *ChatConversation, kind string) string {
	if m := chatModelFor(c, kind); m != "" {
		return m
	}
	switch kind {
	case session.KindCodex:
		return defaultCodexChatModel
	case session.KindOpencode:
		return defaultOpencodeChatModel
	}
	return envOr("AF_CHAT_MODEL", defaultChatModel)
}

// noteContextPressure は閾値超過時に notice を1回だけ追記し、通知センターへも流す
// （自動ターン中＝会話を開いていない時でも気づけるように）。閾値を下回ったら
// （プロバイダ側のコンパクション等で占有が減った場合）フラグを戻し、次の超過で
// 改めて1回知らせる。呼び出し側が会話ロックを保持し、直後に saveConv すること。
func NoteContextPressure(c *ChatConversation) {
	u := c.Context
	if u == nil || u.Pct < chatCtxWarnPct {
		c.CtxWarned = false
		return
	}
	if c.CtxWarned {
		return
	}
	c.CtxWarned = true
	c.Messages = append(c.Messages, newNotice(noticeKeyCtxPressure, map[string]string{
		"pct":    strconv.Itoa(int(u.Pct)),
		"tokens": fmtKTokens(u.Tokens),
		"window": fmtKTokens(u.Window),
	}, ctxPressureContent(u)))
	ev := notice.New("chat-context-pressure", "", "", c.Title)
	ev.Payload["conversation_id"] = c.ID
	ev.Payload["conversationTitle"] = c.Title
	_ = notice.Put(ev)
}

// ctxPressureContent は逼迫 notice の正本言語（ja）フォールバック本文。表示は
// noticeKeyCtxPressure のカタログ訳が担う（chat_notice.go / ADR 0033）。
func ctxPressureContent(u *usagex.ContextUsage) string {
	return "この会話のコンテキスト使用量が上限の約" + strconv.Itoa(int(u.Pct)) + "%" +
		"（" + fmtKTokens(u.Tokens) + " / " + fmtKTokens(u.Window) + " トークン）に達しました。" +
		"このまま続けると、応答の品質低下・ターンの失敗・トークン消費の増大につながります。" +
		"ヘッダのコンテキストバー右にある「圧縮」で要約だけを新しいセッションへ引き継いで続行するか、" +
		"区切りの良いところで新しいチャットを開くことを検討してください。"
}

// fmtKTokens は 1000 以上を「123k」に丸める表示用ヘルパー。
func fmtKTokens(n int) string {
	if n >= 1000 {
		return strconv.Itoa(n/1000) + "k"
	}
	return strconv.Itoa(n)
}
