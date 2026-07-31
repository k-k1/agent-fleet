package main

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
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
	// 直近何ターンを文脈に入れるか。返信の手がかりは「直前に何を言われたか」がほぼ全てなので、
	// 直前の回答＋その前のユーザー発話（何を頼んだか）の 2 ターンだけでよい。件名提案（会話全体の
	// 主題が要る）と違い、窓を広げても候補は良くならず入力トークンだけ増える。
	replySuggestTailTurns = 2
	// 1 ターンで残す長さ。★頭ではなく末尾を残す — 質問文・選択肢の識別子（1/2/A/B）は発言の
	// 末尾に集中するので、件名提案と同じ「先頭 N 文字」で切ると肝心の部分が落ちる。
	replySuggestTailRunes = 600
)

// replySuggestPersona: 会話の言語に合わせ、前置き・番号・引用符なしで 1 行 1 候補を出させる。
// 件名提案（第三者視点の名詞句）と違い、視点は「ユーザー本人が送る返信」であることを明示する。
// ★スタイル: ユーザーは開発者でエージェントに手短に指示する。丁寧語・敬語を付けると（"修正して"
// でよいところ "修正をお願いします" になり）そのまま無駄トークンとして送られるので、常体・命令形で
// 簡潔に。「です／ます／してください／お願いします」や「なるほど／では」等の前置きは禁止。
const replySuggestPersona = "あなたはチャットの会話ログを読み、ユーザーが次にエージェントへ送る短い返信の候補を作る専用ツールです。" +
	"直前のエージェントの発言（質問・確認・提案）に対して、ユーザーが実際に打ちそうな返信を考えます。" +
	"ユーザーは開発者で、エージェントに手短に指示します。文体は常体・命令形で簡潔に。" +
	"敬語・丁寧語（です／ます／してください／お願いします 等）や前置き（なるほど／では 等）は一切付けない。" +
	"例: 『修正をお願いします』ではなく『修正して』、『それで進めてください』ではなく『進めて』。" +
	"エージェントが選択肢を数字や英字（1・2・A・B・P1 等）で提示している場合は、言葉を足さずその識別子だけを候補にする" +
	"（例: 『1番でお願い』『1番で』ではなく『1』、『Aにして』ではなく『A』）。" +
	"承認・却下・続行の指示、質問への短い回答、次の依頼などを、会話と同じ言語で、1 候補 1 行・最大3件・各20文字以内で。" +
	"番号・箇条書き・引用符・説明は一切付けず、候補そのものだけを改行区切りで出力してください。" +
	"見出し・前置き（『返信の候補：』『以下の通りです』等）も禁止 — 1行目から候補そのものを書くこと。"

// replySuggestModel: 短い候補生成には安価/高速なモデルで十分。deployment 単位で上書き可。
func replySuggestModel() string { return envOr("AF_SUGGEST_MODEL", "haiku") }

// replySuggestEnabled: ui-prefs の replySuggest スイッチ（Console の✨ボタン表示 = 既定 ON）。
// キー欠落/不正は true（フロント DEFAULTS.replySuggestEnabled と一致）。
func replySuggestEnabled() bool {
	v, ok := readUIPrefs()["replySuggest"].(bool)
	return !ok || v
}

// replyTailText は返信サジェスト用に 1 発言を切り詰める。件名提案の writeConversationWindow が
// 先頭を残す（冒頭に主題がある）のに対し、こちらは末尾を残す — 返信の手がかり（問いかけ・
// 選択肢の識別子・「どうする?」の一文）は発言の終わりに集中しており、先頭を残す切り方だと
// 長い回答ほど肝心の部分が落ちて、候補が文脈と噛み合わなくなる。
func replyTailText(s string) string {
	t := strings.TrimSpace(s)
	r := []rune(t)
	if len(r) <= replySuggestTailRunes {
		return t
	}
	return "…" + string(r[len(r)-replySuggestTailRunes:])
}

// replySuggestPrompt は直近の実ターン（sidechain/compaction/tool-only を除く）を文脈に渡す。
// タイトルと違い開始ターンは不要 — 返信は「直前に何を言われたか」が全てなので末尾窓だけでよい。
func replySuggestPrompt(turns []transcript.Turn) string {
	real := make([]transcript.Turn, 0, len(turns))
	for _, t := range turns {
		if t.Sidechain || t.Compact || t.Text == "" {
			continue
		}
		real = append(real, t)
	}
	if len(real) > replySuggestTailTurns {
		real = real[len(real)-replySuggestTailTurns:]
	}
	var b strings.Builder
	b.WriteString("会話ログの続きとして、ユーザーが次に送る返信の候補を最大3件、改行区切りで出力してください。\n")
	b.WriteString("直前のエージェントの発言に噛み合う短文にすること。丁寧語にせず、常体・命令形で簡潔に。\n")
	b.WriteString("数字/英字で選択肢が提示されていればその識別子だけ（1・2・A・P1 等）。\n")
	b.WriteString("例（すべて常体で簡潔に・承認/続行/回答/中断/選択）: 進めて / OK / 修正して / 待って / 1 / A\n\n")
	b.WriteString("--- 会話ログ ---\n")
	for _, t := range real {
		fmt.Fprintf(&b, "%s: %s\n", t.Role, replyTailText(t.Text))
	}
	return b.String()
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
	reply, err := oneShotHeadless(ctx, replySuggestPersona, replySuggestPrompt(turns), replySuggestModel())
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
	ctx = withUsageTag(ctx, usageTag{Feature: usageFeatureSuggestSession, Trigger: usageTriggerManual, Ref: name})
	reps, err := runReplySuggestLLM(ctx, turns)
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "generation_failed", "reply suggestion failed")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"suggestions": reps})
}
