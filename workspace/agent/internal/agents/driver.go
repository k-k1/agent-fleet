// Driver 層の型（docs/log/27 §3〜§6、P1.5）。read 層（Agent IF）を無傷のまま、その上に
// thread 単位の制御（write）と購読（live）を増築するための seam。P1.5 で確定するのは
// この型と HTTP 受け口（POST /sessions/{name}/turn・/respond、package main の
// session_turn.go）まで。managed 実装（ThreadHandle）は P2（opencode serve）が初出で、
// P3（codex app-server）が第 2 実装として型の妥当性を検証する。
//
// TUI ルート（従来の tmux 内 TUI）は ThreadHandle を実装しない — Events/Snapshot を
// 持てない TUI に interface を無理に着せず、/turn ハンドラが既存の tmux 経路
// （session_io.go の type+submit / send-keys）へ直接委譲する。Console はどちらの
// ドライバでも同じ呼び出しで済む。
package agents

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// TurnInput is the payload of Send (turn/start 相当) and Steer (turn/steer 相当).
type TurnInput struct {
	Prompt string
	// Attachments are absolute paths of files attached to this turn（docs/log/27 §10:
	// managed は tmux 貼り付けの代わりに API 添付へ）。TUI ルートでは Console が
	// 従来どおりパスをプロンプト本文へ織り込むので、この欄を解釈するのは managed
	// driver（P2/P3: codex turn/start の input items / opencode serve の file part）だけ。
	Attachments []string
	// ClientMessageID is the AF-issued idempotency key（§4）: 再送・reconnect 後の
	// 二重投入を冪等化する。P1.5 ではワイヤで受け渡すだけで、台帳（会話内容を含まない
	// 運用メタデータ、§9.5）は P2 の turn 状態機械と同時に導入する。
	ClientMessageID string
}

// ThreadSettings is a dynamic settings update（§9.4-3: 稼働中セッションのモデル/effort
// 変更が managed で初めて可能になる）。空フィールドは「変更しない」。
type ThreadSettings struct {
	Model       string
	Effort      string
	Mode        string // "plan" | "normal"（TranscriptData.Mode と同語彙）
	ClearModel  bool   // explicit reset to the runtime/provider default
	ClearEffort bool   // explicit reset to the selected model's default
}

// Interaction は承認・質問・plan 確認の一般化（§5）。初期実装スコープは question 系
// のみ（3 者とも承認は bypass 運転のため、実運用の対象は質問だけ）。質問フォームの
// 本体は既存 Pending UI と同じ transcript.Question の列で持つ — claude の
// AskUserQuestion は複数質問を 1 モーダルで出すため、設計書 §5 の単一 Options より
// この形が実態に合う（P1.5 での精緻化）。
type Interaction struct {
	ID        string
	Kind      string // "question"（将来: "approval" | "plan"）
	Prompt    string // 質問前に流れた説明文（ミラーの pendingText 相当）
	Questions []transcript.Question
}

// Decision is the reply verb for an Interaction（§5）.
type Decision string

const (
	DecisionAllow  Decision = "allow"
	DecisionDeny   Decision = "deny"
	DecisionCancel Decision = "cancel"
	DecisionAnswer Decision = "answer" // question 系: Answers が本体
)

// Scope is how long an allow/deny decision sticks（§5）.
type Scope string

const (
	ScopeOnce   Scope = "once"
	ScopeTurn   Scope = "turn"
	ScopeThread Scope = "thread"
)

// InteractionAnswer is one question's answer inside a reply. A multi-question form
// (claude AskUserQuestion) replies with one entry per question, in order.
type InteractionAnswer struct {
	Text    string `json:"text,omitempty"`    // 自由入力（"Type something" 相当）
	Options []int  `json:"options,omitempty"` // 選択肢 index（複数選択は複数個）
}

// InteractionReply is the wire body of POST /sessions/{name}/respond and the
// argument of ThreadHandle.Respond.
type InteractionReply struct {
	ID       string              `json:"id"`
	Decision Decision            `json:"decision"`
	Scope    Scope               `json:"scope,omitempty"`
	Answers  []InteractionAnswer `json:"answers,omitempty"`
}

// TurnState is the turn 状態機械の状態（§4）。既存の WireLive 語彙（working / idle /
// question / compacting）へは射影で供給し、ワイヤ契約は変えない。
type TurnState string

const (
	TurnQueued             TurnState = "queued" // ClientMessageID 採番済み・runtime 未投入
	TurnStarting           TurnState = "starting"
	TurnRunning            TurnState = "running"
	TurnWaitingInteraction TurnState = "waiting_interaction"
	TurnInterrupting       TurnState = "interrupting"
	TurnCompleted          TurnState = "completed"
	TurnFailed             TurnState = "failed"
	TurnCancelled          TurnState = "cancelled"
	TurnUnknown            TurnState = "unknown" // 切断時の正直な状態 — §6 の手順で解決
	// TurnAborted は「回答を出す前に途中で落ちたが、再送すれば続きから走れる」ターン
	// （接続断・一時的なレート制限など）。TurnFailed と分けるのは、原因が解消しない
	// 限り再送が無意味な失敗（残高切れ・プロンプト長超過）と、再送で直る中断とで
	// オペレーターに促すべき行動が正反対だから（docs/log/47）。
	TurnAborted TurnState = "aborted"
)

// Event is a live notification from a managed runtime. 語彙の確定は購読実装と同時
// （P2）に行うため、P1.5 では包括形に留める。イベントは再生されない（EventReplay
// なし前提）— 欠落は Snapshot 照合（§6）で回復する。
type Event struct {
	Kind        string // "turn_state" | "interaction" | "settings"（P2 で確定・拡張）
	TurnState   TurnState
	Interaction *Interaction
	Settings    *ThreadSettings
}

// ThreadSnapshot is the reconciliation (§6) view of a thread's現在地: 切断・daemon
// 再起動後にネイティブ履歴（read 層）と照合して turn 状態を確定するための材料。
type ThreadSnapshot struct {
	TurnState   TurnState
	Interaction *Interaction // waiting_interaction のとき、その中身
	Settings    ThreadSettings
}

// ThreadHandle is thread 単位の write/subscribe（§3）。Driver.Resume が返し、
// プロセスがどこで動いているか（app-server / serve / 世代）は知らない。
type ThreadHandle interface {
	Send(in TurnInput) error  // turn/start 相当
	Steer(in TurnInput) error // turn/steer 相当（実行中 turn への追撃入力）
	Interrupt() error         // turn/interrupt 相当
	UpdateSettings(s ThreadSettings) error
	Respond(reply InteractionReply) error
	Events() <-chan Event
	Snapshot() (ThreadSnapshot, error)
}

// Capabilities は Console が描画を決めるための能力表明（§3.1）。Console は
// `kind == "codex"` 分岐を持たず、ここから affordance を決める（agents.go が kind
// 分岐 50 箇所を Caps に畳んだ規律の延長）。
type Capabilities struct {
	ProcessModel    string // "shared-daemon" | "per-session-child" | "tui"
	Steer           bool
	Fork            bool
	DynamicModel    bool
	DynamicEffort   bool
	DynamicMode     bool
	Permissions     bool // 対応する Interaction 種別（承認）
	Questions       bool // 対応する Interaction 種別（質問）
	EventReplay     bool // 3 者とも false 想定 → 回復は snapshot 照合（§6）
	EphemeralThread bool // 隔離ワンショット thread（chat 統合の将来余地、§9.3）
	TUIAttach       bool // OpenCode のみ true（serve へ TUI を無停止アタッチ）
}

// Driver is the per-kind managed 実装（§3）: read 層（Agent）をそのまま継承し、
// thread 単位の制御・購読を足す。P2 で opencode が最初に実装する。
type Driver interface {
	Agent
	Capabilities() Capabilities
	Resume(m session.Meta) (ThreadHandle, error) // 無ければ新規 start
}
