package main

// 保留中の対話の持ち越し（docs/75 §75.6）。
//
// モーダルを出したまま畳まれたセッションの「意図」を持ち越し、再開後に**文章として**
// 配達する。モーダルそのものは戻せない — claude を --resume しても未応答の tool_use は
// 親ポインタで迂回され、会話木から外れる（docs/75 §75.10 A で実測）。
//
// ここが持つのは 3 つ: 昇格（pending-* → carried）、配達文面の組み立て、配達の入口。
// 文面はテスト可能な純関数に切ってある — 実測（§75.10 C）で分かったとおり、この文面は
// 飾りではなく**機能そのもの**で、「質問し直すな」の 1 行が無いと claude は質問し直す。

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// promoteCarriedFor は畳まれようとしている（あるいは既に畳まれた）セッションの
// 保留ペイロードを持ち越しへ昇格させる。claude 以外は pending-* を持たないので何もしない。
func promoteCarriedFor(m session.Meta) bool {
	if normalizeKind(m.Kind) != session.KindClaude {
		return false
	}
	// まだ生きている＝これから halt が殺すところ。既に死んでいる＝ Workspace 停止 /
	// クラッシュ / 利用者の /exit を一覧が見つけたところ。
	reason := "stopped"
	if sessionAlive(m) {
		reason = "halt"
	}
	return status.PromoteCarried(session.UUID(m.Dir, m.Name), reason)
}

// oneLine は TUI へ打鍵する文字列を 1 行へ畳む。
//
// ★これは飾りではない: {t} は tmux send-keys -l でバイト列がそのままペインに載るので、
// 改行は LF としてペインに届き Enter として作用する（docs/dev/92）。複数行の配達文は
// 途中で送信され、残りが次のプロンプトや別のモーダルへ落ちる。
func oneLine(s string) string {
	r := strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ", "\t", " ")
	return strings.Join(strings.Fields(r.Replace(s)), " ")
}

// CarriedAnswer is one question's answer: the picked option labels (multi-select can
// have several) plus optional free text.
type CarriedAnswer struct {
	Labels []string `json:"labels,omitempty"`
	Notes  string   `json:"notes,omitempty"`
}

// carriedPreamble は配達文の頭。**実測で効くことを確認した文言**（§75.10 C）:
// これが無いと claude は「質問しろ」という元の指示が未達だと解釈して質問し直す。
const carriedPreamble = "（停止前に未回答だった質問への回答です。質問し直さず、この回答を使って作業を続けてください）"

// buildCarriedQuestionPrompt renders the carried answer as the single line typed into
// the resumed TUI. 質問文を必ず一緒に運ぶのは、その質問が**会話から消えている**ため —
// 回答だけ送っても claude には何への回答か分からない。
func buildCarriedQuestionPrompt(qs []transcript.Question, answers []CarriedAnswer) string {
	parts := make([]string, 0, len(answers))
	for i, a := range answers {
		q := ""
		if i < len(qs) {
			q = oneLine(qs[i].Question)
			if q == "" {
				q = oneLine(qs[i].Header)
			}
		}
		labels := make([]string, 0, len(a.Labels))
		for _, l := range a.Labels {
			if l = oneLine(l); l != "" {
				labels = append(labels, l)
			}
		}
		// 選択も自由入力も無い質問は**丸ごと落とす**。「選ばれていません」と送っても
		// claude 側にその質問の記憶は無いので、意味を持たない 1 文が増えるだけ。
		seg := ""
		if len(labels) > 0 {
			seg = strings.Join(labels, "・")
			if q != "" {
				seg = "「" + q + "」= " + seg
			}
		} else if oneLine(a.Notes) != "" && q != "" {
			seg = "「" + q + "」= "
		}
		if notes := oneLine(a.Notes); notes != "" {
			if seg == "" {
				seg = notes
			} else {
				seg += "（補足: " + notes + "）"
			}
		}
		if seg != "" {
			parts = append(parts, seg)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return carriedPreamble + strings.Join(parts, " / ")
}

// buildCarriedPlanPrompt renders the carried plan decision.
//
// ★承認は「承認」ではなく**実行の指示**である（§75.10 E の実測）。文章で承認を送ると
// claude は ExitPlanMode を出し直さずそのまま実行するので、呼び出し側（Console）は
// これを取り消せない決定として扱うこと。
func buildCarriedPlanPrompt(approve bool, feedback string) string {
	fb := oneLine(feedback)
	if approve {
		s := "（停止前に承認待ちだった計画を承認します）さきほど提示した計画のとおり進めてください。"
		if fb != "" {
			s += " 補足: " + fb
		}
		return s
	}
	s := "（停止前に承認待ちだった計画は承認しません）"
	if fb != "" {
		return s + fb
	}
	return s + "計画を見直して、あらためて提示してください。"
}

// buildCarriedPermissionPrompt renders the permission case. 許可の答えは死んだツール
// 呼び出しには届かないので、運ぶのは**事実**だけ — 「続けろ」と、何を訊かれていたか。
func buildCarriedPermissionPrompt(detail string) string {
	d := oneLine(detail)
	s := "（停止前に許可を求めていた操作がありましたが、セッションが停止したため回答は届いていません"
	if d != "" {
		s += "。対象: " + d
	}
	return s + "）必要ならもう一度実行を試みて、作業を続けてください。"
}

// carriedQuestions decodes the stored raw AskUserQuestion payload.
func carriedQuestions(c status.Carried) []transcript.Question {
	if len(c.Questions) == 0 {
		return nil
	}
	var qs []transcript.Question
	if json.Unmarshal(c.Questions, &qs) != nil {
		return nil
	}
	return qs
}

// handleSessionCarriedAnswer (POST /sessions/{name}/carried-answer) answers a carried
// interaction: it resumes the session if needed and delivers the decision as ordinary
// prose, then drops the carried entry.
//
// キー列は 1 つも送らない。持ち越しは「モーダルが出ていない」状態なので、Down/Enter は
// 行き先を持たず、生きたペインに当たれば別のものを決めてしまう（AUQ 誤配達クラス）。
func handleSessionCarriedAnswer(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	var req struct {
		Decision string          `json:"decision"` // answer | approve | reject | continue | discard
		Answers  []CarriedAnswer `json:"answers"`
		Feedback string          `json:"feedback"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_body", "invalid JSON body")
		return
	}
	m, ok := session.ReadMeta(name)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	sid := session.UUID(m.Dir, name)
	c, ok := status.ReadCarried(sid)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "no_carried", "this session has no carried interaction")
		return
	}
	// 破棄はセッションを起こさない — カードを消すだけ。
	if req.Decision == "discard" {
		status.RemoveCarried(sid)
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"discarded": true})
		return
	}
	prompt := ""
	switch c.Kind {
	case "question":
		if len(req.Answers) == 0 {
			httpx.WriteErr(w, http.StatusBadRequest, "bad_answers", "answers is required for a carried question")
			return
		}
		prompt = buildCarriedQuestionPrompt(carriedQuestions(c), req.Answers)
	case "plan":
		if req.Decision != "approve" && req.Decision != "reject" {
			httpx.WriteErr(w, http.StatusBadRequest, "bad_decision", "decision must be approve or reject for a carried plan")
			return
		}
		prompt = buildCarriedPlanPrompt(req.Decision == "approve", req.Feedback)
	case "permission":
		prompt = buildCarriedPermissionPrompt(c.Permission)
	}
	if prompt = oneLine(prompt); prompt == "" {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_answers", "nothing to deliver")
		return
	}
	// 生きているセッションに新しいモーダルが出ているなら送らない。持ち越しの文章が
	// そのモーダルの選択操作に化ける（docs/dev/92 の誤配達クラス）。
	if sessionAlive(m) {
		if blocked := promptBlocker(name); blocked != "" {
			writeBlockedErr(w, blocked)
			return
		}
	} else if err := ensureSessionTmux(name, false); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "start_failed", err.Error())
		return
	}
	// 持ち越しはここで消す: 配達は非同期（CLI の起動待ちがある）なので、応答を待つ
	// あいだにカードが二重に押されるのを防ぐ。配達に失敗しても復活はさせない —
	// 会話は転写に残っており、利用者はコンポーザから同じことを言える。
	status.RemoveCarried(sid)
	// バッジは付けない（recordInjection しない）: これは利用者自身の決定で、Console の
	// 質問カードから答えた場合と同じ「ふつうの user 発言」である。オペレーターや peer の
	// 注入と混ぜると、誰の指示かの区別が壊れる。
	go deliverInitialPrompt(name, prompt)
	httpx.WriteJSON(w, http.StatusAccepted, map[string]any{"delivering": true, "prompt": prompt, "kind": c.Kind})
}
