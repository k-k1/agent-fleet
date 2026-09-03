package main

// docs/log/27 P1.5/P2: セッション操作の意味論エンドポイント。Console のチャット送信・追撃・
// 中断を turn/start・turn/steer・turn/interrupt 相当の操作として受け、質問応答を
// Interaction 応答（§5）として受ける。managed driver のセッションは driverOf →
// ThreadHandle へ落ち（P2: opencode serve / P3: codex app-server）、tui（従来）の
// セッションは既存の tmux 経路へ委譲する — Console はドライバの別を知らずに同じ
// 呼び出しで済む。/input（生 keys/seq 駆動）は CLI ルートの TUI モーダル操作用に
// 従来どおり残る（両者の役割分担: /turn = 意味論、/input = 生 TUI 駆動）。

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/copilot"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/cursor"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/kiro"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// managedDrivers is the kind → managed Driver registry（docs/log/27 §3）。P2 で opencode
// が初出、P3 で codex が加わった。tui ドライバはここに載らない（ThreadHandle を実装
// しない — /turn ハンドラが tmux 経路へ直接委譲する）。
var managedDrivers = map[string]agents.Driver{
	session.KindOpencode: opencode.NewDriver(),
	session.KindCodex:    codex.NewDriver(),
	session.KindCopilot:  copilot.NewDriver(),
	session.KindCursor:   cursor.NewDriver(),
	session.KindKiro:     kiro.NewDriver(),
}

// driverOf resolves the managed driver for a session's kind.
func driverOf(m session.Meta) (agents.Driver, bool) {
	d, ok := managedDrivers[m.Kind]
	return d, ok
}

// turnReq is the wire body of POST /sessions/{name}/turn.
type turnReq struct {
	Op     string `json:"op"` // "start" | "steer" | "interrupt"
	Prompt string `json:"prompt"`
	// Attachments: この turn に添付するファイルの絶対パス（docs/log/27 §10）。managed
	// driver だけが解釈する（TurnInput.Attachments → API 添付）。tui では Console が
	// 従来どおりプロンプト本文へパスを織り込むため、ここでは受けるだけで使わない。
	Attachments []string `json:"attachments"`
	// ClientMessageID: AF 採番の冪等キー（docs/log/27 §4）。managed driver の台帳が再送を
	// 冪等化する。空なら driver が採番する。
	ClientMessageID string `json:"clientMessageID"`
}

// handleSessionTurn (POST /sessions/{name}/turn) applies a semantic turn operation
// to a session. start と steer は tui では同じ type+submit に落ちる（実行中 turn への
// 入力は TUI 自身がキューする）が、managed では ThreadHandle.Send / Steer に接続する
// （opencode は driver 内キュー — 完走後に次 turn として投入）。
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
		handleManagedTurn(w, meta, req)
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

// handleManagedTurn routes a turn op to the session's ThreadHandle（docs/log/27 P2）。
// Resume は冪等（生きた handle があればそれを返す）なので毎回呼んでよく、halted →
// 送信 の流れでも runtime が立ち上がる。
func handleManagedTurn(w http.ResponseWriter, meta session.Meta, req turnReq) {
	d, ok := driverOf(meta)
	if !ok {
		httpx.WriteErr(w, http.StatusNotImplemented, "driver_unavailable",
			"managed driver はこの kind ではまだ利用できません")
		return
	}
	h, err := d.Resume(meta)
	if err != nil {
		writeRuntimeErr(w, err)
		return
	}
	switch req.Op {
	case "interrupt":
		if err := h.Interrupt(); err != nil {
			writeRuntimeErr(w, err)
			return
		}
	default: // start / steer
		if strings.TrimSpace(req.Prompt) == "" && len(req.Attachments) == 0 {
			httpx.WriteErr(w, http.StatusBadRequest, "empty_prompt", "prompt is required for start/steer")
			return
		}
		in := agents.TurnInput{Prompt: req.Prompt, Attachments: req.Attachments, ClientMessageID: req.ClientMessageID}
		if req.Op == "steer" {
			err = h.Steer(in)
		} else {
			err = h.Send(in)
		}
		if err != nil {
			if errors.Is(err, agents.ErrQuestionPending) {
				// tui の submitPromptTUI と同じワイヤ契約（Console はこの code で
				// 楽観 echo を取り消し質問カードへ誘導する）— sentinel は driver 共通。
				httpx.WriteErr(w, http.StatusConflict, "question_pending",
					"a question is awaiting an answer; answer it via the question card, not free text")
				return
			}
			writeRuntimeErr(w, err)
			return
		}
		// 楽観 working は tui と同じ動機（送信直後のポーリングが古い idle を読まない）。
		markSessionWorking(meta.Name)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"sent": meta.Name, "op": req.Op})
}

// handleSessionRespond (POST /sessions/{name}/respond) answers a pending
// Interaction（docs/log/27 §5 — 承認/質問の一般形）by id. managed driver 専用の意味論
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
	d, ok := driverOf(meta)
	if !ok {
		httpx.WriteErr(w, http.StatusNotImplemented, "driver_unavailable",
			"managed driver はこの kind ではまだ利用できません")
		return
	}
	h, err := d.Resume(meta)
	if err != nil {
		writeRuntimeErr(w, err)
		return
	}
	if err := h.Respond(reply); err != nil {
		httpx.WriteErr(w, http.StatusConflict, "respond_failed", err.Error())
		return
	}
	// 回答は turn を続行させる — 楽観 working は tui の keys 送信（Enter）と同じ動機。
	markSessionWorking(name)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"responded": name, "id": reply.ID})
}

// settingsReq is the wire body of POST /sessions/{name}/settings — ThreadSettings
// の動的更新（docs/log/27 §9.4-3、managed 専用）。空フィールドは「変更しない」。tui の
// モード切替は従来どおり /input のキー（planCycleKey）で行う。
type settingsReq struct {
	Model       string `json:"model"`
	Effort      string `json:"effort"`
	Mode        string `json:"mode"` // "plan" | "normal"
	ClearModel  bool   `json:"clearModel"`
	ClearEffort bool   `json:"clearEffort"`
}

// handleSessionSettingsGet reports the settings that the managed driver will use
// for the next turn. Reading the latest transcript turn is insufficient: it is stale
// immediately after a settings change and empty before the first prompt.
func handleSessionSettingsGet(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	meta, ok := session.ReadMeta(name)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	if meta.DriverKind() != session.DriverManaged {
		httpx.WriteErr(w, http.StatusNotImplemented, "settings_unsupported",
			"tui セッションの設定はターミナルで変更します")
		return
	}
	d, ok := driverOf(meta)
	if !ok {
		httpx.WriteErr(w, http.StatusNotImplemented, "driver_unavailable",
			"managed driver はこの kind ではまだ利用できません")
		return
	}
	h, err := d.Resume(meta)
	if err != nil {
		writeRuntimeErr(w, err)
		return
	}
	snap, err := h.Snapshot()
	if err != nil {
		writeRuntimeErr(w, err)
		return
	}
	caps := d.Capabilities()
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"model": snap.Settings.Model, "effort": snap.Settings.Effort, "mode": snap.Settings.Mode,
		"dynamicModel": caps.DynamicModel, "dynamicEffort": caps.DynamicEffort, "dynamicMode": caps.DynamicMode,
	})
}

// handleSessionSettings (POST /sessions/{name}/settings) updates a managed
// session's dynamic thread settings（稼働中セッションのモデル/effort/モード変更 —
// managed で初めて可能になる純粋な改善、§9.4-3）。
func handleSessionSettings(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	var req settingsReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_body", "invalid JSON body")
		return
	}
	if req.Mode != "" && req.Mode != "plan" && req.Mode != "normal" {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_mode", `mode must be "plan" or "normal"`)
		return
	}
	if req.Model == "" && req.Effort == "" && req.Mode == "" && !req.ClearModel && !req.ClearEffort {
		httpx.WriteErr(w, http.StatusBadRequest, "empty_settings", "nothing to update")
		return
	}
	meta, ok := session.ReadMeta(name)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	if meta.DriverKind() != session.DriverManaged {
		httpx.WriteErr(w, http.StatusNotImplemented, "settings_unsupported",
			"tui セッションの設定はターミナル（/input のキー操作）で切り替えます")
		return
	}
	// 稼働中セッションのモデル変更も起動と同じ扱いで断る（model_deny.go）。既に走って
	// いるセッションの現行モデルは触らない — 除外設定は「これから選ぶ」を止めるもので、
	// 進行中の作業を巻き戻すものではない。
	if modelHidden(meta.Kind, req.Model) {
		httpx.WriteErr(w, http.StatusBadRequest, "model_hidden", hiddenModelError(strings.TrimSpace(req.Model)))
		return
	}
	d, ok := driverOf(meta)
	if !ok {
		httpx.WriteErr(w, http.StatusNotImplemented, "driver_unavailable",
			"managed driver はこの kind ではまだ利用できません")
		return
	}
	h, err := d.Resume(meta)
	if err != nil {
		writeRuntimeErr(w, err)
		return
	}
	patch := agents.ThreadSettings{
		Model: req.Model, Effort: req.Effort, Mode: req.Mode,
		ClearModel: req.ClearModel, ClearEffort: req.ClearEffort,
	}
	if err := h.UpdateSettings(patch); err != nil {
		writeRuntimeErr(w, err)
		return
	}
	// Persist the desired next-turn settings only after the native update succeeds.
	// This is especially important for opencode, whose driver owns variant/model state
	// in memory because serve has no thread-settings persistence endpoint.
	if req.ClearModel {
		meta.Model = ""
	} else if req.Model != "" {
		meta.Model = req.Model
	}
	if req.ClearEffort {
		meta.Effort = ""
	} else if req.Effort != "" {
		meta.Effort = req.Effort
	}
	if req.Mode != "" {
		meta.Mode = req.Mode
	}
	session.WriteMeta(meta)
	snap, _ := h.Snapshot()
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"updated": name, "model": snap.Settings.Model, "effort": snap.Settings.Effort, "mode": snap.Settings.Mode,
	})
}
