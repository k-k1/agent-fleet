package claude

// How to read an ExitPlanMode tool_result, i.e. how a plan was resolved.
//
// That string is claude output with no contract and no versioning, and its shape has in fact
// changed: an "approved yet badged rejected" incident came from the approval result gaining
// the entire approved plan body (everything under `## Approved Plan:`). The Console reads the
// string by keyword to pick the badge, so a plan whose own prose merely mentioned rejecting
// or redoing turned an approval into a rejection.
//
// The countermeasure has three layers, and this is the middle one (the runtime canary).
//   1. Lock: planDecision.test.ts / transcript_test.go pin the known shape. They cannot
//      detect the drift itself — the shape changes while they stay green.
//   2. Canary (this file): log once when a result seen in the real fleet reads as neither
//      approved nor rejected. This is the only place the actually-used path can be observed
//      as-is, including approval options CI cannot exercise ("Yes, and manually approve
//      edits" and the like).
//   3. Live detection: claude_plan_contract_test.go (drives a real TUI through approval)
//      and claude_plan_drift_test.go (checks the wording exists in the real CLI binary).

import (
	"log"
	"regexp"
	"strings"
	"sync"
)

// planBodyMarker is where the plan body embedded in an approval result starts.
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
// plan's badge to rejected. The Console cuts it too; this keeps the wire small.
func planAnswerHead(s string) string {
	if m := planBodyMarker.FindStringIndex(s); m != nil {
		return strings.TrimRight(s[:m[0]], " \t\r\n")
	}
	return s
}

// The verdict words are a copy of the Console's planDecision.ts (isApproved / isRejected).
// The Console is what shows the badge, so these exist only to check that the result reads
// the same way here as it does there. TestPlanVerdictKeywordsMatchConsole pins the two
// vocabularies against each other, so fixing one side alone cannot leave them disagreeing.
var (
	planApprovedRe = regexp.MustCompile(`(?i)approv|proceed|start coding|going to code|承認|実行してよい|yes`)
	planRejectedRe = regexp.MustCompile(`(?i)keep planning|not approv|reject|refine|declin|interrupt|却下|中止|やり直`)
)

// Plan verdicts. "unknown" = readable as neither approved nor rejected, and the Console
// falls back to a neutral "decided" badge. Harmless next to a wrong badge, but if
// it keeps happening a drift is under way.
const (
	PlanApproved = "approved"
	PlanRejected = "rejected"
	PlanUnknown  = "unknown"
)

// PlanVerdict classifies an ExitPlanMode tool_result exactly as the Console's
// planOutcome() does for a card with no optimistic reject mark: the embedded plan body is
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
// Console degrades to a neutral "decided" badge, which is safe but SILENT — so say it once,
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
	log.Printf("claude plan drift: could not read the ExitPlanMode result as either approved or "+
		"rejected (qid=%s). The CLI wording may have changed - check planDecision.ts / plan_verdict.go. result: %q",
		qid, head)
}
