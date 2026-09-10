// Project derivation — pure helpers that turn the two flat lists (GET /api/repos,
// GET /api/sessions) into the left rail's working-copy hierarchy. No backend
// change: a "project" is entirely derived here.
//
// A NODE is one working copy (a folder under ~/repos): either a base clone
// (worktree !== true) or a linked worktree (worktree === true, parent = the base
// folder name). Each node carries its own sessions (those running in that folder)
// and its own file subtree (repos/<name>).
//
// Within one base's group the worktrees are NESTED by spawn lineage (repoTree): a
// worktree sits under the working copy the session that created it was spawned from.
// That relation exists nowhere on disk — see worktreeOwners for why it has to be
// derived, and worktreeParentFolder for the five ways it legitimately fails to resolve.
import type { Repo } from "../features/repos/store.ts";
import type { Session } from "../types/session.ts";
import { compareText } from "./intl.ts";

// The working-copy folder a session runs in. Agent sessions carry `repo`
// (the folder name); fall back to the working dir's basename so a session with a
// dir but no repo field still lands under its folder. "" = no folder (e.g. a
// shell in home) → an orphan.
export function sessionFolder(s: Session): string {
  if (s.repo) return s.repo;
  const dir = s.dir || "";
  return dir ? dir.split("/").filter(Boolean).pop() || "" : "";
}

// groupedRepos partitions working copies into PROJECT GROUPS: each group is a base
// clone followed by its worktrees (parent === base.name), e.g. [base, wt-a, wt-b].
// Bases sort by name; worktrees by CREATION TIME (oldest first) within their base —
// their folder names are temp/<slug> so a name sort is effectively random and reads
// as unstable; chronological order keeps existing worktrees put and appends new ones
// at the end. A worktree missing createdAt (or tying) falls back to name so the order
// stays deterministic. A worktree whose parent is unknown (its base was deleted)
// becomes its own single-member trailing group so it never disappears. The rail
// renders one visual cluster per group so a base and its worktrees read as one
// project, separated from the next.
export function groupedRepos(repos: Repo[]): Repo[][] {
  const byName = (a: Repo, b: Repo) => compareText(a.name, b.name);
  // Worktree order: createdAt ascending (RFC3339 UTC sorts chronologically as a
  // string), then name as a stable tie-break / fallback when a timestamp is absent.
  const byCreated = (a: Repo, b: Repo) => {
    const ca = a.createdAt || "";
    const cb = b.createdAt || "";
    if (ca && cb && ca !== cb) return compareText(ca, cb);
    return byName(a, b);
  };
  const bases = repos.filter((r) => r.worktree !== true).sort(byName);
  const baseNames = new Set(bases.map((r) => r.name));
  const worktreesByParent = new Map<string, Repo[]>();
  const orphanWorktrees: Repo[] = [];
  for (const r of repos) {
    if (r.worktree !== true) continue;
    if (r.parent && baseNames.has(r.parent)) {
      const list = worktreesByParent.get(r.parent);
      if (list) list.push(r);
      else worktreesByParent.set(r.parent, [r]);
    } else {
      orphanWorktrees.push(r);
    }
  }
  const groups: Repo[][] = [];
  for (const base of bases) groups.push([base, ...(worktreesByParent.get(base.name) || []).sort(byCreated)]);
  for (const o of orphanWorktrees.sort(byCreated)) groups.push([o]);
  return groups;
}

// orderedRepos is the flat display order (groups concatenated) — each base clone
// followed by its worktrees, then the next base.
export function orderedRepos(repos: Repo[]): Repo[] {
  return groupedRepos(repos).flat();
}

// workingCopyLabel — how a working copy identifies itself where its FOLDER name
// is not the useful handle: as PROJECT + BRANCH. The folder of a worktree is
// "<base>@<slug>" (git.go), which says nothing about the branch it has checked
// out, so a list grouped by folder reads as a pile of slugs; the rail's repo rows
// already solve this by titling a worktree with its branch.
//
// project = the base clone's folder name (a worktree borrows its parent's; an
// orphan worktree whose base is gone falls back to the "<base>@" prefix of its
// own folder). branch = whatever the working copy has checked out, "" when it is
// unknown (SVN copies have none, and a repo the store hasn't loaded yet reports
// nothing) — a caller with no branch should fall back to showing the folder.
export function workingCopyLabel(folder: string, repo?: Repo): { project: string; branch: string } {
  const at = folder.indexOf("@");
  const fromFolder = at > 0 ? folder.slice(0, at) : folder;
  const project = repo?.worktree ? repo.parent || fromFolder : repo?.name || fromFolder;
  return { project, branch: repo?.branch || "" };
}

// worktreeTag — WHICH worktree of the project this working copy is: the "@<slug>" half
// of a worktree folder name ("webshop@checkout-validation" → "checkout-validation").
// "" for a base clone, so a caller can render it as an optional second half after the
// project name (workingCopyLabel's `project`).
//
// The repo entry decides whether this IS a worktree; the folder name only supplies the
// slug. A worktree whose folder carries no "@" (created with a custom directory name)
// falls back to its branch — the folder alone would then say nothing. With no repo entry
// at all (repos not loaded yet, or the folder is gone) the "@" split is all we have.
export function worktreeTag(folder: string, repo?: Repo): string {
  const at = folder.indexOf("@");
  const slug = at > 0 ? folder.slice(at + 1) : "";
  if (!repo) return slug;
  if (!repo.worktree) return "";
  return slug || repo.branch || "";
}

// sessionsInFolder returns the sessions running in one working-copy folder, newest
// first (createdAt desc, matching the old per-dir grouping order).
export function sessionsInFolder(sessions: Session[], folderName: string): Session[] {
  return sessions
    .filter((s) => sessionFolder(s) === folderName)
    .sort((a, b) => compareText(b.createdAt || "", a.createdAt || ""));
}

// ─────────────────────────────────────────────────────────────────────────────
// Spawn lineage (ADR 0073): which working copies and which sessions belong to the
// same family. Everything below is derived from the two flat lists — there is no
// worktree metadata store to ask, and none can be introduced: gitx.Repo IS the
// filesystem walk (`worktree` = IsLinkedWorktree, `createdAt` = the .git gitfile's
// mtime), so a store would stay empty for every worktree made by hand.
// ─────────────────────────────────────────────────────────────────────────────

// olderSession — the "created first" test, total and deterministic. A session with
// no createdAt is never claimed to be the older one (absent is not early); when
// neither has one, or they tie, the name decides so the answer never depends on the
// order the list arrived in.
function olderSession(a: Session, b: Session): boolean {
  const ca = a.createdAt || "";
  const cb = b.createdAt || "";
  if (ca !== cb) {
    if (!ca) return false;
    if (!cb) return true;
    return compareText(ca, cb) < 0;
  }
  return compareText(a.name, b.name) < 0;
}

// worktreeOwners maps a working-copy folder to the session that CREATED it: of the
// sessions running in that folder, the one created first.
//
// Why that is the right answer and not a guess: a worktree only ever comes into
// being as a side effect of a launch, so the first session ever to run in the folder
// is necessarily the one that made it. Sessions that start there later are
// colleagues, not owners.
//
// Why it cannot be read off the names: the folder is "<base>@wip-<slug>" and the
// branch "temp/<slug>" with a RANDOM slug (session_handlers.go's randSlug) unrelated
// to any session name, and the branch is renamed afterwards while the folder stays
// put. `dir` is the only link.
export function worktreeOwners(sessions: Session[]): Map<string, Session> {
  const owners = new Map<string, Session>();
  for (const s of sessions) {
    const f = sessionFolder(s);
    if (!f) continue;
    const cur = owners.get(f);
    if (!cur || olderSession(s, cur)) owners.set(f, s);
  }
  return owners;
}

/** Where a session sits in the spawn forest. */
export interface SessionLineage {
  /** Topmost ancestor of the originSession chain (the session itself when it has none). */
  root: string;
  /** How many sessions share that root, this one included. 1 = a family of one. */
  size: number;
}

// sessionLineages walks every session's originSession chain to its top and counts
// the families. Members of one family share a root, which is what lets the rail
// paint them the same colour wherever they are.
//
// ⚠️ The chain is a DAG by construction, but nothing on the wire enforces it — a
// corrupted meta can name a cycle, and a renderer that never returns is the worst
// way to fail. The walk carries a visited set and treats the point where the chain
// closes on itself as the root.
export function sessionLineages(sessions: Session[]): Map<string, SessionLineage> {
  const byName = new Map(sessions.map((s) => [s.name, s]));
  const rootOf = new Map<string, string>();
  const rootFor = (s: Session): string => {
    const cached = rootOf.get(s.name);
    if (cached) return cached;
    const path: string[] = [];
    const seen = new Set<string>();
    let cur: Session | undefined = s;
    let root = s.name;
    while (cur) {
      if (seen.has(cur.name)) break; // cycle — stop where it closes
      seen.add(cur.name);
      path.push(cur.name);
      const known = rootOf.get(cur.name);
      if (known) {
        root = known;
        break;
      }
      root = cur.name;
      // A parent that is gone (deleted or archived) ends the walk here — normal, not a fault.
      const next: Session | undefined = cur.originSession ? byName.get(cur.originSession) : undefined;
      if (!next) break;
      cur = next;
    }
    for (const n of path) rootOf.set(n, root);
    return root;
  };
  const roots = new Map<string, string>();
  const sizes = new Map<string, number>();
  for (const s of sessions) {
    const r = rootFor(s);
    roots.set(s.name, r);
    sizes.set(r, (sizes.get(r) || 0) + 1);
  }
  const out = new Map<string, SessionLineage>();
  for (const s of sessions) {
    const r = roots.get(s.name) || s.name;
    out.set(s.name, { root: r, size: sizes.get(r) || 1 });
  }
  return out;
}

// The lineage spine's colour ramp (--lineage-0…5, tokens.css, one step per theme).
//
// It is NOT a third session palette. The two that exist cannot serve: the kind hues
// say claude/codex/… and are already spoken for, and a session's own `color` is only
// ever filled in for SSM host bookmarks (session_handlers.go's Color field), so
// borrowing it would leave nearly every family colourless. The ramp is deliberately
// low-chroma so a 2px spine reads as "these belong together" without competing with
// the kind icon or the state chip.
const LINEAGE_RAMP = 6;

// The slot comes from the ROOT'S NAME, not from its position in the list: a family
// keeps its colour when an unrelated session is created or deleted, which is the
// whole point of recognising it. Two families can land on the same slot; the nesting
// is the primary cue, so a collision reads as today's rail rather than as a lie.
function rampSlot(name: string): number {
  let h = 2166136261;
  for (let i = 0; i < name.length; i++) {
    h ^= name.charCodeAt(i);
    h = Math.imul(h, 16777619);
  }
  return Math.abs(h) % LINEAGE_RAMP;
}

/** The CSS colour a family draws its spine in — "" when there is no family (nobody
 *  spawned this session and it spawned nobody). A line on every row would be noise;
 *  the colour has to mean something to be worth reading. */
export function lineageColor(lin: SessionLineage | undefined): string {
  if (!lin || lin.size < 2) return "";
  return `var(--lineage-${rampSlot(lin.root)})`;
}

// sessionLineages over the sessions store's array, memoised on the array identity
// (the store replaces it on every update). Session rows render in several sections
// and each needs only its own colour, so recomputing the whole forest per row is the
// thing to avoid.
const lineageMemo = new WeakMap<Session[], Map<string, SessionLineage>>();
export function lineageColorOf(sessions: Session[], name: string): string {
  let m = lineageMemo.get(sessions);
  if (!m) {
    m = sessionLineages(sessions);
    lineageMemo.set(sessions, m);
  }
  return lineageColor(m.get(name));
}

/** The two indexes the parent decision needs, built once per repoTree call. */
export interface LineageIndex {
  /** Working-copy folder → the session that created it (worktreeOwners). */
  owners: Map<string, Session>;
  /** Session name → session. */
  byName: Map<string, Session>;
}

export function lineageIndex(sessions: Session[]): LineageIndex {
  return { owners: worktreeOwners(sessions), byName: new Map(sessions.map((s) => [s.name, s])) };
}

// worktreeParentFolder — which working copy of `group` the worktree `folder` hangs
// under: the copy that the session that CREATED it was spawned from.
//
// Every early return below is a case where that copy does not resolve, and every one
// of them is an ordinary state of a live rail rather than a fault — so all of them
// fall back to the group's root, exactly where an orphaned worktree already sits.
// They are spelled out one per line because each is a rule someone will otherwise
// "simplify" away, and because a downstream safety net (breakLineageCycles) would
// quietly absorb two of them.
export function worktreeParentFolder(ix: LineageIndex, folder: string, rootFolder: string, group: Set<string>): string {
  const owner = ix.owners.get(folder);
  if (!owner) return rootFolder; // the owner was deleted or archived
  if (!owner.originSession) return rootFolder; // nobody raised it — it is a root of its own
  const parent = ix.byName.get(owner.originSession);
  if (!parent) return rootFolder; // the parent was deleted or archived
  const copy = sessionFolder(parent);
  if (copy === folder) return rootFolder; // the parent was resumed INSIDE the copy its child made
  // create_session names the repository to start in, so a child's worktree can belong
  // to a different base entirely. A tree cannot show that; the spine colour can. The
  // same line catches a parent that runs in no working copy at all (a shell in home):
  // "" is not a member of any group either.
  if (!group.has(copy)) return rootFolder;
  return copy; // the group's base, or a sibling worktree
}

/** One working copy in the rail's tree, with the copies nested under it. */
export interface RepoTreeNode {
  repo: Repo;
  children: RepoTreeNode[];
  /** CSS colour of this copy's lineage spine; "" = it belongs to no family. */
  spine: string;
}

// repoTree turns the flat repo list into the rail's forest: one root per group from
// groupedRepos (a base clone, or an orphaned worktree whose base is gone), with that
// base's worktrees NESTED by lineage instead of laid out flat.
//
// Where each worktree hangs, and the cases where that does not resolve, are
// worktreeParentFolder's business.
export function repoTree(repos: Repo[], sessions: Session[]): RepoTreeNode[] {
  const ix = lineageIndex(sessions);
  const lineages = sessionLineages(sessions);
  const spineOf = (folder: string): string => {
    const owner = ix.owners.get(folder);
    return owner ? lineageColor(lineages.get(owner.name)) : "";
  };
  return groupedRepos(repos).map((members) => {
    const nodes = new Map<string, RepoTreeNode>();
    for (const r of members) nodes.set(r.name, { repo: r, children: [], spine: spineOf(r.name) });
    const rootName = members[0].name;
    const worktrees = members.slice(1);
    const group = new Set(members.map((r) => r.name));
    const parentOf = new Map<string, string>();
    for (const w of worktrees) parentOf.set(w.name, worktreeParentFolder(ix, w.name, rootName, group));
    breakLineageCycles(parentOf, rootName);
    // members[1…] arrives createdAt-ascending from groupedRepos and is appended in
    // that order, so every level of the tree keeps that order: a name sort would be
    // effectively random (temp/<slug>), while chronological order leaves the existing
    // worktrees where they are and puts a new one at the end of its level.
    for (const w of worktrees) {
      const parent = nodes.get(parentOf.get(w.name) || rootName);
      const self = nodes.get(w.name);
      if (parent && self) parent.children.push(self);
    }
    return nodes.get(rootName) as RepoTreeNode;
  });
}

// ⚠️ parentOf is derived from session metadata, so it can describe a cycle (A's owner
// spawned from a session in B while B's owner spawned from one in A) even though the
// real lineage is a DAG. Rendering one would recurse until the tab dies, so every edge
// is walked to the root first and any edge that fails to arrive is cut back to the
// root. Cutting one edge breaks the cycle for everyone else in it, so a later pass over
// the same cycle terminates normally.
function breakLineageCycles(parentOf: Map<string, string>, rootName: string): void {
  for (const name of parentOf.keys()) {
    const seen = new Set<string>([name]);
    let cur = parentOf.get(name) || rootName;
    while (cur !== rootName) {
      if (seen.has(cur)) {
        parentOf.set(name, rootName);
        break;
      }
      seen.add(cur);
      const next = parentOf.get(cur);
      if (next === undefined) {
        parentOf.set(name, rootName); // dangling edge — treat like an unresolved parent
        break;
      }
      cur = next;
    }
  }
}

/** Nodes in a forest, the roots included — the rail's repo count under a working set. */
export function countRepoNodes(nodes: RepoTreeNode[]): number {
  return nodes.reduce((n, t) => n + 1 + countRepoNodes(t.children), 0);
}

// filterRepoTree prunes a forest to the nodes that match, keeping every ancestor of a
// match as its anchor — the same rule the flat layout used when a base with no match
// of its own stayed to carry a matching worktree, now applied at every level.
export function filterRepoTree(nodes: RepoTreeNode[], keep: (r: Repo) => boolean): RepoTreeNode[] {
  const out: RepoTreeNode[] = [];
  for (const n of nodes) {
    const children = filterRepoTree(n.children, keep);
    if (children.length > 0 || keep(n.repo)) out.push({ repo: n.repo, children, spine: n.spine });
  }
  return out;
}

// orphanSessions returns sessions that belong to no known working copy — a folder
// that isn't in the repo list (e.g. a shell in home, or a session whose repo was
// removed). These land in the rail's "other sessions" catch-all (pj.other_sessions).
export function orphanSessions(sessions: Session[], repos: Repo[]): Session[] {
  const names = new Set(repos.map((r) => r.name));
  return sessions
    .filter((s) => {
      const f = sessionFolder(s);
      return !f || !names.has(f);
    })
    .sort((a, b) => compareText(b.createdAt || "", a.createdAt || ""));
}
