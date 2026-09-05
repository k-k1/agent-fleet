// Composing the comment that goes back to the ticket (docs/log/80 §80.10 / ADR 0061 decision 6).
//
// What af drafts and what it does NOT drafts is the whole design here. It assembles the
// MECHANICAL parts — which branch, which files, which session — because those are tedious
// to gather by hand and af is the only thing that knows them per session (docs/log/68's
// transcript × git join). It does NOT write the narrative: there is no summary generated
// from the transcript, because a comment on someone else's ticket is a statement the user
// is making, and a plausible-sounding generated one is exactly the kind of thing people
// post without reading.
//
// So the draft opens with an empty line for the human, and everything below it is fact.
import { t } from "../../lib/i18n/index.ts";
import { folderBase } from "../../lib/workingSets.ts";
import type { SessionFile } from "../mirror/sessionFiles.ts";
import type { WorkItem, WorkItemSessionRef } from "./read.ts";

/** How many file paths the draft lists before it stops and says how many are left. A
 * hundred-line comment is not a report, and the reader can see the branch. */
export const REPORT_FILE_CAP = 20;

export interface ReportDraftInput {
  item: WorkItem;
  session: WorkItemSessionRef;
  files: SessionFile[];
  /** The note the user typed (empty on first open — the draft leads with it). */
  note?: string;
}

/** Paths worth naming, de-duplicated and sorted. A file the agent only touched through a
 * subagent still counts — it changed either way.
 *
 * Paths are REPO-RELATIVE, not working-copy-relative. Rendering the draft showed the
 * real thing this must not do: a work-item launch creates a worktree, so `repo` is
 * "webshop@checkout-validation" and the comment would have read
 * "webshop@checkout-validation/src/checkout/validate.ts" — a local folder name that means
 * nothing to whoever reads the ticket, and that publishes how this workspace lays out its
 * checkouts. The reader wants the path as it exists in the repository.
 *
 * The one case where the prefix is still needed is a session that touched TWO working
 * copies: bare rel paths could then collide ("src/index.ts" twice) and the de-dup would
 * silently drop one. There the base repo name (worktree slug stripped) goes back in
 * front — the base, never the worktree. */
export function reportFilePaths(files: SessionFile[]): string[] {
  const repos = new Set(files.map((f) => (f.repo ? folderBase(f.repo) : "")).filter(Boolean));
  const multi = repos.size > 1;
  const seen = new Set<string>();
  for (const f of files) {
    let p = f.path;
    if (f.repo && f.rel) p = multi ? `${folderBase(f.repo)}/${f.rel}` : f.rel;
    if (p) seen.add(p);
  }
  return [...seen].sort((a, b) => a.localeCompare(b));
}

/** Build the draft body. Deterministic: same inputs, same text (no LLM, no clock). */
export function composeReportDraft({ item, session, files, note = "" }: ReportDraftInput): string {
  const paths = reportFilePaths(files);
  const shown = paths.slice(0, REPORT_FILE_CAP);
  const lines: string[] = [];
  if (note.trim()) lines.push(note.trim(), "");
  lines.push(t("wi.report_worked_on", { key: item.key }));
  if (session.branch) lines.push(t("wi.report_branch", { branch: session.branch }));
  if (paths.length) {
    lines.push("", t("wi.report_files", { count: paths.length }));
    for (const p of shown) lines.push(`- ${p}`);
    if (paths.length > shown.length) lines.push(t("wi.report_files_more", { count: paths.length - shown.length }));
  } else {
    lines.push("", t("wi.report_no_files"));
  }
  return lines.join("\n");
}

/** The one-line summary of what will be posted where, shown above the editor so the
 * destination is never a surprise. */
export function reportTarget(item: WorkItem): string {
  return `${item.key} — ${item.title}`;
}
