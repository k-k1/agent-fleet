// The first prompt for a session launched to REVIEW another session's plan, and the title
// that session gets. Pure functions, so the wording is testable without the mirror.
//
// It hands over a PATH, never the plan body: measured, real plans run 12-36 KB, and a TUI
// launch delivers its first prompt by typing it into the pane. The path also survives the
// round trip — claude rewrites the same file when it revises the plan, so a second review
// reads the updated plan at the same place.
//
// The result comes back by PEER MESSAGE, which is why the button rejects the plan before
// launching: free text (a peer message included) is refused while the approval dialog is
// up, because the modal would swallow it and the trailing Enter would approve the very
// plan under review. Rejected, the planner sits idle in plan mode and the message lands.
//
// Written in the Console's display language (ADR0033, "who reads this string"): the two
// readers are the reviewing model and, through the peer message, the planning session —
// both of which follow the user's language here.
import { t } from "../../lib/i18n/index.ts";
import { clampSessionTitle } from "../../lib/sessionTitle.ts";

export interface ReviewPromptInput {
  /** The session whose plan this is — the peer address the findings go back to. */
  parent: string;
  /** Absolute path of the plan file (from sessionPlanFile). */
  path: string;
  /** Working copy the plan is about. */
  dir: string;
  /** Optional "look at this in particular" line the user typed. */
  focus?: string;
}

export function reviewPrompt({ parent, path, dir, focus }: ReviewPromptInput): string {
  const lines = [
    t("plan.review_prompt_head", { session: parent }),
    "",
    t("plan.review_prompt_file", { path }),
    t("plan.review_prompt_dir", { dir }),
  ];
  const f = (focus || "").trim();
  if (f) lines.push(t("plan.review_prompt_focus", { focus: f }));
  lines.push(
    "",
    t("plan.review_prompt_rules"),
    "",
    t("plan.review_prompt_format"),
    "",
    "## " + t("plan.review_verdict_heading"),
    "approve | changes-requested",
    "",
    "## " + t("plan.review_findings_heading"),
    t("plan.review_prompt_finding_shape"),
    "",
    t("plan.review_prompt_reply", { session: parent }),
  );
  return lines.join("\n");
}

/** Session title for the reviewer, so the rail says WHICH plan it is reviewing. Takes the
 *  already-derived plan heading (planTitle lives in the transcript blocks, which are React;
 *  keeping it out of here keeps this module free of the component tree). */
export function reviewTitle(planHeading: string): string {
  return clampSessionTitle(t("plan.review_title", { title: planHeading }));
}
