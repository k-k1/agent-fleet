// What the Active view of the left pane lists, in which group and in what order (ADR 0145) —
// pure functions only (no store, no clock), so the node vitest project pins them.
//
// The Sessions view answers "where": every working copy, running or not. This one answers
// "what is going on now", so a group exists only while it holds a listed session.
//
//   SCOPE  … running sessions; stopped ones that still need the member (stopped on an
//            unanswered exchange, or holding a handoff nobody took); and every descendant of
//            a listed session, stopped or not, so a family is never shown with holes. Shells and
//            SSM logins are the Terminal view's; unattended runs are listed in the trailing groups
//            (unattendedScope).
//   GROUP  … the tree's three sections in the tree's order: repositories (by remote, as the
//            sessions overview groups them), spaces, other. Two groups follow them: the RUNNING
//            unattended runs, one group for auto-run and one for schedules, as families like the
//            others (decision 2 note). They are not in the scope above: see unattendedScope.
//   FOLD   … a parent hides its descendants until the member opens it. The stored value is
//            the OPENED parents, not the folded ones: a parent that gains its first child
//            must start folded without anybody having recorded it.
import { childrenIndex, descendantNames } from "../sessions/family.ts";
import { isWaiting } from "../sessions/waiting.ts";
import { foldFamilies, orderByFamily, repoGroupId } from "../overview/overview.ts";
import type { FoldInfo } from "../overview/overview.ts";
import { sessionAutoKind, sessionFolder, withoutTerminals, withoutUnattended } from "../../lib/project.ts";
import { sessionInWorkingSet } from "../../lib/workingSets.ts";
import { compareText } from "../../lib/intl.ts";
import type { Space } from "../spaces/store.ts";
import type { Repo } from "../repos/store.ts";
import type { WorkingSet } from "../../lib/workingSets.ts";
import type { Session } from "../../types/session.ts";

export type ActiveGroupKind = "repo" | "space" | "other" | "autorun" | "schedule";

/** The two groups of unattended runs: folded until the member opens them, and their waiting
 *  sessions are not counted on the rail icon (waitingSessions leaves unattended runs out). */
export const isUnattendedKind = (k: ActiveGroupKind): boolean => k === "autorun" || k === "schedule";

export const AUTORUN_GROUP = "u:autorun";
export const SCHEDULE_GROUP = "u:schedule";

/** One heading of the view. */
export interface ActiveGroup {
  /** Identity and React key. Never empty: "o:" is the trailing "other" group. */
  key: string;
  kind: ActiveGroupKind;
  /** Heading text. "" for the "other" group and the two unattended ones — the view localizes those. */
  label: string;
  /** Tooltip: what the group was formed on (the remote, the base folder, the space folder). */
  hint: string;
  /** Cards in order, after the fold has taken its rows out. */
  sessions: Session[];
  /** Running sessions in the group, counted before the fold: folding a family must not read
   *  as sessions having ended. */
  alive: number;
  /** Every session the group lists, also counted before the fold. */
  total: number;
  /** Sessions in the group that wait for the member's answer, counted before the fold: a
   *  folded heading must still say that something under it needs an answer. */
  waiting: number;
  /** What each card's fold control says. Only names still in `sessions` have an entry. */
  fold: Map<string, FoldInfo>;
  /** Per folded parent, how many of the rows it hides wait for the member's answer. A fold
   *  that kept this to itself would hide the one row the view exists to surface. */
  hiddenWaiting: Map<string, number>;
  /** How deep each card sits under its family's root (0 = a root). One column cannot show
   *  lineage by position alone, so the view indents by this. */
  depth: Map<string, number>;
}

export const OTHER_GROUP = "o:";

/** A stopped session the member still owes something: it stopped on an unanswered exchange,
 *  or it proposed a handoff that has not been taken. */
export const needsMember = (s: Session): boolean => !s.alive && (!!s.carried || !!s.handoffPending);

/** The sessions the view lists. `shown` is the schedules whose runs the member's own views
 *  list (withoutUnattended); `repos` places a session in the working set the way the tree
 *  does (sessionInWorkingSet). */
export function activeScope(sessions: Session[], set: WorkingSet | null, shown?: ReadonlySet<string>, repos: Repo[] = []): Session[] {
  const byFolder = new Map(repos.map((r) => [r.name, r]));
  const inSet = (s: Session) => !set || sessionInWorkingSet(set, s, byFolder);
  // Unattended is decided against the whole list: a run's child is unattended through a
  // parent the working set may have filtered out.
  const mine = withoutTerminals(withoutUnattended(sessions, shown));
  const keep = new Set(mine.filter((s) => (s.alive || needsMember(s)) && inSet(s)).map((s) => s.name));
  // Descendants are walked through every session, the ones outside the set included, and the
  // set is applied to the result: a generation the set leaves out must not cut off the ones
  // below it that the set holds.
  for (const n of descendantNames([...keep], mine)) keep.add(n);
  return mine.filter((s) => keep.has(s.name) && inSet(s));
}

/** The running unattended sessions and every descendant of one, for the two trailing groups. A
 *  stopped run whose child still runs is not listed itself, as activeScope does not list a
 *  stopped parent: the child is then a root of its group. Nothing stopped is listed for the
 *  member's attention. What `shown` hands back to the member's own views
 *  is not unattended (sessionAutoKind answers "") and is listed in its repository, never twice. */
export function unattendedScope(sessions: Session[], set: WorkingSet | null, shown?: ReadonlySet<string>, repos: Repo[] = []): Session[] {
  const byFolder = new Map(repos.map((r) => [r.name, r]));
  const inSet = (s: Session) => !set || sessionInWorkingSet(set, s, byFolder);
  const byName = new Map(sessions.map((s) => [s.name, s] as const));
  const mine = withoutTerminals(sessions).filter((s) => sessionAutoKind(s, byName, shown) !== "");
  const keep = new Set(mine.filter((s) => s.alive && inSet(s)).map((s) => s.name));
  for (const n of descendantNames([...keep], mine)) keep.add(n);
  return mine.filter((s) => keep.has(s.name) && inSet(s));
}

/** The members whose parent link is cut so that the group's lineage is a forest. A corrupted
 *  originSession cycle has no root: left alone, every member is a folded child of another and
 *  the whole family is off screen with nothing to open. One link per cycle is cut, at the
 *  member with the smallest name, so the choice does not move between polls. */
export function cycleCuts(sessions: Session[]): Set<string> {
  const byName = new Map(sessions.map((s) => [s.name, s] as const));
  const parentOf = (name: string): string | undefined => {
    const p = byName.get(name)?.originSession;
    return p && p !== name && byName.has(p) ? p : undefined;
  };
  const cuts = new Set<string>();
  const done = new Set<string>();
  for (const s of sessions) {
    const path: string[] = [];
    const at = new Map<string, number>();
    let cur: string | undefined = s.name;
    while (cur !== undefined && !done.has(cur) && !at.has(cur)) {
      at.set(cur, path.length);
      path.push(cur);
      cur = parentOf(cur);
    }
    if (cur !== undefined && at.has(cur)) {
      const cycle = path.slice(at.get(cur));
      cuts.add(cycle.reduce((a, b) => (b < a ? b : a)));
    }
    for (const n of path) done.add(n);
  }
  return cuts;
}

interface GroupId {
  key: string;
  kind: ActiveGroupKind;
  label: string;
  hint: string;
}

/** The known working copy a "<parent>@<slug>" folder without a row of its own was made from, or
 *  "" when it cannot be shown to belong to one. A name alone proves nothing (a clone may be
 *  called "app@notes"), so when the session records its dir, that dir must sit beside the
 *  parent's own; when it records none, the name is all there is. */
function knownParent(s: Session, folder: string, byFolder: Map<string, Repo>): string {
  for (let at = folder.lastIndexOf("@"); at > 0; at = folder.lastIndexOf("@", at - 1)) {
    const parent = folder.slice(0, at);
    const row = byFolder.get(parent);
    if (!row) continue;
    const dir = (s.dir || "").replace(/\/+$/, "");
    if (dir && row.path) {
      const parentOf = (p: string) => p.slice(0, p.lastIndexOf("/"));
      if (dir !== parentOf(row.path.replace(/\/+$/, "")) + "/" + folder) return "";
    }
    return parent;
  }
  return "";
}

// The space's name is looked up here rather than through the spaces store's helper: that
// module reaches the api client, and this file has to stay loadable without a browser.
function groupId(s: Session, byFolder: Map<string, Repo>, spaces: Space[], reposKnown: boolean): GroupId {
  if (s.space) return { key: "s:" + s.space, kind: "space", label: spaces.find((x) => x.folder === s.space)?.name || s.space, hint: s.space };
  const folder = sessionFolder(s);
  // A folder the working-copy list does not know is "other", as it is in the tree: a heading
  // made of a folder that no longer exists would name a place nobody can go to.
  if (folder && byFolder.has(folder)) return { ...repoGroupId(folder, byFolder), kind: "repo" };
  // A worktree folder is "<parent>@<slug>", and the parent can itself contain "@" (a worktree
  // made from a worktree), so the longest known prefix wins. The list can lag behind a session
  // that just started in a new worktree (or behind a tab that was hidden): the parent stands in
  // for the missing row, and the session stays under its repository's heading, not "Other".
  const parent = knownParent(s, folder, byFolder);
  if (parent) return { ...repoGroupId(parent, byFolder), kind: "repo" };
  // No list at all (the first load failed or has not answered): nothing can be called "other"
  // yet, so the folder's base names the heading; it moves to the repository's own key once the
  // list arrives.
  if (!reposKnown && folder) {
    const base = folder.includes("@") ? folder.slice(0, folder.lastIndexOf("@")) : folder;
    return { key: "c:" + base, kind: "repo", label: base, hint: base };
  }
  return { key: OTHER_GROUP, kind: "other", label: "", hint: "" };
}

const KIND_ORDER: Record<ActiveGroupKind, number> = { repo: 0, space: 1, other: 2, autorun: 3, schedule: 4 };

/** The view, grouped. `expanded` holds the parents the member opened; set=null means no
 *  working set narrows the view. */
export function activeGroups(
  sessions: Session[],
  repos: Repo[],
  spaces: Space[],
  set: WorkingSet | null,
  expanded: ReadonlySet<string> = new Set(),
  shown?: ReadonlySet<string>,
  reposKnown = repos.length > 0,
): ActiveGroup[] {
  const byFolder = new Map(repos.map((r) => [r.name, r]));
  const members = new Map<string, { id: GroupId; sessions: Session[] }>();
  for (const s of activeScope(sessions, set, shown, repos)) {
    const id = groupId(s, byFolder, spaces, reposKnown);
    const g = members.get(id.key) || { id, sessions: [] };
    g.sessions.push(s);
    members.set(id.key, g);
  }
  const allByName = new Map(sessions.map((s) => [s.name, s] as const));
  for (const s of unattendedScope(sessions, set, shown, repos)) {
    const auto = sessionAutoKind(s, allByName, shown);
    if (!auto) continue;
    const id: GroupId = { key: auto === "autorun" ? AUTORUN_GROUP : SCHEDULE_GROUP, kind: auto === "autorun" ? "autorun" : "schedule", label: "", hint: "" };
    const g = members.get(id.key) || { id, sessions: [] };
    g.sessions.push(s);
    members.set(id.key, g);
  }
  const out: ActiveGroup[] = [];
  for (const { id, sessions: list } of members.values()) {
    // Everything below reads lineage from these copies, in which a cycle has been opened, and
    // hands the member's own objects back: order, fold, the waiting count and the depth must
    // all see the same forest.
    const cuts = cycleCuts(list);
    const real = new Map(list.map((s) => [s.name, s] as const));
    const ordered = orderByFamily(cuts.size ? list.map((s) => (cuts.has(s.name) ? { ...s, originSession: undefined } : s)) : list);
    // A parent is a parent only inside its group, as in the overview: a child started in
    // another repository is a root under its own heading.
    const { childrenOf } = childrenIndex(ordered);
    const collapsed = new Set([...childrenOf.keys()].filter((name) => !expanded.has(name)));
    const folded = foldFamilies(ordered, collapsed);
    const hiddenWaiting = new Map<string, number>();
    for (const s of folded.sessions) {
      if (!folded.fold.get(s.name)?.hidden) continue;
      const below = descendantNames([s.name], ordered);
      const n = ordered.filter((x) => below.has(x.name) && isWaiting(x)).length;
      if (n > 0) hiddenWaiting.set(s.name, n);
    }
    const byName = new Map(ordered.map((s) => [s.name, s] as const));
    const depth = new Map<string, number>();
    for (const s of folded.sessions) {
      // The cycles are cut, and a self-reference is no parent; the bound is for a list that
      // changes shape in some way nobody thought of.
      let d = 0;
      for (let cur = s; d < ordered.length; d++) {
        const p = cur.originSession;
        const parent = p && p !== cur.name ? byName.get(p) : undefined;
        if (!parent) break;
        cur = parent;
      }
      depth.set(s.name, d);
    }
    out.push({
      ...id,
      sessions: folded.sessions.map((s) => real.get(s.name) ?? s),
      alive: ordered.filter((s) => !!s.alive).length,
      total: ordered.length,
      waiting: ordered.filter((s) => isWaiting(s)).length,
      fold: folded.fold,
      hiddenWaiting,
      depth,
    });
  }
  // Headings in name order inside each kind, stable while sessions come and go: a list that
  // is watched must not reorder its sections every time something starts.
  out.sort((a, b) => KIND_ORDER[a.kind] - KIND_ORDER[b.kind] || compareText(a.label, b.label) || compareText(a.key, b.key));
  return out;
}
