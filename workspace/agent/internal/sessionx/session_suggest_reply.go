package sessionx

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// replyMarkerRe strips only a leading bullet/number marker. Symbols (- * ・ >) are stripped
// with or without a following space; a number only in the "1. / 1) then space then text"
// shape. That way a bare choice identifier ("1", "A", "P1") answered as the candidate is
// not erased wholesale.
var replyMarkerRe = regexp.MustCompile(`^\s*(?:[-*・>]\s*|[0-9]+[.)]\s+)`)

// replyLabelRe strips a leading label such as "candidate: go ahead". A line that is a label
// with no body (i.e. the heading "reply candidates:") becomes empty and is dropped later.
// The label vocabulary is limited to known words so a choice identifier survives (a line
// like "A: go ahead" is left alone).
var replyLabelRe = regexp.MustCompile(`(?i)^\s*(?:候補|返信候補|返信|回答|答え|出力|suggestions?|candidates?|replies|answers?)\s*[:：]\s*`)

// Reply suggestion v2 (LLM context generation). The recent conversation log goes through the
// one-shot headless route (oneShotHeadless, the same backend-agnostic path as title/branch
// suggestion) and comes back as a few short replies the user is likely to send next.
// On-demand only (the Console's sparkle button), so tokens are spent only when it is pressed.
// It is independent of the front end's frequency learning (layer A); the returned candidates
// are merged into the chip row.

const (
	ReplySuggestTimeout  = 60 * time.Second
	replySuggestCount    = 3  // maximum number of candidates returned
	replySuggestMaxRunes = 20 // per-candidate cap; longer lines are dropped as prose (persona says 20)
	// The window is a CHARACTER budget, not a turn count. One transcript turn is one content
	// block, so an ordinary tool-using answer is a run of interim notes like "trimming two
	// points." (measured: up to 8 of them). A fixed turn window (previously 2 turns) fills up
	// with those interim notes alone and passes neither the real answer nor the request
	// (measured: 7 of 11 transcripts were assistant+assistant, carrying only 22 characters).
	// With a budget window a short reply like "1" or "go ahead" costs almost nothing, so the
	// window reaches back to the substantive answer (proposal, choices, question) before it.
	replySuggestBudgetRunes = 1200 // rough total for the conversation log (the last message alone may exceed it)
	replySuggestMaxMsgs     = 6    // maximum messages to walk back (counted after folding)
	replySuggestPerMsgRunes = 700  // length kept per message
)

// ReplySuggestPersona picks the language of the INSTRUCTIONS (the language the persona and
// prompt are written in) from the Console's display language (docs/log/28 P6). Same shape as
// TitleSuggestPersona(lang).
//
// The language of the candidates themselves is not the display language but the language of
// the CONVERSATION (both instruction texts say so). A candidate is a message the user sends
// into the session as-is, and sending English into a session working in Japanese flips the
// output language from then on (the same reason as the resume-after-interrupt text in
// chat_report.go). The branch exists so the instructions to the model do not diverge from
// the display language; it is not an axis over the generated text.
func ReplySuggestPersona(lang string) string {
	if lang == "en" {
		return replySuggestPersonaEN
	}
	return replySuggestPersonaJA
}

// replySuggestPersonaJA asks for one candidate per line, in the conversation's language, with
// no preamble, numbering or quotes. Unlike title suggestion (a third-person noun phrase), the
// viewpoint is stated explicitly: this is a reply the USER sends.
// Style: the user is a developer who instructs the agent tersely, so plain imperative forms
// only. Polite padding would be sent into the session verbatim as wasted tokens, which is why
// the persona bans honorifics and lead-ins outright.
const replySuggestPersonaJA = "あなたはチャットの会話ログを読み、ユーザーが次にエージェントへ送る短い返信の候補を作る専用ツールです。" +
	"直前のエージェントの発言（質問・確認・提案）に対して、ユーザーが実際に打ちそうな返信を考えます。" +
	"ユーザーは開発者で、エージェントに手短に指示します。文体は常体・命令形で簡潔に。" +
	"敬語・丁寧語（です／ます／してください／お願いします 等）や前置き（なるほど／では 等）は一切付けない。" +
	"例: 『修正をお願いします』ではなく『修正して』、『それで進めてください』ではなく『進めて』。" +
	"エージェントが選択肢を数字や英字（1・2・A・B・P1 等）で提示している場合は、言葉を足さずその識別子だけを候補にする" +
	"（例: 『1番でお願い』『1番で』ではなく『1』、『Aにして』ではなく『A』）。" +
	"承認・却下・続行の指示、質問への短い回答、次の依頼などを、会話と同じ言語で、1 候補 1 行・最大3件・各20文字以内で。" +
	"番号・箇条書き・引用符・説明は一切付けず、候補そのものだけを改行区切りで出力してください。" +
	"見出し・前置き（『返信の候補：』『以下の通りです』等）も禁止 — 1行目から候補そのものを書くこと。"

// replySuggestPersonaEN is the same contract written in English. "Write every candidate in
// the conversation log's language" comes BEFORE the examples: the examples are English, so
// the other order makes it emit English candidates even in a Japanese thread.
const replySuggestPersonaEN = "You read a chat conversation log and write short replies the USER might send next to the agent. " +
	"Respond to what the agent just said (a question, a confirmation, a proposal) with what this user would realistically type. " +
	"Write every candidate in the SAME LANGUAGE as the conversation log — the examples below are English, but a Japanese log gets Japanese candidates. " +
	"The user is a developer who instructs the agent tersely: imperative and short, no polite padding " +
	"(no 'Could you please …', no 'Sure, let's …' lead-ins). " +
	"Example: 'fix it', not 'Could you please fix it'; 'go ahead', not 'Please proceed with that'. " +
	"When the agent offers numbered or lettered choices (1, 2, A, B, P1 ...), make the identifier ALONE the candidate " +
	"(just '1', not 'let's go with 1'; just 'A', not 'pick A'). " +
	"Approvals, rejections, go-aheads, short answers to a question, the next request — one candidate per line, at most 3, at most 20 characters each. " +
	"No numbering, no bullets, no quotes, no explanation — output the candidates themselves, newline-separated. " +
	"No heading or preamble ('Here are some replies:' …) — the first line is already a candidate."

// ReplySuggestModel: a cheap/fast model is enough for short candidates. Overridable per
// deployment.
func ReplySuggestModel() string { return envOr("AF_SUGGEST_MODEL", "haiku") }

// ReplySuggestEnabled reads the ui-prefs replySuggest switch (shows the Console's sparkle
// button; default ON). A missing or malformed key reads as true, matching the front end's
// DEFAULTS.ReplySuggestEnabled.
func ReplySuggestEnabled() bool {
	v, ok := uiprefs.Read()["replySuggest"].(bool)
	return !ok || v
}

// ReplyMsg is one message as the window sees it: neither a transcript turn (one line = one
// content block) nor a chat chatMessage, but the logical message left after folding.
type ReplyMsg struct {
	Role string
	Text string
}

// replyTailLines truncates one message for reply suggestion. Title suggestion's
// writeConversationWindow keeps the HEAD (the subject is stated up front); this keeps the
// TAIL, because the cues for a reply (the question, the choice identifiers, the "so what do
// you want?" sentence) cluster at the end of a message. Keeping the head drops exactly that
// part on long answers, and the candidates stop fitting the context.
// The cut is by LINE (paragraph, bullet and heading boundaries). Cutting mechanically by
// character count clips a choice line such as "1. L19: ..." at its front, and the
// instruction to answer with the identifier alone stops working. Blank lines are dropped.
func replyTailLines(s string, max int) string {
	t := strings.TrimSpace(s)
	if len([]rune(t)) <= max {
		return t
	}
	lines := strings.Split(t, "\n")
	keep := make([]string, 0, len(lines))
	n := 0
	for i := len(lines) - 1; i >= 0; i-- {
		ln := strings.TrimSpace(lines[i])
		if ln == "" {
			continue
		}
		r := []rune(ln)
		if n+len(r) > max {
			// When the last line alone blows the budget, cut that line by characters:
			// better than keeping nothing.
			if len(keep) == 0 {
				keep = append(keep, "…"+string(r[len(r)-max:]))
			}
			break
		}
		keep = append([]string{ln}, keep...)
		n += len(r)
	}
	return "…\n" + strings.Join(keep, "\n")
}

// replyFoldWindow cuts the window out of a message list: fold consecutive messages of the
// same role into one, then walk back from the newest until the character budget is met.
// The folding is the point — without it each interim note counts as a turn of its own and
// the window never reaches the substantive answer (see the comment on the constants).
func replyFoldWindow(msgs []ReplyMsg) []ReplyMsg {
	folded := make([]ReplyMsg, 0, len(msgs))
	for _, m := range msgs {
		if n := len(folded); n > 0 && folded[n-1].Role == m.Role {
			folded[n-1].Text += "\n" + m.Text
			continue
		}
		folded = append(folded, m)
	}
	out := make([]ReplyMsg, 0, replySuggestMaxMsgs)
	used := 0
	for i := len(folded) - 1; i >= 0 && len(out) < replySuggestMaxMsgs; i-- {
		txt := replyTailLines(folded[i].Text, replySuggestPerMsgRunes)
		out = append([]ReplyMsg{{folded[i].Role, txt}}, out...)
		if used += len([]rune(txt)); used >= replySuggestBudgetRunes {
			break
		}
	}
	return out
}

// ReplySuggestWindow writes the window body (a run of "role: text"). Shared by the session
// and chat variants.
func ReplySuggestWindow(b *strings.Builder, msgs []ReplyMsg) {
	for _, m := range replyFoldWindow(msgs) {
		fmt.Fprintf(b, "%s: %s\n", m.Role, m.Text)
	}
}

// ReplySuggestPrompt passes the most recent real turns (sidechain, compaction and tool-only
// turns excluded) as context. Unlike titles it needs no opening turn: a reply depends only on
// what was just said, so the tail window is enough.
func ReplySuggestPrompt(turns []transcript.Turn, lang string) string {
	real := make([]ReplyMsg, 0, len(turns))
	for _, t := range turns {
		if t.Sidechain || t.Compact || t.Text == "" {
			continue
		}
		real = append(real, ReplyMsg{t.Role, t.Text})
	}
	var b strings.Builder
	b.WriteString(ReplySuggestInstructions(lang, ReplyCounterpartSession))
	b.WriteString(ReplySuggestLogHeader(lang))
	ReplySuggestWindow(&b, real)
	return b.String()
}

// Who the reply is addressed to (session = the agent, chat = the assistant). The rest of the
// instructions is shared, so both callers swap only this one word.
const (
	ReplyCounterpartSession = iota
	ReplyCounterpartChat
)

// ReplySuggestInstructions / ReplySuggestLogHeader: the conversation log itself is passed
// verbatim and only the frame around it is written in the display language (the same split as
// title suggestion's TitleSuggestInstructions).
func ReplySuggestInstructions(lang string, counterpart int) string {
	if lang == "en" {
		who := "agent"
		if counterpart == ReplyCounterpartChat {
			who = "assistant"
		}
		return "Continue the conversation log below: output at most 3 replies the user would send next, newline-separated.\n" +
			"Each must fit what the " + who + " just said. Terse and imperative, no polite padding.\n" +
			"If numbered/lettered choices were offered, output the identifier alone (1, 2, A, P1 ...).\n" +
			"Write them in the conversation log's own language.\n" +
			"Examples (approve / continue / answer / halt / choose): go ahead / OK / fix it / hold on / 1 / A\n\n"
	}
	who := "エージェント"
	if counterpart == ReplyCounterpartChat {
		who = "アシスタント"
	}
	return "会話ログの続きとして、ユーザーが次に送る返信の候補を最大3件、改行区切りで出力してください。\n" +
		"直前の" + who + "の発言に噛み合う短文にすること。丁寧語にせず、常体・命令形で簡潔に。\n" +
		"数字/英字で選択肢が提示されていればその識別子だけ（1・2・A・P1 等）。\n" +
		"候補は会話ログで使われている言語で書くこと。\n" +
		"例（すべて常体で簡潔に・承認/続行/回答/中断/選択）: 進めて / OK / 修正して / 待って / 1 / A\n\n"
}

func ReplySuggestLogHeader(lang string) string {
	if lang == "en" {
		return "--- conversation log ---\n"
	}
	return "--- 会話ログ ---\n"
}

// CleanSuggestedReplies shapes the LLM's raw output into a candidate list: split into lines,
// strip bullets, numbering and quotes, drop empty, heading and over-long lines, fold
// case-insensitive duplicates, and keep at most replySuggestCount.
func CleanSuggestedReplies(s string) []string {
	out := make([]string, 0, replySuggestCount)
	seen := map[string]bool{}
	for _, line := range strings.Split(s, "\n") {
		c := strings.TrimSpace(line)
		// Strip only a leading bullet/number marker ("1. go ahead", "- OK", "・wait").
		// A bare choice identifier ("1", "A", "P1") is the answer itself, so
		// replyMarkerRe does not erase it.
		c = replyMarkerRe.ReplaceAllString(c, "")
		// A labelled line ("candidate: go ahead") keeps only the body; a label-only line
		// is dropped by the heading test below.
		c = replyLabelRe.ReplaceAllString(c, "")
		c = strings.Trim(c, "\"'「」『』`")
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		// Drop a heading or preamble such as "replies the user might send next:". No real
		// reply ends in a colon, and the model adds a preamble however firmly the persona
		// forbids it, so it is killed on the output side too.
		if strings.HasSuffix(c, ":") || strings.HasSuffix(c, "：") {
			continue
		}
		if len([]rune(c)) > replySuggestMaxRunes {
			continue // prose that long is not a "quick reply"
		}
		k := strings.ToLower(c)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, c)
		if len(out) >= replySuggestCount {
			break
		}
	}
	return out
}

func runReplySuggestLLM(ctx context.Context, turns []transcript.Turn) ([]string, error) {
	lang := uiprefs.Locale() // instruction language only (candidates follow the conversation; see ReplySuggestPersona)
	reply, err := chatx.OneShotHeadless(ctx, chatx.OneShotShort, ReplySuggestPersona(lang), ReplySuggestPrompt(turns, lang), ReplySuggestModel())
	if err != nil {
		return nil, fmt.Errorf("reply suggestion failed: %w", err)
	}
	return CleanSuggestedReplies(reply), nil
}

// HandleSuggestReplies is preview-only: it never touches Meta. The Console's sparkle button
// calls it and merges the returned candidates into the chip row above the composer.
func HandleSuggestReplies(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	if !ReplySuggestEnabled() {
		httpx.WriteErr(w, http.StatusBadRequest, "feature_disabled", "reply suggestion is turned off")
		return
	}
	m, found := session.ReadMeta(name)
	if !found {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	turns := sessionTitleTurns(m) // same transcript load as title suggestion (absorbs kind differences)
	if len(turns) == 0 {
		httpx.WriteErr(w, http.StatusBadRequest, "no_content", "not enough conversation yet to suggest replies")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), ReplySuggestTimeout)
	defer cancel()
	ctx = usagex.WithTag(ctx, usagex.Tag{Feature: usagex.FeatureSuggestSession, Trigger: usagex.TriggerManual, Ref: name})
	reps, err := runReplySuggestLLM(ctx, turns)
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "generation_failed", "reply suggestion failed")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"suggestions": reps})
}
