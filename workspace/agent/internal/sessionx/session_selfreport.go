package sessionx

// 自己申告ファストパス（docs/log/51 §自己申告ファストパス / Phase 3）の**投入側**。
//
// リコンサイラは「機械的 idle」を証拠に完了を推定する。どれだけ証拠を足しても推定は
// 推定で、意味的完了（＝指示をやり切った）を直接測れるのはセッション自身だけなので、
// 報告義務を負う指示にだけ「終わったら af_report を1回呼べ」を1行足す。
//
// **backbone ではない**（ADR 0035 決定5）。呼び忘れても早呼びでも全体の正しさは変わらず、
// リコンサイラが settle で拾う — 申告が効くのは「2 tick の裏取りを 1 tick に縮める」ぶん
// だけ。だからこの1行は失敗しても構わないし、ツールが配られていない kind には足さない。
//
// 申告に渡すのはセッション名だけで、報告本文はサーバが生成する（fact-only）。本文を
// モデルに書かせると、報告経由のプロンプトインジェクション面をわざわざ増やすことになる。

import (
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// selfReportToolAvailable reports whether the session's CLI actually gets the af MCP
// server (mcpreg の builtin「af」— materialize 対象の kind すべて)。shell / ssm には
// 書き出す設定ファイルが無く、ツールも存在しないので、存在しないツールの呼出しを指示
// しないためにここで切る。
func selfReportToolAvailable(kind string) bool {
	k := NormalizeKind(kind)
	for _, m := range mcpreg.MaterializedKinds {
		if m == k {
			return true
		}
	}
	return false
}

// selfReportHintLine is the sentence appended to an instruction prompt. 2言語で書くのは
// docs/log/30 と同じ理由 — セッションが英語で作業しているところへ日本語だけを流し込むと、
// 以後の出力言語がそれに引きずられる（セッションごとの言語を読む術は無い）。「出力言語を
// 変えるな」を明示してあるのはその保険。
func selfReportHintLine(name string) string {
	return "[agent-fleet] この指示をやり切って作業が残っていない時点で、MCP ツール af_report(session=\"" + name +
		"\") を1回だけ呼んでください（質問や承認で止まる場合・作業が続く場合は呼ばないこと）。" +
		"この注記自体への返答は不要で、回答の言語も変えないでください。 / " +
		"When this instruction is fully done, call the MCP tool af_report(session=\"" + name +
		"\") exactly once. Do not call it if you are stopping to ask, or if work remains. " +
		"No reply to this note is needed; keep your output language unchanged."
}

// withSelfReportHint appends the fast-path line to an instruction prompt for the
// sessions that can act on it. 呼ぶのは report_to を運ぶ投入経路だけ（＝報告義務のある
// 指示）。利用者が Console で打った入力には足さない — 報告先が無い。
func withSelfReportHint(prompt string, m session.Meta) string {
	if strings.TrimSpace(prompt) == "" || !selfReportToolAvailable(m.Kind) {
		return prompt
	}
	return prompt + "\n\n" + selfReportHintLine(m.Name)
}
