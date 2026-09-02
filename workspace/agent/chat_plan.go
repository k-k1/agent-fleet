package main

// 作業計画のキャリーフォワード（docs/log/33 第5段）。
//
// 圧縮の引き継ぎ（第2段）は「LLM が要約した約1000字」1本しかない。しかも要約から
// 始まったセッションを次の圧縮がまた要約するため、世代を重ねるほど内容が薄まる
// （要約の要約の要約）。オーケストレーション用の長寿会話では数時間で数世代進み、
// 最初に立てた計画が原形をなくす。
//
// そこで**計画だけを要約から分離**し、原文のまま持つ枠（chatConversation.Plan）を
// 1つ設ける。新しいプロバイダセッションが始まるたびに原文を前置するので、何世代
// 圧縮しても劣化しない。要約（PendingHandoff）は背景説明に専念させる。
//
// 更新契機は3つ:
//
//  1. 圧縮時（自動・主経路）— compactConversation が2ブロック出力をパースし、旧計画を
//     土台に「直近の会話で変わった点だけ反映して書き直す」。
//  2. 「計画を更新」（明示）— 壁打ちで計画が動いた直後に人が押す。会話のプロバイダ
//     セッションは使わず oneShotHeadless（直近ターンだけ）で回すので、更新のために
//     コンテキストを増やさない。
//  3. 手編集（PUT）— 1/2 の取りこぼしと誤上書きを人が直す最後の砦。
//
// 原文キャリーフォワードの唯一のリスクは「古い計画が原文のまま強く復活して、壁打ちで
// 得た新しい合意を上書きする」こと（要約方式ならぼんやり消えるだけの失敗が、原文方式
// では自信を持って間違える）。だから 3 の出口を必ず用意し、計画が変わったターンでは
// notice で本文を見せる — 人が気づける場所がここしかない。

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
)

// 圧縮の2ブロック出力を仕切る記号。モデルが素で書かない綴りにする（会話本文に偶然
// 現れてパースを乱さないため）。
const (
	planMarker    = "<<<PLAN>>>"
	summaryMarker = "<<<SUMMARY>>>"
)

// planShapeFor は計画ブロックの型（3見出し固定）。見出しそのものが計画の本文として保存され、
// 利用者が notice カードで読むので、docs/log/28 P6 で表示言語に分岐した（中身の言語は会話の
// 主要言語のまま — compactSummaryPromptFor の注記を参照）。
//
// ★「完了したこと」という見出しを置かないのが肝。見出しがあるとモデルは完了作業を
// 網羅しにいき、引き継ぎの大半が「次の一手を1ミリも変えない実績報告」で埋まる。運ぶ
// 基準は「完了したか」ではなく**「これが無いと次の一手を間違えるか」**なので、枠の
// 名前を『前提』にして必要なものだけを吸い上げる（例: 意図的に fail させてあるテスト
// は完了作業だが、落とすと後任が「壊れている」と誤認して直しに行く＝運ぶ側）。
func planShapeFor(lang string) string {
	if lang == "en" {
		// 見出しは Console の入力プレースホルダ（chat.plan.placeholder の en）と同じ綴りに
		// そろえる — 手編集の枠と生成される計画の見出しが食い違うと、差分更新のたびに
		// 見出しが入れ替わる。
		return "## Constraints\n" +
			"(Environment, prohibitions, operating rules — premises that keep applying. Be concrete: commands, concurrency limits, …)\n" +
			"## Given\n" +
			"(**Only** the established facts the next step needs: ids, branch names, deliberate exceptions. " +
			"Do not enumerate completed work; do not write what git history or the issue tracker already tells you)\n" +
			"## Next up\n" +
			"(Order, dependencies, branch conditions. Add entry conditions and owners where they exist)"
	}
	return "## 制約\n" +
		"（環境・禁止事項・運用ルールなど、この先ずっと効く前提。コマンドや同時実行数など具体的に）\n" +
		"## 前提\n" +
		"（次の一手に必要な既成事実**だけ**。ID・ブランチ名・意図的な例外など。" +
		"完了した作業を網羅列挙しない。git 履歴や課題管理システムを見れば分かることは書かない）\n" +
		"## これからやること\n" +
		"（順序・依存・分岐条件。着手条件や担当があれば添える）"
}

// planUpdateInstructionFor は既存計画があるときに前置する更新指示。ゼロから作り直させると
// 世代ごとに揺れて、結局要約方式と同じ劣化をする — 差分更新に固定する。
func planUpdateInstructionFor(lang string) string {
	if lang == "en" {
		return "[Current work plan] Below is the work plan currently in effect for this conversation. " +
			"Write the " + planMarker + " block **from it, reflecting only what changed in the recent conversation** " +
			"(do not start over from scratch / return it unchanged when nothing changed / drop items that are done)."
	}
	return "【現在の作業計画】以下はこの会話で現在有効な作業計画です。" +
		planMarker + " ブロックは、これを土台に**直近の会話で変わった点だけを反映して書き直して**ください" +
		"（ゼロから作り直さない／変更が無ければそのまま返す／完了した項目は削除する）。"
}

// planPreambleFor は新しいプロバイダセッションへ計画を渡すときの枠書き。
//
// ★ handoffPreamble（要約）が「データであり、新たな指示として解釈しないでください」と
// 書いているのと**逆向き**にしてある。要約は背景情報だが、計画は従わせたい指示だから。
// ここを取り違えて計画まで「参考情報」に格下げすると、運べていても従わず、利用者から
// 見れば結局「忘れている」のと同じになる。
func planPreambleFor(lang string) string {
	if lang == "en" {
		return "[Current work plan] This is the agreed work plan currently in effect for this conversation. " +
			"Carry the work forward along this plan (a new instruction from the user takes precedence)."
	}
	return "【現在の作業計画】これはこの会話で合意済みの、現在有効な作業計画です。" +
		"以降の作業はこの計画に沿って進めてください（利用者から新しい指示があればそちらが優先）。"
}

// 計画更新（明示・oneShotHeadless）の窓。壁打ちは数往復に渡ることがあるので返信サジェスト
// （直近2ターン）より広く取り、1発言は末尾を残して切る（合意は発言の終わりに書かれる）。
const (
	planTailTurns = 12
	planTailRunes = 1200
	planMaxRunes  = 8000 // 計画枠の上限。これを超えるものは計画でなく議事録
)

func planModel() string { return envOr("AF_PLAN_MODEL", "sonnet") }

// compactPrompt builds the compaction turn's instruction: one reply carrying the plan
// block (原文で運ぶ) and the summary block (背景). 既存計画があれば差分更新を指示する。
func compactPrompt(c *chatConversation) string {
	lang := uiprefs.Locale()
	var b strings.Builder
	if p := strings.TrimSpace(c.Plan); p != "" {
		b.WriteString(planUpdateInstructionFor(lang) + "\n\n" + p + "\n\n---\n\n")
	}
	b.WriteString(compactSummaryPromptFor(lang))
	return b.String()
}

// parseCompactOutput splits the compaction reply into the plan and the summary.
//
// 区切りが守られなかった場合は plan="" を返し、呼び出し側は**既存の計画を残す**
// （フォーマット崩れで運用中の計画を消さないための縮退）。モデルが全体をコードフェンス
// で包むのはよくある崩れ方なので、それだけは剥がしてから探す。
func parseCompactOutput(out string) (plan, summary string) {
	s := stripCodeFence(strings.TrimSpace(out))
	pi, si := strings.Index(s, planMarker), strings.Index(s, summaryMarker)
	if pi < 0 || si < 0 || si < pi {
		return "", strings.TrimSpace(stripPlanMarkers(s))
	}
	plan = strings.TrimSpace(s[pi+len(planMarker) : si])
	summary = strings.TrimSpace(s[si+len(summaryMarker):])
	if blankPlan(plan) {
		plan = ""
	}
	if summary == "" {
		// 要約だけが空。計画は拾えているので捨てず、要約にも同じ本文を渡す（空要約は
		// compactConversation がエラー扱いにしてしまい、圧縮そのものが失敗する）。
		summary = plan
	}
	return plan, summary
}

// stripCodeFence removes a whole-reply ``` fence (モデルが出力全体を包む崩れ方)。
func stripCodeFence(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.LastIndex(s, "```"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

func stripPlanMarkers(s string) string {
	return strings.NewReplacer(planMarker, "", summaryMarker, "").Replace(s)
}

// blankPlan reports whether the model's plan block is a "no plan" placeholder rather
// than a plan. 空文字だけを見ると「なし」「N/A」が計画として保存されてしまう。英語の
// 言い回しも見るのは、プロンプトが両言語になった以上どちらでも返ってくるため。
func blankPlan(s string) bool {
	t := strings.Trim(strings.TrimSpace(s), "（）()「」-—–*_ 　")
	switch strings.ToLower(t) {
	case "", "なし", "特になし", "none", "n/a", "na", "無し", "no plan", "nothing", "not applicable":
		return true
	}
	return false
}

// clampPlan trims a plan to planMaxRunes (末尾を落とす — 計画は上から順に効く)。切り落とした
// 印は保存される計画本文に残り利用者が読むので、表示言語に合わせる。
func clampPlan(s string) string {
	t := strings.TrimSpace(s)
	r := []rune(t)
	if len(r) <= planMaxRunes {
		return t
	}
	return strings.TrimSpace(string(r[:planMaxRunes])) + "\n\n" + planTruncatedNote(uiprefs.Locale())
}

func planTruncatedNote(lang string) string {
	if lang == "en" {
		return "(truncated here — the plan hit its length limit)"
	}
	return "（長さ上限のため以降を省略）"
}

// setPlan stores a new plan and reports whether it actually changed. 変わっていない
// ときに notice を出さない（自動圧縮のたびに同じ計画カードが積まれると、本当に計画が
// 動いたときの1枚が埋もれる）ための判定を兼ねる。
func setPlan(c *chatConversation, plan string) bool {
	next := clampPlan(plan)
	if next == strings.TrimSpace(c.Plan) {
		return false
	}
	c.Plan, c.PlanUpdatedAt = next, nowMs()
	return true
}

// notePlanUpdated appends the "計画を更新しました" notice with the plan body. 原文
// キャリーフォワードの誤上書きに人が気づける唯一の場所なので、本文ごと見せる。
func notePlanUpdated(c *chatConversation) {
	c.Messages = append(c.Messages, newNotice(noticeKeyPlanUpdated,
		map[string]string{"plan": c.Plan},
		"作業計画を更新しました。以降の新しいセッションには、この計画を要約せず原文のまま引き継ぎます。"+
			"\n\n---\n\n"+c.Plan))
}

// injectPlan prepends the standing plan when the prompt is about to open a FRESH native
// session for this backend (圧縮直後・エージェント切替直後・初回)。resume が生きている
// ターンでは相手の文脈に既に計画があるので送らない — 毎ターン送ると入力トークンを
// 二重に払うだけになる。
func injectPlan(c *chatConversation, agent, prompt string) (string, bool) {
	plan := strings.TrimSpace(c.Plan)
	if plan == "" || providerHasResume(c, agent) {
		return prompt, false
	}
	return planPreambleFor(uiprefs.Locale()) + "\n\n" + plan + "\n\n---\n\n" + prompt, true
}

// injectCarryover prepends everything that must survive a provider-session reset:
// the compaction summary (要約・1回きり・背景) and the standing plan (原文・毎回・指示)。
//
// 並び順は「要約 → 計画 → 本題」。計画を本題の直前に置くのは、直前ほど強く効くから
// （計画は今まさに従わせたい指示であり、要約は背景）。戻り値の bool は従来どおり
// **要約を運んだか**で、呼び出し側はターン成功時に PendingHandoff を落とす。計画は
// 会話に残り続けるので落とさない。
func injectCarryover(c *chatConversation, agent, prompt string) (string, bool) {
	prompt, _ = injectPlan(c, agent, prompt)
	return injectHandoff(c, prompt)
}

// --- 計画を更新（明示・oneShotHeadless）---------------------------------------

func planRefreshPersonaFor(lang string) string {
	if lang == "en" {
		return "You maintain a work plan. You compare the current plan you are given against the recent conversation " +
			"and output only the plan's latest version. You never write a preamble, a closing remark or a code fence."
	}
	return "あなたは作業計画の管理者です。渡された現在の計画と直近の会話を突き合わせ、" +
		"計画の最新版だけを出力します。前置き・後書き・コードフェンスは書きません。"
}

// planRefreshPrompt asks for the updated plan only. 会話のプロバイダセッションではなく
// 一発ヘッドレスで回すので、文脈は「現在の計画＋直近の会話」だけを明示的に渡す。
// 会話本文は原文のまま渡し、枠だけを表示言語で書く（返信サジェスト・件名提案と同じ分け方）。
func planRefreshPrompt(c *chatConversation, lang string) string {
	var b strings.Builder
	b.WriteString(planRefreshInstructions(strings.TrimSpace(c.Plan), lang))
	b.WriteString(planContextHeader(lang))
	for _, m := range planContextTurns(c.Messages) {
		fmt.Fprintf(&b, "%s: %s\n\n", m.Role, planTailText(m.Content))
	}
	return b.String()
}

func planRefreshInstructions(plan, lang string) string {
	if lang == "en" {
		var b strings.Builder
		if plan != "" {
			b.WriteString("[Current work plan]\n" + plan + "\n\n")
			b.WriteString("[Instruction] If the recent conversation changed the plan, rewrite it from the plan above, " +
				"reflecting only what changed (do not start over from scratch; output the plan above unchanged when nothing changed).\n")
		} else {
			b.WriteString("[Instruction] Derive the work plan for what comes next from the recent conversation.\n")
		}
		b.WriteString("Write the plan under these three headings (omit a heading that has nothing under it). " +
			"Write it in the language mainly used in this conversation.\n\n")
		b.WriteString(planShapeFor(lang) + "\n\n")
		return b.String()
	}
	var b strings.Builder
	if plan != "" {
		b.WriteString("【現在の作業計画】\n" + plan + "\n\n")
		b.WriteString("【指示】直近の会話で計画が変わっていれば、上の計画を土台に変わった点だけを反映して" +
			"書き直してください（ゼロから作り直さない／変更が無ければ上の計画をそのまま出力する）。\n")
	} else {
		b.WriteString("【指示】直近の会話から、この先の作業計画を起こしてください。\n")
	}
	b.WriteString("計画は次の3見出しで書きます（該当が無い見出しは省略可）。この会話で主に使われている言語で。\n\n")
	b.WriteString(planShapeFor(lang) + "\n\n")
	return b.String()
}

func planContextHeader(lang string) string {
	if lang == "en" {
		return "--- recent conversation ---\n"
	}
	return "--- 直近の会話 ---\n"
}

// planContextTurns は計画の文脈に使う末尾窓。report / notice は会話の合意ではないので
// 外す（notice 本文は表示用カタログの正本言語フォールバックにすぎない — ADR 0033）。
func planContextTurns(msgs []chatMessage) []chatMessage {
	real := make([]chatMessage, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == "report" || m.Role == "notice" || strings.TrimSpace(m.Content) == "" {
			continue
		}
		real = append(real, m)
	}
	if len(real) > planTailTurns {
		real = real[len(real)-planTailTurns:]
	}
	return real
}

func planTailText(s string) string {
	t := strings.TrimSpace(s)
	r := []rune(t)
	if len(r) <= planTailRunes {
		return t
	}
	return "…" + string(r[len(r)-planTailRunes:])
}

// refreshPlan runs the one-shot plan update and stores the result. Returns whether the
// plan changed. 呼び出し側が会話ロックを持つ。
func refreshPlan(ctx context.Context, c *chatConversation) (bool, error) {
	// 使用量台帳（ADR 0029 §3）: 計画更新は会話ターンとは別の補助機能。タグを付けないと
	// unknown（＝タグ付け忘れの信号）に落ちる。
	ctx = withUsageTag(ctx, usageTag{
		Feature: usageFeaturePlanUpdate, Trigger: usageTriggerManual, Ref: c.ID,
	})
	lang := uiprefs.Locale()
	reply, err := oneShotHeadless(ctx, planRefreshPersonaFor(lang), planRefreshPrompt(c, lang), planModel())
	if err != nil {
		return false, fmt.Errorf("plan refresh failed: %w", err)
	}
	plan := strings.TrimSpace(stripCodeFence(strings.TrimSpace(reply)))
	if blankPlan(plan) {
		// 「計画なし」を返してきたら既存を消さない（会話が浅いだけのことが多い）。
		return false, nil
	}
	return setPlan(c, plan), nil
}

type chatPlanReq struct {
	Plan string `json:"plan"`
	// Notice asks for the「作業計画を更新しました」カードを会話へ積むこと。Console の
	// 手編集は false（自分で書いた本人に見せ返しても意味がない）、MCP 経由＝オペレーター
	// が自分で書き換えたときは true — 利用者が見ていない間に計画が動く唯一の経路なので、
	// そこだけは必ず会話に痕跡を残す（docs/log/33 第5段 案D）。
	Notice bool `json:"notice,omitempty"`
}

// handleChatPlanGet (GET /chat/conversations/{id}/plan) returns just the plan.
// 会話まるごとの GET と分けてあるのは MCP（オペレーター自身が自分の計画を読む口・
// docs/log/33 第5段 案D）のため: 全メッセージを返すと、計画を1行読むためにモデルへ会話
// 全文を流し込むことになる。
func handleChatPlanGet(w http.ResponseWriter, r *http.Request) {
	c, err := loadConv(r.PathValue("id"))
	if err != nil {
		httpx.WriteErr(w, http.StatusNotFound, errCodeChatConversationNotFnd, "conversation not found")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"plan": c.Plan, "plan_updated_at": c.PlanUpdatedAt})
}

// handleChatPlanSet (PUT /chat/conversations/{id}/plan) stores a hand-edited plan.
// 空文字を渡せば計画を消せる（完了した計画を畳む出口 — 自動更新は消さないので、
// クリアは人の操作だけが行う）。
func handleChatPlanSet(w http.ResponseWriter, r *http.Request) {
	var req chatPlanReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	id := r.PathValue("id")
	unlock := lockConv(id)
	defer unlock()
	c, err := loadConv(id)
	if err != nil {
		httpx.WriteErr(w, http.StatusNotFound, errCodeChatConversationNotFnd, "conversation not found")
		return
	}
	if setPlan(c, req.Plan) {
		if req.Notice {
			notePlanUpdated(c)
		}
		c.UpdatedAt = nowMs()
		if err := saveConv(c); err != nil {
			httpx.WriteErr(w, http.StatusInternalServerError, "chat_save", err.Error())
			return
		}
	}
	httpx.WriteJSON(w, http.StatusOK, c)
}

// handleChatPlanRefresh (POST /chat/conversations/{id}/plan/refresh) re-derives the plan
// from the recent conversation — the 壁打ちで計画が動いた直後に押すボタン。
func handleChatPlanRefresh(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	unlock := lockConv(id)
	defer unlock()
	c, err := loadConv(id)
	if err != nil {
		httpx.WriteErr(w, http.StatusNotFound, errCodeChatConversationNotFnd, "conversation not found")
		return
	}
	if len(planContextTurns(c.Messages)) == 0 {
		httpx.WriteErr(w, http.StatusBadRequest, "no_content", "not enough conversation yet to derive a plan")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), chatTimeout)
	defer cancel()
	changed, err := refreshPlan(ctx, c)
	if err != nil {
		httpx.WriteErr(w, http.StatusBadGateway, "provider", err.Error())
		return
	}
	if changed {
		notePlanUpdated(c)
		c.UpdatedAt = nowMs()
		if err := saveConv(c); err != nil {
			httpx.WriteErr(w, http.StatusInternalServerError, "chat_save", err.Error())
			return
		}
	}
	httpx.WriteJSON(w, http.StatusOK, c)
}
