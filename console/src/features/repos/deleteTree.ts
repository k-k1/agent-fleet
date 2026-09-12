// deleteTree — what "delete this working copy" takes with it, graded.
//
// The everyday leftover is a finished spawn: a worktree whose session is stopped, its work
// already in the parent, and three more like it hanging underneath. Removing that by hand is
// one archive plus one delete per copy, in the right order, so it does not get done. This
// turns the subtree the rail ALREADY draws (lib/project.ts repoTree, which nests a worktree
// under the copy whose session spawned it) into a plan the modal can list and run in one go.
//
// The lineage is derived here rather than asked of the Agent for the reason repoTree gives:
// worktree parentage exists nowhere on disk — the folder and branch slugs are random and the
// branch is renamed afterwards — so session metadata is the only link. Listing and deleting
// from the same derivation is what keeps "what the rail shows below this row" and "what the
// run deletes" the same set.
import type { Repo } from "./store.ts";
import type { Session } from "../../types/session.ts";
import type { MsgKey } from "../../lib/i18n/index.ts";
import type { RepoTreeNode } from "../../lib/project.ts";
import { sessionsInFolder } from "../../lib/project.ts";
import { agentOf } from "../../agents/registry.ts";

/**
 * safe    — nothing is lost by removing it: merged into the parent, clean, nobody working in it.
 * review  — removable, but something goes with it (uncommitted, unpushed, or not in the parent).
 * blocked — the Agent will refuse it (a lock, a live session), and no force flag gets past that.
 */
export type CopyGrade = "safe" | "review" | "blocked";

/** One working copy's row in the plan. */
export interface CopyPlan {
  repo: Repo;
  /** 0 = the copy that was right-clicked; each nested worktree adds one. */
  depth: number;
  /** Sessions living in this copy. Archived ones are not in the list the rail holds. */
  sessions: Session[];
  /** Stopped sessions to archive (the shelf keeps the conversation). */
  archive: Session[];
  /** Stopped shell/ssm sessions to forget — no conversation worth shelving. */
  forget: Session[];
  grade: CopyGrade;
  /** i18n key of the one-line "why" under the row; "" when it is plainly safe. Typed so a
   *  key that does not exist in the catalogue fails tsc here rather than printing itself. */
  whyKey: MsgKey | "";
  /** The number that `why` interpolates (sessions, or commits). */
  whyCount: number;
  /** The DELETE needs ?force=true: the Agent refuses a dirty or unpushed worktree without it. */
  force: boolean;
  /** Branch that can go with this copy: a worktree branch already contained in its parent. */
  branch: string;
  /** Working copy the branch delete runs in (the base clone) — the branch is checked out here. */
  branchIn: string;
}

/** Sessions that hold a copy open whatever flags are passed: running, or deletion-locked. */
const blockers = (sessions: Session[]) => ({
  alive: sessions.filter((s) => s.alive),
  locked: sessions.filter((s) => s.locked),
});

function gradeCopy(r: Repo, sessions: Session[]): Pick<CopyPlan, "grade" | "whyKey" | "whyCount"> {
  const { alive, locked } = blockers(sessions);
  // Blocked first, and in the order the Agent checks: a locked copy answers 403 before
  // anything else is looked at, so naming a different reason would send the user to fix
  // the wrong thing.
  if (r.locked) return { grade: "blocked", whyKey: "rp.del.why_locked", whyCount: 0 };
  if (alive.length) return { grade: "blocked", whyKey: "rp.del.why_alive", whyCount: alive.length };
  if (locked.length) return { grade: "blocked", whyKey: "rp.del.why_session_locked", whyCount: locked.length };
  // Then what would be LOST, most immediate first: work never committed cannot be recovered
  // from anywhere, commits the parent does not have are recoverable only by reflog, and
  // unpushed commits at least exist in the parent's history.
  if (r.dirty) return { grade: "review", whyKey: "rp.del.why_dirty", whyCount: 0 };
  const rel = r.integration?.relation;
  if (rel === "unmerged" || rel === "diverged") {
    return { grade: "review", whyKey: "rp.del.why_unmerged", whyCount: r.integration?.worktreeUnique || 0 };
  }
  if ((r.ahead || 0) > 0) return { grade: "review", whyKey: "rp.del.why_unpushed", whyCount: r.ahead || 0 };
  // "unknown" is not "fine": it is the answer when the comparison could not be made at all.
  if (rel === "unknown") return { grade: "review", whyKey: "rp.del.why_unknown", whyCount: 0 };
  return { grade: "safe", whyKey: "", whyCount: 0 };
}

/** Whether this copy's branch may be deleted along with it. */
function branchOf(r: Repo): { branch: string; branchIn: string } {
  const rel = r.integration?.relation;
  // Only a worktree's own branch, only when the parent's history already contains it, and
  // only when we know which copy to run the delete in — the branch is checked out HERE, so
  // it can only be deleted from the parent clone, after this worktree is gone.
  if (!r.worktree || !r.branch || !r.parent) return { branch: "", branchIn: "" };
  if (rel !== "same" && rel !== "contained") return { branch: "", branchIn: "" };
  return { branch: r.branch, branchIn: r.parent };
}

/** Flattens the rail's subtree into rows, the right-clicked copy first (pre-order). */
export function planTree(node: RepoTreeNode, sessions: Session[], depth = 0): CopyPlan[] {
  const r = node.repo;
  const mine = sessionsInFolder(sessions, r.name);
  const stopped = mine.filter((s) => !s.alive && !s.locked);
  const { branch, branchIn } = branchOf(r);
  const row: CopyPlan = {
    repo: r,
    depth,
    sessions: mine,
    // The same split the rail's bulk archive uses: a coding session's conversation goes to
    // the shelf, a shell has none to keep.
    archive: stopped.filter((s) => !agentOf(s.kind).caps.ephemeral),
    forget: stopped.filter((s) => agentOf(s.kind).caps.ephemeral),
    ...gradeCopy(r, mine),
    force: !!r.dirty || (r.ahead || 0) > 0,
    branch,
    branchIn,
  };
  return [row, ...node.children.flatMap((c) => planTree(c, sessions, depth + 1))];
}

/** Ticked when the modal opens: the rows that lose nothing. A row that would take work
 *  with it is left for the user to tick — that tick IS the confirmation the old flow got
 *  from its second "force delete?" dialog. */
export function defaultSelection(plans: CopyPlan[]): Set<string> {
  return new Set(plans.filter((p) => p.grade === "safe").map((p) => p.repo.name));
}

/** Run order: deepest first, so a copy is never removed before the ones nested under it,
 *  and the base clone (which git refuses to remove while a worktree of it is registered)
 *  goes last. Ties keep the listed order. */
export function deleteOrder(plans: CopyPlan[], selected: Set<string>): CopyPlan[] {
  return plans
    .filter((p) => selected.has(p.repo.name) && p.grade !== "blocked")
    .map((p, i) => ({ p, i }))
    .sort((a, b) => b.p.depth - a.p.depth || a.i - b.i)
    .map(({ p }) => p);
}

/** A base clone cannot go while any linked worktree of it survives — the Agent answers
 *  has_worktrees, and no force flag passes it. The rail nests a base's whole group under
 *  it, so "every other row in this plan is going too" is exactly the condition. It depends
 *  on the SELECTION, so it is recomputed as the user ticks rather than baked into the grade. */
export function baseBlockedByWorktrees(plans: CopyPlan[], selected: Set<string>): boolean {
  const root = plans[0];
  if (!root || root.repo.worktree) return false;
  if (!selected.has(root.repo.name)) return false;
  return plans.slice(1).some((p) => !selected.has(p.repo.name) || p.grade === "blocked");
}

export interface RunSummary {
  copies: number;
  archive: number;
  forget: number;
  branches: number;
  /** Rows that need force=true — the count the warning line shows. */
  force: number;
}

export function summarize(plans: CopyPlan[], selected: Set<string>, withBranches: boolean): RunSummary {
  const rows = deleteOrder(plans, selected);
  return {
    copies: rows.length,
    archive: rows.reduce((n, p) => n + p.archive.length, 0),
    forget: rows.reduce((n, p) => n + p.forget.length, 0),
    branches: withBranches ? rows.filter((p) => p.branch).length : 0,
    force: rows.filter((p) => p.force).length,
  };
}
