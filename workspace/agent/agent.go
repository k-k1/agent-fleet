package main

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/agy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/copilot"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/cursor"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/kiro"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// The Agent interface and its input/output types live in internal/agents
// (docs/23 残① Wave C); opencode の実装は internal/agents/opencode（Wave D）、
// codex は internal/agents/codex（Wave E）、claude は internal/agents/claude
// （Wave F）。This file keeps the registry and the shared live-state helpers.

// agentRegistry is the kind → agents.Agent registry. agentOf falls back to claude
// for an unknown or empty kind, matching the historical default (a session with no
// recognized kind launches claude).
var agentRegistry = map[string]agents.Agent{
	session.KindClaude:   claude.New(),
	session.KindOpencode: opencode.New(),
	session.KindCodex:    codex.New(),
	session.KindCursor:   cursor.New(),
	session.KindKiro:     kiro.New(),
	session.KindAgy:      agy.New(),
	session.KindCopilot:  copilot.New(),
	session.KindShell:    shellAgent{},
	session.KindSSM:      ssmAgent{},
}

func agentOf(kind string) agents.Agent {
	if a, ok := agentRegistry[kind]; ok {
		return a
	}
	return agentRegistry[session.KindClaude]
}

// normalizeKind maps a create request's kind onto a registered one, defaulting the
// unknown/empty/"claude" cases to claude (the historical create whitelist).
func normalizeKind(kind string) string {
	if _, ok := agentRegistry[kind]; ok {
		return kind
	}
	return session.KindClaude
}

// --- shared live-state helpers -------------------------------------------------

// sessionAlive is the driver-aware liveness test: tui = the tmux session exists,
// managed = a live runtime handle exists（docs/27 P2）。wireSession の alive 引数を
// 埋める全ハンドラはこれを使う（tmuxx.HasSession 直叩きは managed を常に停止扱い
// にしてしまう）。
func sessionAlive(m session.Meta) bool {
	if m.DriverKind() == session.DriverManaged {
		return managedAlive(m)
	}
	return tmuxx.HasSession(session.TmuxName(m.Name))
}

// driveState is the live state for the drive endpoints (status/output/messages):
// "stopped" when not alive, else idle-or-recorded. heal self-corrects a stale
// non-idle cache when the claude pane is back at its ready prompt (killed+resumed,
// rejected permission, abandoned question) — /output opts out (heal=false) to match
// its historical behavior.
func driveState(m session.Meta, alive, heal bool) string {
	if !alive {
		return "stopped"
	}
	// opencode: derive state from its own store (the status plugin is unreliable) so the
	// chat chip doesn't stick on 進行中 after a turn the plugin never reported idle for.
	if m.Kind == session.KindOpencode {
		if st := opencode.LiveState(m); st != "" {
			return st
		}
	}
	// agy: no hooks — /input persists an optimistic "working" that nothing clears
	// while an interactive prompt is up (the question/permission widget replaces the
	// idle footer, so the claude-shaped heal below can't fire and the chat showed a
	// blocked session as 作業中). The conversation DB knows the whole state (last step
	// status — agy/pending.go), so ask it first.
	if m.Kind == session.KindAgy {
		if st := agy.LiveState(m); st != "" {
			// Turn end. agy has no Stop hook, so this poll is the only place the
			// completion can be observed — persist idle AND fire the notification,
			// or the operator's 完了報告 arm is never consumed and the report never
			// arrives (docs/30 ②). MarkTurnEnd shares recordSessionNotification with
			// the hook route, so "which transition counts" stays one implementation.
			// Gated on previous=="working" so repeated polls report once; a duplicate
			// from two concurrent polls is absorbed by handleChatReport's disarm.
			if st == "idle" && status.LiveState(session.UUID(m.Dir, m.Name)) == "working" {
				agents.MarkTurnEnd(session.UUID(m.Dir, m.Name), agents.TurnCompleted)
			}
			return st
		}
	}
	// copilot: no hooks either — events.jsonl 由来の分類が唯一の状態ソース
	// （state.go。managed でも子プロセスが同じファイルを書くので整合する）。
	// agy と同じく、この poll が turn 完了の唯一の観測点（TUI ルート）なので
	// working→idle の遷移で MarkTurnEnd を発火する（docs/30 ②）。managed は
	// driver の runTurn が MarkTurnEnd 済みで status も idle — 二重発火しない。
	if m.Kind == session.KindCopilot {
		if st := copilot.LiveState(m); st != "" {
			if st == "idle" && status.LiveState(session.UUID(m.Dir, m.Name)) == "working" {
				agents.MarkTurnEnd(session.UUID(m.Dir, m.Name), agents.TurnCompleted)
			}
			return st
		}
	}
	// cursor: no hooks either — JSONL 転写末尾の分類が状態ソース（state.go）。
	// copilot と同型で、この poll が turn 完了の唯一の観測点（TUI ルート）なので
	// working→idle の遷移で MarkTurnEnd を発火する（docs/30 ②）。managed（Track A2）
	// は driver の runTurn が MarkTurnEnd 済みで status も idle — 二重発火しない。
	if m.Kind == session.KindCursor {
		if st := cursor.LiveState(m); st != "" {
			if st == "idle" && status.LiveState(session.UUID(m.Dir, m.Name)) == "working" {
				agents.MarkTurnEnd(session.UUID(m.Dir, m.Name), agents.TurnCompleted)
			}
			return st
		}
	}
	// kiro: no hooks — 2.14.1 は Stop hook を持たない（hook トリガは AgentSpawn/
	// PrePrompt/PreToolUse/PostToolUse のみ・実測 docs/43 §5-1）ので、状態源は TUI
	// 文字列契約（state.go）。cursor/copilot と同型で、この poll が turn 完了の唯一の
	// 観測点（TUI ルート）なので working→idle の遷移で MarkTurnEnd を発火する（docs/30
	// ②）。承認待ち（"question"）は idle でないので発火せず素通し。managed（Track A2）は
	// driver の runTurn が MarkTurnEnd 済みで status も idle — 二重発火しない。空（フッタ
	// 未描画）のときは generic 経路（/input の楽観 working）へフォールバックする。
	if m.Kind == session.KindKiro {
		if st := kiro.LiveState(m); st != "" {
			if st == "idle" && status.LiveState(session.UUID(m.Dir, m.Name)) == "working" {
				agents.MarkTurnEnd(session.UUID(m.Dir, m.Name), agents.TurnCompleted)
			}
			return st
		}
	}
	sid := session.UUID(m.Dir, m.Name)
	// WireLive と同じ解決（status.EffectiveModal）: 質問/プランのペイロードが捕捉されて
	// いる permission は、TUI が実際に出しているモーダル（question / plan）で名乗る。
	// 一覧のバッジ（WireLive）とチャットのチップ（ここ）は同じ状態を見せる必要がある。
	state := status.EffectiveModal(sid, status.LiveState(sid))
	isClaude := normalizeKind(m.Kind) == session.KindClaude
	// ペインを読むのは 1 回だけ（tmuxx.ReadPane）。heal=false（/output）は従来どおり
	// ペインを見ないが、claude の上限モーダルだけは heal に関係なく報告する必要がある
	// ので、その 2 つのどちらかが要るときにキャプチャする。
	var pane tmuxx.PaneRead
	if heal || isClaude {
		pane = tmuxx.ReadPane(m.Name)
	}
	// claude が利用上限メニューでペインを人間待ちに固定している状態（agents.StateBlocked
	// のコメント参照）。WireLive と同じ判定をここでも行うのは、チャット/ミラーが見るのは
	// この関数だからで、片側だけ直すと一覧と本文でチップが食い違う。状態の書き換え
	// （HealIdle）は heal 側にだけ寄せる。
	// 認証切れ（docs/47 §4-8）。WireLive と同じ判定を同じ順（上限メニューより先）でここでも
	// 行う — ミラー／チャットのチップと一覧のバッジが食い違うと、どちらが本当か利用者には
	// 分からない。順序の理由は WireLive 側のコメント参照（待っても直らない方を先に出す）。
	if isClaude && claude.AuthExpired() {
		if heal && state != "idle" {
			claude.HealIdle(sid)
		}
		return agents.StateAuth
	}
	if isClaude && pane.RateLimitMenu {
		if heal && state != "idle" {
			claude.HealIdle(sid)
		}
		return agents.StateBlocked
	}
	// codex managed: a turn rejected/failed with usageLimitExceeded leaves the
	// session sitting at idle, but re-sending will hit the same limit. Surface it
	// with the same "blocked" badge as Claude's usage-limit menu.
	if m.DriverKind() == session.DriverManaged && normalizeKind(m.Kind) == session.KindCodex && state == "idle" && codex.IsRateLimited(m.Name) {
		return agents.StateBlocked
	}
	if heal && state != "idle" && pane.Idle {
		state = "idle"
		// claude は「API エラーでターンが落ちた」を transcript 末尾から見分けられるので、
		// その 1 ケースだけ黙って消さず終端イベントとして通知する（docs/47）。判別材料が
		// claude の jsonl 形式に固有なので、他 kind は従来どおりマーカー削除のみ。
		// normalizeKind: 空 kind の旧セッションも claude なので、生の比較では取り逃がす。
		if isClaude {
			claude.HealIdle(sid)
		} else {
			status.Remove(sid)
		}
	} else if heal && state == "idle" && pane.Busy {
		// Reverse-heal: the hook state reads idle (its "working" file was never written,
		// or the self-heal above removed it during a transient prompt frame) but the pane
		// is plainly mid-turn (interrupt affordance shown). Trust the live TUI and persist
		// working so the chat shows 進行中 + the stop button, and the eventual Stop still
		// fires the answer-ready notification (recorded off the previous "working" state).
		state = "working"
		status.Persist(sid, "working")
	}
	return state
}
