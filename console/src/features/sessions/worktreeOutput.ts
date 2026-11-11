// Wire shapes and the request layer for dropping regenerable build output from dormant
// worktrees (#3202, phase 2 of #3079; the Agent side is workspace/agent/internal/wtoutput).
//
//   GET  api/cleanup/worktree-output                       → { dormant_days, worktrees: Report[], measured_at }
//   POST api/cleanup/worktree-output?worktree=NAME&dry_run=1 → Report (200), or Report with
//                                                              status "refused" (409)
//
// The name travels in the query, not the path. Without dry_run=1 the POST deletes and is not
// reversible (nothing goes to the trash), so the only caller that omits it is the row's
// confirmed delete in WorktreeOutputSection.
import { api, errText, raw } from "../../core/api/client.ts";

export interface WtoDir {
  rel: string;
  bytes: number;
  /** A symlink: only the link is removed, `bytes` is the link's own size. */
  link?: boolean;
}

export interface WtoReport {
  name: string;
  path: string;
  /** "candidate" = something can be dropped, "empty" = nothing to drop, "refused". */
  status: "candidate" | "empty" | "refused";
  /** Set when refused; see REASONS. An Agent newer than this Console may send another. */
  reason?: string;
  detail?: string;
  dirs?: WtoDir[];
  bytes: number;
  /** Filled by a real delete (empty on a dry run). */
  removed?: WtoDir[];
  /** "rel: why" for a directory that was left in place. */
  skipped?: string[];
  /** Sizes are lower bounds: the walk stopped at its budget. */
  truncated?: boolean;
}

export interface WtoSurvey {
  dormant_days: number;
  worktrees: WtoReport[];
  measured_at: string;
}

/** Refusal reasons the Console has a sentence for (clean.wto_reason_<reason>). */
export const WTO_REASONS = [
  "not_linked_worktree",
  "locked",
  "live_session",
  "not_dormant",
  "dirty",
  "git_unreadable",
  "in_use",
] as const;

/** The grade a survey row gets. "regenerable" is its own grade, not "safe": what it drops
 *  never goes to the trash, so a "select all safe" must not be able to reach it. */
export type WtoGrade = "regenerable" | "keep";
export const wtoGrade = (r: WtoReport): WtoGrade => (r.status === "candidate" ? "regenerable" : "keep");

const STATUSES = new Set(["candidate", "empty", "refused"]);
const isReport = (v: unknown): v is WtoReport =>
  !!v && typeof v === "object" && STATUSES.has((v as WtoReport).status) && typeof (v as WtoReport).name === "string";

/** Candidates first (largest first), then the rest by name. */
export function sortReports(list: WtoReport[]): WtoReport[] {
  const rank = (r: WtoReport) => (r.status === "candidate" ? 0 : r.status === "empty" ? 1 : 2);
  return [...list].sort((a, b) => rank(a) - rank(b) || b.bytes - a.bytes || a.name.localeCompare(b.name));
}

export type SurveyResult =
  | { kind: "ok"; survey: WtoSurvey }
  | { kind: "unsupported" } // an Agent older than the endpoint
  | { kind: "error"; message: string };

export async function loadSurvey(): Promise<SurveyResult> {
  try {
    const res = await api("api/cleanup/worktree-output");
    const notFound = res?.error?.status === 404 || res?.error?.code === "http_404";
    if (notFound) return { kind: "unsupported" };
    if (res?.error) return { kind: "error", message: errText(res.error) };
    if (!res || !Array.isArray(res.worktrees)) return { kind: "unsupported" };
    return { kind: "ok", survey: res as WtoSurvey };
  } catch {
    return { kind: "error", message: "" };
  }
}

export type ApplyResult =
  | { kind: "report"; report: WtoReport }
  // definite = the Agent refused the request itself (4xx: bad name, no such working copy), so
  // nothing was deleted; otherwise (5xx, a lost connection) the outcome of a delete is unknown.
  | { kind: "error"; message: string; definite: boolean };

/** One POST for one worktree. `dryRun` false DELETES. A 409 carries the Report of a refusal,
 *  so it is a result, not an error; anything else without a Report (400, 404, 502, a network
 *  failure) is an error whose outcome is unknown for a real delete — the caller re-surveys. */
export async function applyWorktreeOutput(name: string, dryRun: boolean): Promise<ApplyResult> {
  const q = `worktree=${encodeURIComponent(name)}${dryRun ? "&dry_run=1" : ""}`;
  try {
    const res = await raw(`api/cleanup/worktree-output?${q}`, { method: "POST" });
    const body = await res.json().catch(() => null);
    if (isReport(body)) return { kind: "report", report: body };
    return { kind: "error", message: body?.error ? errText(body.error) : "", definite: res.status >= 400 && res.status < 500 };
  } catch {
    return { kind: "error", message: "", definite: false };
  }
}
