package sessionx

// オペレーター（af_write アシスタント）からの AskUserQuestion 回答と
// ExitPlanMode プラン承認/却下（docs/log/30）。
//
// POST /sessions/{name}/answer-question {choices:[1,2,…]} — 質問順に 1-based の
// 選択肢番号を1つずつ渡し、フォーム全体を一括で回答する。ブリッジ（docs/log/37 P2b、
// ボタン1押し=1問の逐次蓄積）と違い、呼び出し元（オペレーターの MCP ツール）は
// 全質問の回答を一度に決めて渡す。適用はブリッジと同じ経路を共有する:
//   - TUI claude: hooks が記録した pending 質問を検証し、Console/ブリッジと同じ
//     単一選択キー列（buildClaudeSingleSelectKeys）で TUI モーダルを駆動する。
//   - managed: driver の live Interaction を読み直して構造化回答（Respond）。
// 自由入力（Other）と multiSelect は v1 対象外 — Console へ誘導する
// （TUI モーダルはタイプ文字を無視するため自由入力は構造的に不可能）。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// validateAnswerChoices checks a full-form answer against the pending questions:
// one 1-based pick per question, each within its option range, no multi-select
// (v1: single-select only — the TUI key path can't express toggles safely and the
// operator flow relays a single user decision). Returns the picked labels.
func validateAnswerChoices(qs []transcript.Question, choices []int) ([]string, error) {
	if len(qs) == 0 {
		return nil, fmt.Errorf("no pending question")
	}
	if len(choices) != len(qs) {
		return nil, fmt.Errorf("choices must have exactly %d entries (one 1-based pick per question), got %d", len(qs), len(choices))
	}
	labels := make([]string, len(qs))
	for i, q := range qs {
		if q.MultiSelect {
			return nil, fmt.Errorf("question %d is multi-select — answer it from the Console", i+1)
		}
		c := choices[i]
		if c < 1 || c > len(q.Options) {
			return nil, fmt.Errorf("choice %d for question %d is out of range (1..%d)", c, i+1, len(q.Options))
		}
		labels[i] = q.Options[c-1].Label
	}
	return labels, nil
}

// picksOf converts validated 1-based choices to the 0-based per-question pick map
// shared with the bridge key/answer builders.
func picksOf(choices []int) map[int]int {
	picks := map[int]int{}
	for i, c := range choices {
		picks[i] = c - 1
	}
	return picks
}

// applyManagedAnswerAll answers a managed session's pending question form in one
// call: snapshot the live interaction, validate the full-form choices against it,
// Respond. The live re-read (same as the bridge / /respond) makes staleness
// self-guarding — an already-answered or changed form fails validation or Respond.
func applyManagedAnswerAll(h agents.ThreadHandle, name string, choices []int) ([]string, error) {
	snap, err := h.Snapshot()
	if err != nil {
		return nil, err
	}
	inter := snap.Interaction
	if inter == nil || inter.Kind != "question" || len(inter.Questions) == 0 {
		return nil, errNoPendingQuestion
	}
	labels, err := validateAnswerChoices(inter.Questions, choices)
	if err != nil {
		return nil, err
	}
	reply := agents.InteractionReply{
		ID: inter.ID, Decision: agents.DecisionAnswer,
		Answers: buildInteractionAnswers(picksOf(choices), len(inter.Questions)),
	}
	if err := h.Respond(reply); err != nil {
		return nil, err
	}
	markSessionWorking(name)
	clearBridgeAnswer(name)
	return labels, nil
}

var errNoPendingQuestion = fmt.Errorf("no pending question")

// HandleSessionAnswerQuestion (POST /sessions/{name}/answer-question) applies a
// full-form AskUserQuestion answer from the operator's MCP tool.
func HandleSessionAnswerQuestion(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	var req struct {
		Choices []int `json:"choices"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_body", "invalid JSON body")
		return
	}
	if len(req.Choices) == 0 {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_choices", "choices (1-based option per question) is required")
		return
	}
	meta, ok := session.ReadMeta(name)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}

	if meta.DriverKind() == session.DriverManaged {
		d, ok := driverOf(meta)
		if !ok {
			httpx.WriteErr(w, http.StatusNotImplemented, "driver_unavailable",
				"managed driver はこの kind ではまだ利用できません")
			return
		}
		h, err := d.Resume(meta)
		if err != nil {
			httpx.WriteErr(w, http.StatusBadGateway, "runtime_failed", err.Error())
			return
		}
		labels, err := applyManagedAnswerAll(h, name, req.Choices)
		if err != nil {
			if err == errNoPendingQuestion {
				httpx.WriteErr(w, http.StatusConflict, "no_question", "no pending question (already answered?)")
				return
			}
			httpx.WriteErr(w, http.StatusConflict, "answer_failed", err.Error())
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"answered": name, "picked": labels})
		return
	}

	// TUI: only claude records a pending question payload (hooks); other TUI kinds
	// have no structured form to answer.
	if meta.Kind != session.KindClaude {
		httpx.WriteErr(w, http.StatusNotImplemented, "answer_unsupported",
			"この kind の質問は Console から回答してください")
		return
	}
	sid := session.UUID(meta.Dir, meta.Name)
	raw, ok := status.ReadPendingQuestion(sid)
	if !ok || len(raw) == 0 {
		httpx.WriteErr(w, http.StatusConflict, "no_question", "no pending question (already answered?)")
		return
	}
	var qs []transcript.Question
	if err := json.Unmarshal(raw, &qs); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "bad_pending", "could not parse the pending question")
		return
	}
	labels, err := validateAnswerChoices(qs, req.Choices)
	if err != nil {
		httpx.WriteErr(w, http.StatusConflict, "answer_failed", err.Error())
		return
	}
	pane, err := claudePane(name)
	if err != nil {
		httpx.WriteErr(w, http.StatusConflict, "not_running", err.Error())
		return
	}
	if err := sendNamedKeys(pane, buildClaudeSingleSelectKeys(len(qs), picksOf(req.Choices))); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "tmux_failed", err.Error())
		return
	}
	markSessionWorking(name)
	clearBridgeAnswer(name)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"answered": name, "picked": labels})
}

// HandleSessionPlanRespond (POST /sessions/{name}/plan-respond {decision, feedback})
// applies the operator's decision to a pending ExitPlanMode approval (claude TUI —
// plan mode is a claude concept).
//
//   - approve: Enter on the approval dialog（ブリッジ planKeys と同じ・オプション先頭が
//     承認である契約は Console mirror と共通）。
//   - reject: Escape で承認ダイアログを閉じて中断し、feedback があればコンポーザ復帰を
//     待ってから修正指示として送信する。位置固定キー（Down×3）での却下は CLI 更新で
//     承認に化けた実測があるため使わない（中断→通常プロンプトが版に依存しない）。
//     フィードバックをここで送り切るのは、plan モーダルが開いたまま send_to_session
//     すると本文がモーダルに食われ Enter が承認になる誤爆経路を閉じるため。
func HandleSessionPlanRespond(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	var req struct {
		Decision string `json:"decision"`
		Feedback string `json:"feedback"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_body", "invalid JSON body")
		return
	}
	if req.Decision != "approve" && req.Decision != "reject" {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_decision", "decision must be approve or reject")
		return
	}
	meta, ok := session.ReadMeta(name)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	if meta.Kind != session.KindClaude {
		httpx.WriteErr(w, http.StatusNotImplemented, "plan_unsupported",
			"プラン承認は claude セッションのみ対象です")
		return
	}
	sid := session.UUID(meta.Dir, meta.Name)
	// effectiveModal, not the raw state: ExitPlanMode's own permission_prompt overwrites
	// "plan" with "permission" while its approval dialog is still up (session_status.go),
	// and judging by the raw state refused every decision taken after that hook fired.
	if st, _ := status.Read(sid); effectiveModal(sid, st.State) != "plan" {
		httpx.WriteErr(w, http.StatusConflict, "no_plan", "no pending plan approval (already handled?)")
		return
	}
	pane, err := claudePane(name)
	if err != nil {
		httpx.WriteErr(w, http.StatusConflict, "not_running", err.Error())
		return
	}
	if req.Decision == "approve" {
		if err := sendNamedKeys(pane, []string{"Enter"}); err != nil {
			httpx.WriteErr(w, http.StatusInternalServerError, "tmux_failed", err.Error())
			return
		}
		markSessionWorking(name)
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"responded": name, "decision": "approve"})
		return
	}
	// reject: interrupt the dialog, then (best-effort) deliver the feedback once the
	// composer is back. If the composer never shows, report it so the operator falls
	// back to send_to_session (delivery-confirmed) instead of silently losing text.
	if err := sendNamedKeys(pane, []string{"Escape"}); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "tmux_failed", err.Error())
		return
	}
	resp := map[string]any{"responded": name, "decision": "reject"}
	if req.Feedback != "" {
		delivered := false
		for i := 0; i < 20; i++ { // ~5s: the dialog closes fast; boot-like stalls don't apply
			time.Sleep(250 * time.Millisecond)
			if tmuxx.AtIdlePrompt(name) {
				delivered = typeLineAndSubmit(name, pane, req.Feedback) == nil
				break
			}
		}
		if delivered {
			markSessionWorking(name)
		}
		resp["feedback_delivered"] = delivered
		if !delivered {
			resp["hint"] = "コンポーザ復帰を確認できませんでした。send_to_session でフィードバックを送ってください"
		}
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}
