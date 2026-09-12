// Which sessions the overview shows, in which group and in what order — pure functions only
// (no store, no clock), so the node vitest project pins them.
//
// Scope: alive sessions, and stopped ones only when the pane's toggle asks for them; the
// active working set narrows both, so the grid matches what the left rail shows (the same
// rule as the phone's session rotation, rotate.ts).
//
// Two axes, decided with the user (ADR 0078 decisions 6 and 9):
//
//   GROUP  … the repository, identified by its remote (host + owner/name), so every working
//            copy of one project — the base clone and each `@wip-*` worktree, and a second
//            clone under another folder name — lands under one heading. Folder names cannot
//            say this; only the remote can (gitx: Repo.remotePath).
//   ORDER  … inside a group, whole FAMILIES are staged (a family holding a session that waits
//            for a person comes first), and a family is laid out parent → children. The stage
//            is deliberately per family rather than per card: the user asked for parent and
//            child to stay adjacent, stopped children included, and a per-card stage would
//            scatter a family across the group every time one member answered.
import { sessionTier } from "../sessions/order.ts";
import { sessionFolder } from "../../lib/project.ts";
import { sessionInSet } from "../../lib/workingSets.ts";
import { compareText } from "../../lib/intl.ts";
import type { Repo } from "../repos/store.ts";
import type { WorkingSet } from "../../lib/workingSets.ts";
import type { Session } from "../../types/session.ts";

/** One heading of the grid: a repository (or the trailing "no working copy" bucket). */
export interface OverviewGroup {
  /** Identity and React key. "" = the trailing bucket for sessions in no working copy. */
  key: string;
  /** Heading text. "" for the trailing bucket — the view localizes that one. */
  label: string;
  /** Tooltip: the remote identity this group was formed on, or the folder it fell back to. */
  hint: string;
  /** Cards, in grid order. */
  sessions: Session[];
  /** How many of them are running (the heading's count). */
  alive: number;
}

/** The bucket for a session that runs in no working copy (a shell in home). Sorts last. */
export const NO_REPO_GROUP = "";

interface GroupId {
  key: string;
  label: string;
  hint: string;
}

/** The repository a working copy belongs to. The remote is preferred over any folder name:
 *  two clones of one repository are two folders, a linked worktree is a third, and the user
 *  renames all of them. Falls back to the BASE folder (a worktree's parent) so a local-only
 *  repo or an SVN copy still groups its worktrees together. */
function repoGroupId(folder: string, byFolder: Map<string, Repo>): GroupId {
  const repo = byFolder.get(folder);
  // A linked worktree carries its own origin, but fall back to the parent's when it has
  // none (a worktree of a local-only repo), which is also the folder we group by then.
  const parent = repo?.worktree && repo.parent ? byFolder.get(repo.parent) : undefined;
  const host = repo?.remote || parent?.remote || "";
  const path = repo?.remotePath || parent?.remotePath || "";
  if (path) {
    const name = path.split("/").filter(Boolean).pop() || path;
    return { key: "r:" + host + "/" + path, label: name, hint: host ? host + "/" + path : path };
  }
  const base = (repo?.worktree && repo.parent) || folder;
  return { key: "c:" + base, label: base, hint: base };
}

/** Sessions of one family, parent first, then each child's subtree in spawn order.
 *
 * `childrenOf` is keyed by parent name; a name that is not in this group is not a parent here
 * (a child started in ANOTHER repository is a root of its own group — a grid cannot show one
 * card under two headings, and the lineage spine colour already says they are related).
 * `seen` is what keeps a corrupted originSession cycle from recursing forever — the same
 * hazard sessionLineages guards (lib/project.ts). */
function family(root: Session, childrenOf: Map<string, Session[]>, seen: Set<string>): Session[] {
  if (seen.has(root.name)) return [];
  seen.add(root.name);
  const out = [root];
  // Siblings oldest first: inside one family the order IS the spawn order, so a new child
  // appends at the end instead of pushing its elders down. (Roots below go newest first —
  // they are separate pieces of work, and that is the order the grid always had.)
  const kids = [...(childrenOf.get(root.name) || [])].sort(
    (a, b) => compareText(a.createdAt || "", b.createdAt || "") || compareText(a.name, b.name),
  );
  for (const k of kids) out.push(...family(k, childrenOf, seen));
  return out;
}

/** Lowest tier (= most in need of a person) among a family's members: the family's stage. */
const familyTier = (members: Session[]): number => members.reduce((t, m) => Math.min(t, sessionTier(m)), 3);

/** Order one group's sessions: families staged, each laid out parent → children. */
export function orderByFamily(sessions: Session[]): Session[] {
  const present = new Set(sessions.map((s) => s.name));
  const childrenOf = new Map<string, Session[]>();
  const roots: Session[] = [];
  for (const s of sessions) {
    const parent = s.originSession && present.has(s.originSession) && s.originSession !== s.name ? s.originSession : "";
    if (parent) childrenOf.set(parent, [...(childrenOf.get(parent) || []), s]);
    else roots.push(s);
  }
  const seen = new Set<string>();
  const families = roots
    .map((r) => family(r, childrenOf, seen))
    .filter((f) => f.length > 0)
    .map((members) => ({ members, tier: familyTier(members), root: members[0] }));
  // A cycle leaves its members unreachable from any root (every node has a parent). They are
  // appended as their own families so a corrupted meta hides no card.
  for (const s of sessions) {
    if (seen.has(s.name)) continue;
    const members = family(s, childrenOf, seen);
    if (members.length) families.push({ members, tier: familyTier(members), root: members[0] });
  }
  families.sort(
    (a, b) =>
      a.tier - b.tier ||
      compareText(b.root.createdAt || "", a.root.createdAt || "") ||
      compareText(a.root.name, b.root.name),
  );
  return families.flatMap((f) => f.members);
}

/** The grid, grouped by repository. set=null means every session (no filter). */
export function overviewGroups(
  sessions: Session[],
  repos: Repo[],
  set: WorkingSet | null,
  showStopped: boolean,
): OverviewGroup[] {
  const byFolder = new Map(repos.map((r) => [r.name, r]));
  const scoped = sessions.filter((s) => (showStopped || !!s.alive) && (!set || sessionInSet(set, s)));
  const groups = new Map<string, OverviewGroup>();
  for (const s of scoped) {
    const folder = sessionFolder(s);
    const id: GroupId = folder ? repoGroupId(folder, byFolder) : { key: NO_REPO_GROUP, label: "", hint: "" };
    const g = groups.get(id.key) || { ...id, sessions: [], alive: 0 };
    g.sessions.push(s);
    groups.set(id.key, g);
  }
  const out = [...groups.values()];
  for (const g of out) {
    g.sessions = orderByFamily(g.sessions);
    g.alive = aliveCount(g.sessions);
  }
  // Headings in name order, stable while sessions come and go: a grid that is watched must
  // not renumber its sections every time something starts. The "no working copy" bucket
  // trails, as it does in the rail's tree.
  out.sort((a, b) => {
    if ((a.key === NO_REPO_GROUP) !== (b.key === NO_REPO_GROUP)) return a.key === NO_REPO_GROUP ? 1 : -1;
    return compareText(a.label, b.label) || compareText(a.key, b.key);
  });
  return out;
}

/** How many of the cards are running — the head's count, so it says the same thing the
 * grid shows even while stopped rows are mixed in. */
export const aliveCount = (sessions: Session[]): number => sessions.filter((s) => !!s.alive).length;

/** Compact elapsed time ("45s" / "12m" / "3h20m" / "2d"), for "waiting since". Same shape as
 *  sessionview's remainingShort, which is the other duration the rail shows, so the two read
 *  alike. "" when the instant is unknown or in the future. */
export function elapsedShort(sinceMs: number, now: number = Date.now()): string {
  if (!isFinite(sinceMs) || sinceMs <= 0) return "";
  const ms = now - sinceMs;
  if (ms < 0) return "";
  const secs = Math.floor(ms / 1000);
  if (secs < 60) return `${secs}s`;
  const mins = Math.floor(secs / 60);
  if (mins < 60) return `${mins}m`;
  const hours = Math.floor(mins / 60);
  if (hours < 24) {
    const m = mins % 60;
    return m ? `${hours}h${m}m` : `${hours}h`;
  }
  const days = Math.floor(hours / 24);
  const h = hours % 24;
  return h ? `${days}d${h}h` : `${days}d`;
}
