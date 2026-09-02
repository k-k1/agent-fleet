package main

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

// replyMarkerRe は行頭の箇条書き/番号マーカーだけを剥がす。記号（- * ・ >）は空白の有無に
// かかわらず、番号は「1. / 1) のあと空白＋本文」の形のときだけ剥がす。こうすることで、選択肢の
// 識別子そのもの（"1" "A" "P1"）を答えとして出したときに丸ごと消してしまわない。
var replyMarkerRe = regexp.MustCompile(`^\s*(?:[-*・>]\s*|[0-9]+[.)]\s+)`)

// replyLabelRe は「候補: 進めて」のような行頭ラベルを剥がす。ラベルだけで本文が無い行
// （＝見出し「返信の候補：」）は剥がした結果が空になり、そのまま落ちる。選択肢の識別子を
// 潰さないよう、ラベル語は既知のものだけに限る（"A: 進めて" のような行は触らない）。
var replyLabelRe = regexp.MustCompile(`(?i)^\s*(?:候補|返信候補|返信|回答|答え|出力|suggestions?|candidates?|replies|answers?)\s*[:：]\s*`)

// 返信サジェスト v2（LLM 文脈生成）。直近の会話ログを一発ヘッドレス（oneShotHeadless・
// タイトル/ブランチ提案と同じ backend-agnostic 経路）に渡し、ユーザーが次に送りそうな短い
// 返信の候補を数件返す。On-demand（Console の✨ボタン）専用でトークンは押した時だけ消費する。
// フロントの頻度学習（Layer A）とは独立し、返ってきた候補をチップ列にマージして出す。

const (
	replySuggestTimeout  = 60 * time.Second
	replySuggestCount    = 3  // 返す候補の最大数
	replySuggestMaxRunes = 20 // 1 候補の長さ上限（超える行はプロンプト扱いで捨てる・ペルソナの20字と一致）
	// 窓は「ターン数」ではなく「文字予算」で決める。★転写の 1 ターン＝1 コンテンツブロックなので、
	// ツールを使う普通の回答は「2点をトリムします。」級の途中報告が何本も並ぶ（実測で最大 8 本）。
	// ターン数固定の窓（旧 2 ターン）だと、その途中報告だけで窓が埋まり、肝心の回答も依頼も一切
	// 渡らない（実測 11 転写中 7 件が assistant+assistant で、渡っていたのは 22 文字だけ）。
	// 予算窓なら「1」「進めて」のような短い返事はほぼコストゼロで通過し、その手前にある実質的な
	// 回答（提案・選択肢・問いかけ）まで自然に遡れる。
	replySuggestBudgetRunes = 1200 // 会話ログ全体の目安（最後の 1 発言だけは超過を許す）
	replySuggestMaxMsgs     = 6    // 遡る発言数の上限（畳んだ後の数）
	replySuggestPerMsgRunes = 700  // 1 発言で残す長さ
)

// replySuggestPersona は指示文の言語（＝ペルソナ/プロンプトを書く言語）を Console の表示言語で
// 選ぶ（docs/log/28 P6）。titleSuggestPersona(lang) と同じ形。
//
// ★候補そのものの言語は表示言語ではなく**会話の言語**（両言語の指示文がそう書いてある）。
// 候補はユーザーがそのままセッションへ送る文であり、日本語で作業中のセッションへ英語を送ると
// 以降の出力言語まで反転してしまう（chat_report.go の中断再開文と同じ理由）。分岐するのは
// 「モデルへの指示文が表示言語と割れないようにする」ためで、生成物の言語軸ではない。
func replySuggestPersona(lang string) string {
	if lang == "en" {
		return replySuggestPersonaEN
	}
	return replySuggestPersonaJA
}

// replySuggestPersonaJA: 会話の言語に合わせ、前置き・番号・引用符なしで 1 行 1 候補を出させる。
// 件名提案（第三者視点の名詞句）と違い、視点は「ユーザー本人が送る返信」であることを明示する。
// ★スタイル: ユーザーは開発者でエージェントに手短に指示する。丁寧語・敬語を付けると（"修正して"
// でよいところ "修正をお願いします" になり）そのまま無駄トークンとして送られるので、常体・命令形で
// 簡潔に。「です／ます／してください／お願いします」や「なるほど／では」等の前置きは禁止。
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

// replySuggestPersonaEN: 日本語版と同じ契約を英語で書いたもの。★「候補は会話ログの言語で」を
// 例より先に置く — 例が英語なので、順序を逆にすると日本語スレッドでも英語の候補を出しはじめる。
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

// replySuggestModel: 短い候補生成には安価/高速なモデルで十分。deployment 単位で上書き可。
func replySuggestModel() string { return envOr("AF_SUGGEST_MODEL", "haiku") }

// replySuggestEnabled: ui-prefs の replySuggest スイッチ（Console の✨ボタン表示 = 既定 ON）。
// キー欠落/不正は true（フロント DEFAULTS.replySuggestEnabled と一致）。
func replySuggestEnabled() bool {
	v, ok := uiprefs.Read()["replySuggest"].(bool)
	return !ok || v
}

// replyMsg は窓を組むための「1 発言」。転写のターン（＝1 行＝1 コンテンツブロック）でも
// チャットの chatMessage でもなく、畳んだ後の論理的な発言を表す。
type replyMsg struct {
	role string
	text string
}

// replyTailLines は返信サジェスト用に 1 発言を切り詰める。件名提案の writeConversationWindow が
// 先頭を残す（冒頭に主題がある）のに対し、こちらは末尾を残す — 返信の手がかり（問いかけ・
// 選択肢の識別子・「どうする?」の一文）は発言の終わりに集中しており、先頭を残す切り方だと
// 長い回答ほど肝心の部分が落ちて、候補が文脈と噛み合わなくなる。
// ★切るのは行（＝段落・箇条書き・見出しの境界）単位。文字数で機械的に切ると「1. L19：…」の
// ような選択肢行が頭から欠けて、識別子だけを答える指示が効かなくなる。空行は落として詰める。
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
			// 末尾の 1 行だけで予算を超えるときは、その行を字数で切る（何も残さないよりよい）。
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

// replyFoldWindow は発言列を「同一 role の連続を 1 発言に畳む → 新しい方から文字予算を
// 満たすまで遡る」で窓に切り出す。畳みが本体: これが無いと途中報告 1 本 1 本が 1 ターンとして
// 数えられ、窓が実質的な回答に届かない（定数のコメント参照）。
func replyFoldWindow(msgs []replyMsg) []replyMsg {
	folded := make([]replyMsg, 0, len(msgs))
	for _, m := range msgs {
		if n := len(folded); n > 0 && folded[n-1].role == m.role {
			folded[n-1].text += "\n" + m.text
			continue
		}
		folded = append(folded, m)
	}
	out := make([]replyMsg, 0, replySuggestMaxMsgs)
	used := 0
	for i := len(folded) - 1; i >= 0 && len(out) < replySuggestMaxMsgs; i-- {
		txt := replyTailLines(folded[i].text, replySuggestPerMsgRunes)
		out = append([]replyMsg{{folded[i].role, txt}}, out...)
		if used += len([]rune(txt)); used >= replySuggestBudgetRunes {
			break
		}
	}
	return out
}

// replySuggestWindow は窓の本文（"role: text" の並び）を書く。セッション版とチャット版で共通。
func replySuggestWindow(b *strings.Builder, msgs []replyMsg) {
	for _, m := range replyFoldWindow(msgs) {
		fmt.Fprintf(b, "%s: %s\n", m.role, m.text)
	}
}

// replySuggestPrompt は直近の実ターン（sidechain/compaction/tool-only を除く）を文脈に渡す。
// タイトルと違い開始ターンは不要 — 返信は「直前に何を言われたか」が全てなので末尾窓だけでよい。
func replySuggestPrompt(turns []transcript.Turn, lang string) string {
	real := make([]replyMsg, 0, len(turns))
	for _, t := range turns {
		if t.Sidechain || t.Compact || t.Text == "" {
			continue
		}
		real = append(real, replyMsg{t.Role, t.Text})
	}
	var b strings.Builder
	b.WriteString(replySuggestInstructions(lang, replyCounterpartSession))
	b.WriteString(replySuggestLogHeader(lang))
	replySuggestWindow(&b, real)
	return b.String()
}

// 返信先の呼び分け（セッション＝エージェント／チャット＝アシスタント）。指示文の他の部分は
// 共通なので、この 1 語だけを差し替えて両方から使う。
const (
	replyCounterpartSession = iota
	replyCounterpartChat
)

// replySuggestInstructions / replySuggestLogHeader: 会話ログ本文は原文のまま渡し、その前後の
// 枠だけを表示言語で書く（件名提案の titleSuggestInstructions と同じ分け方）。
func replySuggestInstructions(lang string, counterpart int) string {
	if lang == "en" {
		who := "agent"
		if counterpart == replyCounterpartChat {
			who = "assistant"
		}
		return "Continue the conversation log below: output at most 3 replies the user would send next, newline-separated.\n" +
			"Each must fit what the " + who + " just said. Terse and imperative, no polite padding.\n" +
			"If numbered/lettered choices were offered, output the identifier alone (1, 2, A, P1 ...).\n" +
			"Write them in the conversation log's own language.\n" +
			"Examples (approve / continue / answer / halt / choose): go ahead / OK / fix it / hold on / 1 / A\n\n"
	}
	who := "エージェント"
	if counterpart == replyCounterpartChat {
		who = "アシスタント"
	}
	return "会話ログの続きとして、ユーザーが次に送る返信の候補を最大3件、改行区切りで出力してください。\n" +
		"直前の" + who + "の発言に噛み合う短文にすること。丁寧語にせず、常体・命令形で簡潔に。\n" +
		"数字/英字で選択肢が提示されていればその識別子だけ（1・2・A・P1 等）。\n" +
		"候補は会話ログで使われている言語で書くこと。\n" +
		"例（すべて常体で簡潔に・承認/続行/回答/中断/選択）: 進めて / OK / 修正して / 待って / 1 / A\n\n"
}

func replySuggestLogHeader(lang string) string {
	if lang == "en" {
		return "--- conversation log ---\n"
	}
	return "--- 会話ログ ---\n"
}

// cleanSuggestedReplies は LLM の生出力を候補配列へ整形する。行分割し、箇条書き記号/番号/
// 引用符を剥がし、空行・見出し行・長すぎる行を落とし、重複（大小無視）を畳んで最大
// replySuggestCount 件。
func cleanSuggestedReplies(s string) []string {
	out := make([]string, 0, replySuggestCount)
	seen := map[string]bool{}
	for _, line := range strings.Split(s, "\n") {
		c := strings.TrimSpace(line)
		// 先頭の箇条書き/番号マーカー（"1. 進めて" "- OK" "・待って" 等）だけを剥がす。裸の
		// 選択肢識別子（"1" "A" "P1"）は答えそのものなので replyMarkerRe では消えない。
		c = replyMarkerRe.ReplaceAllString(c, "")
		// "候補: 進めて" のようなラベル付きは中身だけ残す（ラベルだけの行は次の見出し判定で落ちる）。
		c = replyLabelRe.ReplaceAllString(c, "")
		c = strings.Trim(c, "\"'「」『』`")
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		// 「ユーザーが次に送る返信の候補：」のような見出し/前置きを落とす。コロンで終わる返信は
		// 実在しない（禁止したつもりでもモデルは前置きを付けるので、出力側でも殺す）。
		if strings.HasSuffix(c, ":") || strings.HasSuffix(c, "：") {
			continue
		}
		if len([]rune(c)) > replySuggestMaxRunes {
			continue // プロンプト級の長文は「クイック返信」ではない
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
	lang := uiprefs.Locale() // 指示文の言語だけ（候補そのものは会話の言語 — replySuggestPersona 参照）
	reply, err := chatx.OneShotHeadless(ctx, replySuggestPersona(lang), replySuggestPrompt(turns, lang), replySuggestModel())
	if err != nil {
		return nil, fmt.Errorf("reply suggestion failed: %w", err)
	}
	return cleanSuggestedReplies(reply), nil
}

// handleSuggestReplies は preview 専用（Meta を一切触らない）。Console の✨ボタンが叩き、
// 返ってきた候補をコンポーサー上のチップ列にマージする。
func handleSuggestReplies(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	if !replySuggestEnabled() {
		httpx.WriteErr(w, http.StatusBadRequest, "feature_disabled", "reply suggestion is turned off")
		return
	}
	m, found := session.ReadMeta(name)
	if !found {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	turns := sessionTitleTurns(m) // タイトル提案と同じ転写ロード（kind 差を吸収）
	if len(turns) == 0 {
		httpx.WriteErr(w, http.StatusBadRequest, "no_content", "not enough conversation yet to suggest replies")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), replySuggestTimeout)
	defer cancel()
	ctx = usagex.WithTag(ctx, usagex.Tag{Feature: usagex.FeatureSuggestSession, Trigger: usagex.TriggerManual, Ref: name})
	reps, err := runReplySuggestLLM(ctx, turns)
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "generation_failed", "reply suggestion failed")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"suggestions": reps})
}
