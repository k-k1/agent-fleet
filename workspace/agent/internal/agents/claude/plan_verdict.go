package claude

// ExitPlanMode の tool_result（＝プランの決着）をどう読むか。
//
// この文字列は claude の**非契約・無版管理**の出力で、実際に形が変わった:
// 2026-08-31 に「承認したのに 却下 バッジ」が起きた原因は、承認結果に承認された
// **計画本文が丸ごと**付くようになったこと（`## Approved Plan:` 以下）。Console は
// この文字列をキーワードで読んでバッジを出すので、計画本文が「却下」「やり直し」に
// 触れているだけで承認が却下に化けた。
//
// 対策は 3 層で、ここは真ん中（実行時カナリア）。
//   1. ロック: planDecision.test.ts / transcript_test.go が既知の形を固定する。
//      ドリフトそのものは検知できない（緑のまま形が変わる）。
//   2. **カナリア（このファイル）**: 実フリートで流れた結果が承認とも却下とも読めな
//      かったら 1 度だけ log に出す。CI が踏めない承認肢（"Yes, and manually approve
//      edits" 等）も含め、実際に使われた経路をそのまま見られるのはここだけ。
//   3. ライブ検知: claude_plan_contract_test.go（実 TUI で承認まで通す）と
//      claude_plan_drift_test.go（実 CLI バイナリに文言が在るか）。

import (
	"log"
	"regexp"
	"strings"
	"sync"
)

// planBodyMarker は承認結果に埋め込まれた計画本文の始まり。
var planBodyMarker = regexp.MustCompile(`(?im)^[ \t]*#{1,6}[ \t]*(approved plan|承認されたプラン)[ \t]*[:：]`)

// planAnswerHead drops the approved plan that claude appends to an ExitPlanMode
// tool_result. The current CLI writes:
//
//	User has approved your plan. You can now start coding. …
//	Your plan has been saved to: …/plans/immutable-dazzling-babbage.md
//	## Approved Plan:
//	<the entire plan Markdown>
//
// The verdict is the header; the body is a verbatim copy of a plan the Console already
// holds as the plan part. Keeping it would ship every approved plan twice on every poll
// (9 KB+ each, in a map that is re-sent whole), and the Console badges the card by
// keyword — reading the plan's own prose ("却下"/"やり直し"/"reject") flipped an APPROVED
// plan's badge to 却下 (2026-08-31). The Console cuts it too; this keeps the wire small.
func planAnswerHead(s string) string {
	if m := planBodyMarker.FindStringIndex(s); m != nil {
		return strings.TrimRight(s[:m[0]], " \t\r\n")
	}
	return s
}

// 判定語は Console の planDecision.ts（isApproved / isRejected）の写し。バッジを出すの
// は Console 側なので、ここは「Console と同じ読み方で読めるか」を確かめるためだけに
// 存在する。TestPlanVerdictKeywordsMatchConsole が両者の語彙を突き合わせて固定するので、
// 片方だけ直して食い違うことはない。
var (
	planApprovedRe = regexp.MustCompile(`(?i)approv|proceed|start coding|going to code|承認|実行してよい|yes`)
	planRejectedRe = regexp.MustCompile(`(?i)keep planning|not approv|reject|refine|declin|interrupt|却下|中止|やり直`)
)

// Plan verdicts. "unknown" = 承認とも却下とも読めない（Console のバッジは「決定済み」に
// 倒れる）。誤ったバッジよりは無害だが、**それが続くならドリフトが起きている**。
const (
	PlanApproved = "approved"
	PlanRejected = "rejected"
	PlanUnknown  = "unknown"
)

// PlanVerdict classifies an ExitPlanMode tool_result exactly as the Console's
// planOutcome() does for a card with no optimistic 却下 mark: the embedded plan body is
// cut off first, and a definitive approval wins over a reject keyword.
func PlanVerdict(result string) string {
	head := planAnswerHead(result)
	approved, rejected := planApprovedRe.MatchString(head), planRejectedRe.MatchString(head)
	switch {
	case approved && !rejected:
		return PlanApproved
	case rejected:
		return PlanRejected
	default:
		return PlanUnknown
	}
}

// planDriftWarned keeps the canary to one line per plan. Bounded by the number of plans
// a Workspace ever presents (one empty struct each), and the map dies with the process.
var planDriftWarned sync.Map

// notePlanVerdict is the runtime canary: a plan that resolved to text we cannot read as
// either verdict is the exact signature of the CLI changing its wording again. The
// Console degrades to a neutral 決定済み badge, which is safe but SILENT — so say it once,
// with the text, in the Agent log. qid makes it per-plan, not per-poll (this runs on
// every /messages poll for the whole transcript).
func notePlanVerdict(qid, result string) {
	head := planAnswerHead(result)
	if head == "" || PlanVerdict(head) != PlanUnknown {
		return
	}
	if _, dup := planDriftWarned.LoadOrStore(qid, struct{}{}); dup {
		return
	}
	log.Printf("claude plan drift: ExitPlanMode の結果を承認/却下のどちらとも判定できませんでした"+
		"（qid=%s）。CLI の文言が変わった可能性があります — planDecision.ts / plan_verdict.go を確認。結果: %q",
		qid, head)
}
