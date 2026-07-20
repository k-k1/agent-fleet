package main

// アシスタントチャットのコンテキスト使用量の捕捉と逼迫通知（docs/33）。
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
	"strconv"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/notice"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
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
type claudeUsage struct {
	InputTokens              int           `json:"input_tokens"`
	CacheCreationInputTokens int           `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int           `json:"cache_read_input_tokens"`
	Iterations               []claudeUsage `json:"iterations,omitempty"`
}

// contextTokens は入力側スナップショットの合計 = コンテキスト占有量。
func (u claudeUsage) contextTokens() int {
	return u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
}

// claudeModelUsage は result イベントの modelUsage エントリのうち必要な分。
// contextWindow はモデルの実ウィンドウ（recorded として使える）。
type claudeModelUsage struct {
	ContextWindow int `json:"contextWindow"`
}

// claudeCtx は 1 回の claude 実行のイベント列からコンテキスト占有を追跡する。
// stream では assistant イベント毎の message.usage を、非 stream では result の
// usage.iterations 末尾を最終スナップショットとして採る。
type claudeCtx struct {
	snap   claudeUsage
	window int
	model  string
}

// observeAssistant は stream の assistant イベント（message.usage）を反映する。
// 同一メッセージのイベントは同じ usage を重複して運ぶので、単純に最後勝ちでよい。
func (t *claudeCtx) observeAssistant(model string, u claudeUsage) {
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
func (t *claudeCtx) observeResult(u claudeUsage, modelUsage map[string]claudeModelUsage) {
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

// apply は追跡結果を会話へ格納する（成功ターンの saveConv 前に呼ぶ）。
func (t *claudeCtx) apply(c *chatConversation) {
	setChatContext(c, t.snap.InputTokens, t.snap.CacheReadInputTokens,
		t.snap.CacheCreationInputTokens, t.window, t.model)
}

// codexUsage は codex exec --json の turn.completed が運ぶ usage。input_tokens は
// cached_input_tokens を含む（rollout の token_count と同じ流儀）。ターン合算値
// なので、ツール多段ターンではコンテキスト占有として過大側の近似になる — チャット
// の大半（ツールなし 1 呼び出し）では正確で、警告用途には安全側。
type codexUsage struct {
	InputTokens       int `json:"input_tokens"`
	CachedInputTokens int `json:"cached_input_tokens"`
}

// opencodeUsage は opencode run --format json の step_finish が運ぶ part.tokens。
// input はキャッシュ分を含まない（SQLite ストアの message.data.tokens と同じ形）。
type opencodeUsage struct {
	Input int `json:"input"`
	Cache struct {
		Read  int `json:"read"`
		Write int `json:"write"`
	} `json:"cache"`
}

// setChatContext は共通の格納口。スナップショットが空のターン（usage の取れない
// プロバイダ経路・空応答）では何もせず前回値を残す。window が取れなければモデル名
// から推定する（contextWindowGuess — get_session_usage と同じ）。
func setChatContext(c *chatConversation, fresh, read, create, window int, model string) {
	if fresh < 0 {
		fresh = 0
	}
	tokens := fresh + read + create
	if tokens <= 0 {
		return
	}
	source := "recorded"
	if window <= 0 {
		window, source = contextWindowGuess(model, tokens), "estimated"
	}
	u := &contextUsage{
		Tokens: tokens, Read: read, Create: create, Fresh: fresh,
		Window: window, WindowSource: source, Model: model,
	}
	if window > 0 {
		u.Pct = float64(tokens) / float64(window) * 100
	}
	c.Context = u
}

// chatCtxModelFor はウィンドウ推定に使うモデル名: 会話に固定があればそれ、なければ
// バックエンド毎の既定（codex/opencode は作成時に snapshot されるので、空は主に
// 旧 claude 会話）。
func chatCtxModelFor(c *chatConversation) string {
	if c.Model != "" {
		return c.Model
	}
	switch c.Agent {
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
func noteContextPressure(c *chatConversation) {
	u := c.Context
	if u == nil || u.Pct < chatCtxWarnPct {
		c.CtxWarned = false
		return
	}
	if c.CtxWarned {
		return
	}
	c.CtxWarned = true
	c.Messages = append(c.Messages, chatMessage{
		Role: "notice", Content: ctxPressureContent(u), TS: nowMs(),
	})
	ev := notice.New("chat-context-pressure", "", "", c.Title)
	ev.Payload["conversation_id"] = c.ID
	ev.Payload["conversationTitle"] = c.Title
	_ = notice.Put(ev)
}

// ctxPressureContent は逼迫 notice の本文。保存される会話データは JA で統一
// （docs/19: 報告・notice と同じ流儀）。
func ctxPressureContent(u *contextUsage) string {
	return "この会話のコンテキスト使用量が上限の約" + strconv.Itoa(int(u.Pct)) + "%" +
		"（" + fmtKTokens(u.Tokens) + " / " + fmtKTokens(u.Window) + " トークン）に達しました。" +
		"このまま続けると、応答の品質低下・ターンの失敗・トークン消費の増大につながります。" +
		"区切りの良いところで新しいチャットを開き、必要な要点だけを引き継ぐことを検討してください。"
}

// fmtKTokens は 1000 以上を「123k」に丸める表示用ヘルパー。
func fmtKTokens(n int) string {
	if n >= 1000 {
		return strconv.Itoa(n/1000) + "k"
	}
	return strconv.Itoa(n)
}
