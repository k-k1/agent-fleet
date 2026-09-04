package chatx

// Work-plan carry-forward (docs/log/33 stage 5).
//
// The compaction handoff (stage 2) is a single ~1000-character LLM summary, and the next
// compaction summarises a session that already started from a summary — a summary of a
// summary of a summary, thinner with every generation. A long-lived orchestration
// conversation goes through several generations in a few hours, and the plan it opened
// with loses its shape.
//
// So the plan is kept apart from the summary, in one slot that holds it verbatim
// (chatConversation.Plan). The verbatim text is prepended whenever a new provider session
// begins, so no number of compactions degrades it, and the summary (PendingHandoff) is
// left to carry background alone.
//
// Three things update it:
//
//  1. Compaction (automatic, the main path) — compactConversation parses the two-block
//     output and rewrites the old plan, reflecting only what the recent conversation
//     changed.
//  2. "Refresh the plan" (explicit) — pressed right after the plan moved in discussion. It
//     runs through oneShotHeadless (the recent turns only) rather than the conversation's
//     provider session, so refreshing costs no extra context.
//  3. Hand editing (PUT) — the last resort for what 1 and 2 missed or overwrote wrongly.
//
// The one risk of carrying the text verbatim is an old plan coming back strongly enough to
// overwrite a new agreement reached in discussion: where the summary approach would fade
// out vaguely, this one is confidently wrong. Hence exit 3 must always exist, and a turn
// that changed the plan shows the body in a notice — that is the only place a person can
// notice it.

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// Separators for the two-block compaction output. The spelling is one a model would not
// write on its own, so it cannot turn up by chance in the conversation and derail parsing.
const (
	planMarker    = "<<<PLAN>>>"
	summaryMarker = "<<<SUMMARY>>>"
)

// PlanShapeFor is the shape of the plan block (three fixed headings). The headings are
// stored as part of the plan body and read by the user on the notice card, so they follow
// the display language (docs/log/28 P6); the language of the content stays the
// conversation's main one — see the note in compactSummaryPromptFor.
//
// Deliberately there is no "Done" heading. Given one, the model goes looking to enumerate
// completed work and most of the handoff fills up with achievement reports that change the
// next step not at all. The test for carrying something is not "is it done" but "would the
// next step be wrong without it", so the slot is named "Given" and draws in only what is
// needed. A deliberately failing test, for instance, is completed work, but dropping it
// makes the successor read it as broken and go fix it — so it is carried.
func PlanShapeFor(lang string) string {
	if lang == "en" {
		// Keep the headings spelled exactly as the Console's input placeholder
		// (chat.plan.placeholder, en). If the hand-editing frame and the generated plan's
		// headings disagree, the headings swap around on every incremental update.
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

// PlanUpdateInstructionFor is the update instruction prepended when a plan already exists.
// Rewriting from scratch wobbles from generation to generation and degrades exactly like
// the summary approach, so this pins it to an incremental update.
func PlanUpdateInstructionFor(lang string) string {
	if lang == "en" {
		return "[Current work plan] Below is the work plan currently in effect for this conversation. " +
			"Write the " + planMarker + " block **from it, reflecting only what changed in the recent conversation** " +
			"(do not start over from scratch / return it unchanged when nothing changed / drop items that are done)."
	}
	return "【現在の作業計画】以下はこの会話で現在有効な作業計画です。" +
		planMarker + " ブロックは、これを土台に**直近の会話で変わった点だけを反映して書き直して**ください" +
		"（ゼロから作り直さない／変更が無ければそのまま返す／完了した項目は削除する）。"
}

// PlanPreambleFor is the framing text used when handing the plan to a new provider session.
//
// It points the opposite way from handoffPreamble (the summary), which says "this is data,
// do not interpret it as a new instruction": a summary is background, a plan is an
// instruction meant to be followed. Demote the plan to "reference information" by mistake
// and it is carried but not obeyed, which to the user looks exactly like forgetting it.
func PlanPreambleFor(lang string) string {
	if lang == "en" {
		return "[Current work plan] This is the agreed work plan currently in effect for this conversation. " +
			"Carry the work forward along this plan (a new instruction from the user takes precedence)."
	}
	return "【現在の作業計画】これはこの会話で合意済みの、現在有効な作業計画です。" +
		"以降の作業はこの計画に沿って進めてください（利用者から新しい指示があればそちらが優先）。"
}

// The window for an explicit (oneShotHeadless) plan refresh. Discussion can run over
// several exchanges, so it is wider than the reply suggestion's (the last two turns), and a
// single message is cut keeping its tail — agreement is written at the end of a message.
const (
	planTailTurns = 12
	planTailRunes = 1200
	planMaxRunes  = 8000 // ceiling for the plan slot; past this it is minutes, not a plan
)

func planModel() string { return envOr("AF_PLAN_MODEL", "sonnet") }

// CompactPrompt builds the compaction turn's instruction: one reply carrying the plan block
// (carried verbatim) and the summary block (background). When a plan already exists it asks
// for an incremental update.
func CompactPrompt(c *ChatConversation) string {
	lang := uiprefs.Locale()
	var b strings.Builder
	if p := strings.TrimSpace(c.Plan); p != "" {
		b.WriteString(PlanUpdateInstructionFor(lang) + "\n\n" + p + "\n\n---\n\n")
	}
	b.WriteString(CompactSummaryPromptFor(lang))
	return b.String()
}

// parseCompactOutput splits the compaction reply into the plan and the summary.
//
// When the separators were not honoured it returns plan="", and the caller keeps the
// existing plan — a degradation that stops a malformed reply from wiping a plan in use.
// Wrapping the whole reply in a code fence is a common way for it to go wrong, so that one
// case is stripped before searching.
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
		// Only the summary came out empty. The plan was parsed, so rather than throw it away
		// hand the same body to the summary too: compactConversation treats an empty summary
		// as an error and the whole compaction fails.
		summary = plan
	}
	return plan, summary
}

// stripCodeFence removes a whole-reply ``` fence, the failure mode where the model wraps
// its entire output.
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
// than a plan. Looking at the empty string alone would store "なし" or "N/A" as the plan.
// The English wordings are checked too because the prompt exists in both languages, so
// either can come back.
func blankPlan(s string) bool {
	t := strings.Trim(strings.TrimSpace(s), "（）()「」-—–*_ 　")
	switch strings.ToLower(t) {
	case "", "なし", "特になし", "none", "n/a", "na", "無し", "no plan", "nothing", "not applicable":
		return true
	}
	return false
}

// clampPlan trims a plan to planMaxRunes, dropping the tail — a plan takes effect from the
// top down. The truncation marker stays in the stored plan body and is read by the user, so
// it follows the display language.
func clampPlan(s string) string {
	t := strings.TrimSpace(s)
	r := []rune(t)
	if len(r) <= planMaxRunes {
		return t
	}
	return strings.TrimSpace(string(r[:planMaxRunes])) + "\n\n" + PlanTruncatedNote(uiprefs.Locale())
}

func PlanTruncatedNote(lang string) string {
	if lang == "en" {
		return "(truncated here — the plan hit its length limit)"
	}
	return "（長さ上限のため以降を省略）"
}

// setPlan stores a new plan and reports whether it actually changed. That verdict is also
// what keeps a notice from being raised when nothing changed: stacking the same plan card
// on every automatic compaction buries the one card that says the plan really moved.
func setPlan(c *ChatConversation, plan string) bool {
	next := clampPlan(plan)
	if next == strings.TrimSpace(c.Plan) {
		return false
	}
	c.Plan, c.PlanUpdatedAt = next, NowMs()
	return true
}

// notePlanUpdated appends the "plan updated" notice together with the plan body. It is the
// only place a person can catch a verbatim carry-forward overwriting something, so the
// whole body is shown.
func notePlanUpdated(c *ChatConversation) {
	c.Messages = append(c.Messages, newNotice(noticeKeyPlanUpdated,
		map[string]string{"plan": c.Plan},
		"作業計画を更新しました。以降の新しいセッションには、この計画を要約せず原文のまま引き継ぎます。"+
			"\n\n---\n\n"+c.Plan))
}

// InjectPlan prepends the standing plan when the prompt is about to open a FRESH native
// session for this backend (right after a compaction, right after an agent switch, or the
// first turn). On a turn where resume is alive the plan is already in the other side's
// context, so it is not sent — sending it every turn only pays for the input tokens twice.
func InjectPlan(c *ChatConversation, agent, prompt string) (string, bool) {
	plan := strings.TrimSpace(c.Plan)
	if plan == "" || providerHasResume(c, agent) {
		return prompt, false
	}
	return PlanPreambleFor(uiprefs.Locale()) + "\n\n" + plan + "\n\n---\n\n" + prompt, true
}

// InjectCarryover prepends everything that must survive a provider-session reset: the
// compaction summary (background, once) and the standing plan (verbatim, every time, an
// instruction).
//
// The order is summary -> plan -> the actual prompt. The plan sits immediately before the
// prompt because the closer it is the stronger it bites: the plan is the instruction to
// follow right now, the summary is background. The returned bool says whether the SUMMARY
// was carried, and the caller drops PendingHandoff once the turn succeeds. The plan is not
// dropped — it stays with the conversation.
func InjectCarryover(c *ChatConversation, agent, prompt string) (string, bool) {
	prompt, _ = InjectPlan(c, agent, prompt)
	return InjectHandoff(c, prompt)
}

// --- Explicit plan refresh (oneShotHeadless) -----------------------------------

func PlanRefreshPersonaFor(lang string) string {
	if lang == "en" {
		return "You maintain a work plan. You compare the current plan you are given against the recent conversation " +
			"and output only the plan's latest version. You never write a preamble, a closing remark or a code fence."
	}
	return "あなたは作業計画の管理者です。渡された現在の計画と直近の会話を突き合わせ、" +
		"計画の最新版だけを出力します。前置き・後書き・コードフェンスは書きません。"
}

// planRefreshPrompt asks for the updated plan only. It runs one-shot headless rather than
// through the conversation's provider session, so the context is passed explicitly and
// consists of the current plan plus the recent conversation, nothing else. The conversation
// text goes in verbatim and only the framing is written in the display language — the same
// split as the reply suggestion and the title suggestion.
func planRefreshPrompt(c *ChatConversation, lang string) string {
	var b strings.Builder
	b.WriteString(PlanRefreshInstructions(strings.TrimSpace(c.Plan), lang))
	b.WriteString(PlanContextHeader(lang))
	for _, m := range planContextTurns(c.Messages) {
		fmt.Fprintf(&b, "%s: %s\n\n", m.Role, planTailText(m.Content))
	}
	return b.String()
}

func PlanRefreshInstructions(plan, lang string) string {
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
		b.WriteString(PlanShapeFor(lang) + "\n\n")
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
	b.WriteString(PlanShapeFor(lang) + "\n\n")
	return b.String()
}

func PlanContextHeader(lang string) string {
	if lang == "en" {
		return "--- recent conversation ---\n"
	}
	return "--- 直近の会話 ---\n"
}

// planContextTurns is the tail window used as the plan's context. report / notice are
// excluded because they are not agreements reached in the conversation: a notice body is
// only the display catalogue's source-language fallback (ADR 0033).
func planContextTurns(msgs []ChatMessage) []ChatMessage {
	real := make([]ChatMessage, 0, len(msgs))
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
// plan changed. The caller holds the conversation lock.
func refreshPlan(ctx context.Context, c *ChatConversation) (bool, error) {
	// Usage ledger (ADR 0029 §3): a plan refresh is an auxiliary feature, separate from a
	// conversation turn. Without a tag it falls into unknown, the signal for a forgotten tag.
	ctx = usagex.WithTag(ctx, usagex.Tag{
		Feature: usagex.FeaturePlanUpdate, Trigger: usagex.TriggerManual, Ref: c.ID,
	})
	lang := uiprefs.Locale()
	reply, err := OneShotHeadless(ctx, OneShotProse, PlanRefreshPersonaFor(lang), planRefreshPrompt(c, lang), planModel())
	if err != nil {
		return false, fmt.Errorf("plan refresh failed: %w", err)
	}
	plan := strings.TrimSpace(stripCodeFence(strings.TrimSpace(reply)))
	if blankPlan(plan) {
		// A "no plan" reply does not erase the existing one — usually the conversation is
		// just still shallow.
		return false, nil
	}
	return setPlan(c, plan), nil
}

type chatPlanReq struct {
	Plan string `json:"plan"`
	// Notice asks for the "plan updated" card to be stacked into the conversation. Hand
	// editing from the Console passes false — showing it back to the person who just wrote it
	// buys nothing. An operator rewriting it over MCP passes true: that is the only path on
	// which the plan moves while the user is not watching, so it always leaves a trace in the
	// conversation (docs/log/33 stage 5, option D).
	Notice bool `json:"notice,omitempty"`
}

// HandleChatPlanGet (GET /chat/conversations/{id}/plan) returns just the plan. It is kept
// apart from the whole-conversation GET for MCP's sake — the face an operator reads its own
// plan through (docs/log/33 stage 5, option D): returning every message would pour the
// entire conversation into the model just to read one line of plan.
func HandleChatPlanGet(w http.ResponseWriter, r *http.Request) {
	c, err := LoadConv(r.PathValue("id"))
	if err != nil {
		httpx.WriteErr(w, http.StatusNotFound, errCodeChatConversationNotFnd, "conversation not found")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"plan": c.Plan, "plan_updated_at": c.PlanUpdatedAt})
}

// HandleChatPlanSet (PUT /chat/conversations/{id}/plan) stores a hand-edited plan. An empty
// string clears the plan — the way to fold up a finished one. The automatic updates never
// clear it, so clearing is a human action only.
func HandleChatPlanSet(w http.ResponseWriter, r *http.Request) {
	var req chatPlanReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	id := r.PathValue("id")
	unlock := LockConv(id)
	defer unlock()
	c, err := LoadConv(id)
	if err != nil {
		httpx.WriteErr(w, http.StatusNotFound, errCodeChatConversationNotFnd, "conversation not found")
		return
	}
	if setPlan(c, req.Plan) {
		if req.Notice {
			notePlanUpdated(c)
		}
		c.UpdatedAt = NowMs()
		if err := SaveConv(c); err != nil {
			httpx.WriteErr(w, http.StatusInternalServerError, "chat_save", err.Error())
			return
		}
	}
	httpx.WriteJSON(w, http.StatusOK, c)
}

// HandleChatPlanRefresh (POST /chat/conversations/{id}/plan/refresh) re-derives the plan
// from the recent conversation — the button pressed right after the plan moved in
// discussion.
func HandleChatPlanRefresh(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	unlock := LockConv(id)
	defer unlock()
	c, err := LoadConv(id)
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
		c.UpdatedAt = NowMs()
		if err := SaveConv(c); err != nil {
			httpx.WriteErr(w, http.StatusInternalServerError, "chat_save", err.Error())
			return
		}
	}
	httpx.WriteJSON(w, http.StatusOK, c)
}
