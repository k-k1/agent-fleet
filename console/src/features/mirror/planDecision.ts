// Plan (ExitPlanMode) decision logic — how the approve/reject buttons drive claude's
// native plan-approval prompt, and how a historical plan's badge is classified.
//
// WHY reject = interrupt, not keystroke navigation:
//   claude's ExitPlanMode prompt is a TUI select menu whose option count/order is
//   CLI-version dependent. The repo pins two real captures that disagree:
//     - a 4-option build (workspace/agent/internal/tmuxx/testdata/footers/
//       modal_plan_approval.txt): 1.Yes-auto / 2.Yes-manual / 3.No-refine-on-web /
//       4.Tell Claude what to change
//     - a 2-option 2.1.212 build (spinner_test.go): 1.Yes-auto / 2.Yes-manual
//   The old reject typed a fixed "Down Down Down Enter" to land on the 4th row. On any
//   shorter, WRAPPING menu those three Downs wrap the cursor back onto a "Yes" row, so
//   reject silently APPROVED the plan (measured: the card badged Reject while claude replied
//   that the plan was approved and carried on coding). There is no fixed Down-offset that
//   selects a reject across every menu shape — so reject must not navigate by position at all.
//   Reject now sends an interrupt (Escape), which dismisses the modal back to plan mode
//   on any layout and records an interrupt tool_result that isRejected() recognises.
//
// Approve stays "Enter" = accept the highlighted default. The default (❯) is always an
// approval ("Yes, …"); the tmuxx plan-approval contract test guards that invariant.
export const PLAN_APPROVE_KEYS: readonly string[] = ["Enter"];

// WHY only the header of the outcome is classified:
//   Newer claude appends the WHOLE approved plan to the ExitPlanMode tool_result:
//     User has approved your plan. You can now start coding. …
//     Your plan has been saved to: …/plans/immutable-dazzling-babbage.md
//     ## Approved Plan:
//     <the entire plan Markdown>
//   The verdict is always in that header; the body is the plan's own prose. Matching the
//   keywords against the whole text lets a plan that merely *mentions* 却下 / 中止 /
//   やり直し / "reject" / "refine" badge its OWN approval as rejected — measured: an
//   approved plan, card badged Reject, claude coding on right under it. Every plan long
//   enough to discuss alternatives is a coin flip. So: cut the embedded plan off first.
const PLAN_BODY_MARKER = /^[ \t]*#{1,6}[ \t]*(approved plan|承認されたプラン)[ \t]*[:：]/im;

// outcomeHead reduces an ExitPlanMode tool_result to the part that states the verdict:
// everything before the embedded plan, and at most a few lines of it (a cheap guard for
// the next version that embeds something without the marker — the verdict has always been
// the first line, and failing to match merely leaves the badge neutral, never wrong).
export function outcomeHead(outcome?: string): string {
  const s = outcome || "";
  const m = s.match(PLAN_BODY_MARKER);
  return (m ? s.slice(0, m.index) : s).slice(0, 400);
}

// isApproved / isRejected guess an ExitPlanMode tool_result's meaning to badge a
// historical plan. Best-effort keyword match — the exact result text varies by version.
export function isApproved(outcome?: string): boolean {
  return /approv|proceed|start coding|going to code|承認|実行してよい|yes/i.test(outcomeHead(outcome));
}
export function isRejected(outcome?: string): boolean {
  // "interrupt" catches a rejected plan's tool_result ("[Request interrupted by user
  // for tool use]"), which is how an Escape/reject out of ExitPlanMode is recorded.
  return /keep planning|not approv|reject|refine|declin|interrupt|却下|中止|やり直/i.test(outcomeHead(outcome));
}

export type PlanOutcome = "approved" | "rejected" | "decided";

// planOutcome badges a plan, reconciling the OPTIMISTIC reject mark (set the instant the
// user clicks reject, before the tool_result lands) against the REAL outcome text.
//
// A definitive approval in the outcome text WINS over the optimistic reject flag: this
// is what stops the symptom above — a card left badged Reject while claude actually
// coded. Empty/unknown outcome keeps the optimistic guess so the badge still flips to
// Reject immediately for instant feedback, then stays Reject once the interrupt result lands.
export function planOutcome(outcome: string | undefined, optimisticRejected: boolean): PlanOutcome {
  if (isApproved(outcome) && !isRejected(outcome)) return "approved";
  if (optimisticRejected || isRejected(outcome)) return "rejected";
  return "decided";
}
