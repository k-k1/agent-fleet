package main

// docs/27 P1.5: セッション操作の意味論エンドポイント。Console のチャット送信・追撃・
// 中断を turn/start・turn/steer・turn/interrupt 相当の操作として受け、質問応答を
// Interaction 応答（§5）として受ける。managed driver のセッションは driverOf →
// ThreadHandle へ落ち（P2: opencode serve / P3: codex app-server）、tui（従来）の
// セッションは既存の tmux 経路へ委譲する — Console はドライバの別を知らずに同じ
// 呼び出しで済む。/input（生の keys/seq 駆動）は CLI ルートの TUI モーダル操作用に
// 従来どおり残る（両者の役割分担: /turn = 意味論、/input = 生 TUI 駆動）。

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// driverOf resolves the managed driver for a session's kind. P1.5 では登録はまだ
// 無い（P2 で opencode が初出）— Console/ワイヤ側の受け皿を先に確定するため、
// seam だけ切っておく。登録が入るまで managed セッションへの /turn・/respond は
// driver_unavailable で正直に落ちる。
func driverOf(m session.Meta) (agents.Driver, bool) {
	return nil, false
}

// turnReq is the wire body of POST /sessions/{name}/turn.
type turnReq struct {
	Op     string `json:"op"` // "start" | "steer" | "interrupt"
	Prompt string `json:"prompt"`
	// Attachments: この turn に添付するファイルの絶対パス（docs/27 §10）。managed
	// driver だけが解釈する（P2/P3 で API 添付へ）。tui では Console が従来どおり
	// プロンプト本文へパスを織り込むため、ここでは受けるだけで使わない。
	Attachments []string `json:"attachments"`
	// ClientMessageID: AF 採番の冪等キー（docs/27 §4）。台帳は P2 の turn 状態機械と
	// 同時に導入するため、P1.5 では受けて応答へ返すだけ。
	ClientMessageID string `json:"clientMessageID"`
}

// handleSessionTurn (POST /sessions/{name}/turn) applies a semantic turn operation
// to a session. start と steer は tui では同じ type+submit に落ちる（実行中 turn への
// 入力は TUI 自身がキューする）が、Console が意味を区別して送ることで managed の
// turn/start・turn/steer（P2/P3）へそのまま接続できる。
func handleSessionTurn(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	var req turnReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_body", "invalid JSON body")
		return
	}
	if req.Op != "start" && req.Op != "steer" && req.Op != "interrupt" {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_op", "op must be start, steer or interrupt")
		return
	}
	meta, ok := session.ReadMeta(name)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	if meta.DriverKind() == session.DriverManaged {
		if _, ok := driverOf(meta); !ok {
			httpx.WriteErr(w, http.StatusNotImplemented, "driver_unavailable",
				"managed driver はこの kind ではまだ利用できません")
			return
		}
		// P2: ThreadHandle.Send/Steer/Interrupt へ接続（TurnInput{Prompt, Attachments,
		// ClientMessageID}）。driverOf が常に false を返す間はここへ到達しない。
		httpx.WriteErr(w, http.StatusNotImplemented, "driver_unavailable", "managed driver は未実装です")
		return
	}
	// tui ルート: 既存の tmux 経路へ委譲。ガード（not_running / no_pane /
	// question_pending）は /input の {prompt} パスと同一に保つ。
	tn := session.TmuxName(name)
	if !tmuxx.HasSession(tn) {
		httpx.WriteErr(w, http.StatusConflict, "not_running", "session is not running; start it first")
		return
	}
	pane := tmuxx.SessionPaneID(tn)
	if pane == "" {
		httpx.WriteErr(w, http.StatusInternalServerError, "no_pane", "could not resolve session pane")
		return
	}
	if req.Op == "interrupt" {
		// チャットの停止ボタン。opencode のサブエージェント詳細ビューでは Escape が
		// ナビゲーションに化けるので、/input の Escape 特例と同じく Up を前置する。
		keys := []string{"Escape"}
		if opencodeInSubagentView(tn) {
			keys = []string{"Up", "Escape"}
		}
		if err := sendNamedKeys(pane, keys); err != nil {
			httpx.WriteErr(w, http.StatusInternalServerError, "tmux_failed", err.Error())
			return
		}
		// Escape は turn を始めないので working は付けない（/input と同じ理由 —
		// Stop hook が発火せず 進行中 に張り付く）。
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"sent": name, "op": req.Op})
		return
	}
	// start / steer — ガード（question_pending / slash は working を付けない）ごと
	// /input の {prompt} パスと同じ配送（submitPromptTUI）に落とす。steer もモーダルが
	// 出ている限り同じ経路で誤答するので、ガードは共通で正しい。
	if strings.TrimSpace(req.Prompt) == "" {
		httpx.WriteErr(w, http.StatusBadRequest, "empty_prompt", "prompt is required for start/steer")
		return
	}
	if !submitPromptTUI(w, name, pane, req.Prompt) {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"sent": name, "op": req.Op})
}

// handleSessionRespond (POST /sessions/{name}/respond) answers a pending
// Interaction（docs/27 §5 — 承認/質問の一般形）by id. managed driver 専用の意味論
// 経路で、tui セッションの質問応答は従来どおり /input の keys/seq（Console が TUI
// モーダルをナビゲーションで駆動する）— TUI モーダルには「id で答える」口がない。
func handleSessionRespond(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	var reply agents.InteractionReply
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&reply); err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_body", "invalid JSON body")
		return
	}
	if reply.ID == "" {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_interaction", "interaction id is required")
		return
	}
	switch reply.Decision {
	case agents.DecisionAllow, agents.DecisionDeny, agents.DecisionCancel, agents.DecisionAnswer:
	default:
		httpx.WriteErr(w, http.StatusBadRequest, "bad_decision", "decision must be allow, deny, cancel or answer")
		return
	}
	meta, ok := session.ReadMeta(name)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	if meta.DriverKind() != session.DriverManaged {
		httpx.WriteErr(w, http.StatusNotImplemented, "respond_unsupported",
			"tui セッションの質問は keys/seq（/input）で TUI モーダルを駆動して答えます")
		return
	}
	if _, ok := driverOf(meta); !ok {
		httpx.WriteErr(w, http.StatusNotImplemented, "driver_unavailable",
			"managed driver はこの kind ではまだ利用できません")
		return
	}
	// P2: ThreadHandle.Respond(reply) へ接続。driverOf が常に false の間は到達しない。
	httpx.WriteErr(w, http.StatusNotImplemented, "driver_unavailable", "managed driver は未実装です")
}
