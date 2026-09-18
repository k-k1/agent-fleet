// stopTree — which sessions "stop everything under this working copy" would stop, and how.
//
// The everyday case is the mirror image of deleteTree's: a spawn left four sessions running
// across a base clone and its worktrees, and putting the workspace down for the night means
// pressing stop on each row in turn. The subtree the rail already draws (lib/project.ts
// repoTree) is the scope, so "what the rail shows below this row" and "what the run stops"
// stay the same set — the same property deleteTree exists to keep.
//
// A stop is NOT a delete: the conversation stays and the session resumes, so nothing here is
// irreversible. The one thing a stop can destroy is a turn that is still being written, and
// that is what the two modes are for:
//
//   - now   — halt (the Console's stop button). Instant, and it cuts a running turn off.
//   - after — the stop-after-turn arm (docs/log/85): the session folds itself away once the
//             turn it is running ends. Nothing in flight is lost, so this is what a busy row
//             gets by default.
//
// Which is why the arm is NOT offered to every row. It is consumed by the report reconciler's
// "has the turn ended" evidence, and a session parked on a question / a usage limit / an
// expired login is never quiet by that measure — arming one would leave it running with a
// promise nothing keeps. Those rows are not busy either, so they take the plain halt.
import type { Repo } from "./store.ts";
import type { Session } from "../../types/session.ts";
import type { MsgKey } from "../../lib/i18n/index.ts";
import type { RepoTreeNode } from "../../lib/project.ts";
import { sessionsInFolder } from "../../lib/project.ts";
import { agentOf } from "../../agents/registry.ts";
import { remainingShort } from "../../lib/sessionview.ts";

/** What will be sent for one ticked row. */
export type StopMode = "now" | "after";

/** States in which a turn is actually being computed — the only ones an immediate stop cuts
 *  off. Everything else an alive session can show (question / plan / permission / blocked /
 *  limited / auth …) is the session waiting for somebody or something else. */
const BUSY_STATES = new Set(["working", "compacting"]);

/** One live session's row in the plan. */
export interface StopRow {
  session: Session;
  /** Working-copy folder it runs in — the group it is listed under. */
  copy: string;
  /** A turn is in flight: stopping now loses the reply being written. */
  busy: boolean;
  /** This kind can end a turn, so the stop-after-turn arm will eventually fire on it.
   *  False for shell / ssm, which have no turn model at all (caps.fixedAliveChip). */
  canArm: boolean;
  /** Keep-awake pin remaining ("" = not pinned). */
  pinned: string;
  /** Already armed from inside the conversation (af_stop_after_turn) or the session menu. */
  armed: boolean;
  /** Ticked when the modal opens. */
  pick: boolean;
  /** i18n key of the one-line "why" under the row; "" when the row needs no explanation. */
  whyKey: MsgKey | "";
}

/** One working copy of the subtree, with the live sessions it holds. Copies with none are
 *  left out entirely: an empty group is a row that can never be acted on. */
export interface StopGroup {
  repo: Repo;
  /** 0 = the copy that was right-clicked; each nested worktree adds one. */
  depth: number;
  rows: StopRow[];
}

function rowFor(s: Session, copy: string): StopRow {
  const busy = BUSY_STATES.has(String(s.state || "")) || !!s.backgroundBusy;
  // fixedAliveChip is exactly "this kind has no working/idle model": af cannot tell a running
  // build from an abandoned pager in a shell, and no hook will ever say the turn ended.
  const canArm = !agentOf(s.kind).caps.fixedAliveChip;
  const pinned = remainingShort(s.keepAwakeUntil);
  const armed = !!s.stopAfterTurnAt;
  // Pre-ticked only when stopping this row costs nothing: an idle agent session, or a busy one
  // that will be armed rather than cut off. The two that are left out are the two where af is
  // NOT the one who knows — a shell whose command it cannot see, and a session the user pinned
  // awake on purpose. Ticking those is the user saying it, the same deliberate act the delete
  // plan asks for on a row that would lose work.
  const pick = canArm && !pinned;
  const whyKey: MsgKey | "" = pinned
    ? "rp.stop.why_pinned"
    : !canArm
      ? "rp.stop.why_opaque"
      : armed
        ? "rp.stop.why_armed"
        : "";
  return { session: s, copy, busy, canArm, pinned, armed, pick, whyKey };
}

/** Flattens the rail's subtree into per-copy groups of LIVE sessions, the right-clicked copy
 *  first (pre-order). Stopped sessions are not rows: there is nothing to stop. */
export function planStopTree(node: RepoTreeNode, sessions: Session[], depth = 0): StopGroup[] {
  const rows = sessionsInFolder(sessions, node.repo.name)
    .filter((s) => s.alive)
    .map((s) => rowFor(s, node.repo.name));
  const here: StopGroup[] = rows.length ? [{ repo: node.repo, depth, rows }] : [];
  return [...here, ...node.children.flatMap((c) => planStopTree(c, sessions, depth + 1))];
}

/** Every row of the plan, groups flattened away. */
export const stopRows = (groups: StopGroup[]): StopRow[] => groups.flatMap((g) => g.rows);

export function defaultStopSelection(groups: StopGroup[]): Set<string> {
  return new Set(stopRows(groups).filter((r) => r.pick).map((r) => r.session.name));
}

/** How a ticked row is stopped. `force` is the modal's "stop the running ones right away too"
 *  tick — it only ever moves a row from `after` to `now`, never the other way. */
export const stopModeOf = (r: StopRow, force: boolean): StopMode =>
  r.busy && r.canArm && !force ? "after" : "now";

/** Rows that will be acted on, in listed order. */
export const stopOrder = (groups: StopGroup[], selected: Set<string>): StopRow[] =>
  stopRows(groups).filter((r) => selected.has(r.session.name));

export interface StopSummary {
  /** Rows stopped immediately. */
  now: number;
  /** Rows that get the arm instead. */
  after: number;
  total: number;
  /** Ticked rows that are mid-turn and would be cut off — the warning line's count. It is
   *  the count with `force` ON, so it is 0 while the arm is still protecting them. */
  cut: number;
  /** Busy rows among the ticked ones, whether or not `force` is on: what the force tick
   *  offers, and so the condition for showing it at all. */
  busy: number;
}

export function summarizeStop(groups: StopGroup[], selected: Set<string>, force: boolean): StopSummary {
  const rows = stopOrder(groups, selected);
  const busy = rows.filter((r) => r.busy && r.canArm);
  const after = busy.filter((r) => stopModeOf(r, force) === "after").length;
  return {
    now: rows.length - after,
    after,
    total: rows.length,
    cut: busy.length - after,
    busy: busy.length,
  };
}

/** Live sessions anywhere in this subtree — what the right-click menu counts to decide
 *  whether the item is worth offering at all. */
export function liveSessionCount(node: RepoTreeNode, sessions: Session[]): number {
  return stopRows(planStopTree(node, sessions)).length;
}
