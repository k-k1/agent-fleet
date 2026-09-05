// Working sets (docs/log/52 + ADR 0036) — named groups of { working
// copies, assistant conversations, repo-less sessions } that scope what the
// left rail shows. This module owns the vocabulary: the pure membership
// predicates plus the settings-backed mutations, shared by the rail sections,
// the row menus and the rail-top switcher.
//
// Membership rules (docs/log/52 §1):
// - a base clone belongs by its FOLDER NAME; its worktrees follow the base
//   (worktrees are never assigned individually);
// - a session inherits its working copy's membership (sessionFolder → base);
//   repo-less sessions (shell/ssm, or one whose repo is gone) are assigned
//   directly by session name;
// - a conversation is assigned explicitly by conversation id.
// Dangling references (deleted repo / conversation) are harmless — predicates
// simply stop matching — and are NOT auto-pruned: at read time "not loaded yet"
// and "deleted" are indistinguishable (docs/log/52 §2).
//
// This module is PURE (unit-testable under the node vitest project) — the
// settings-backed selection/mutations live in workingSetsStore.ts.
import { sessionFolder } from "./project.ts";
import type { Session } from "../types/session.ts";
import type { Repo } from "../features/repos/store.ts";

export interface WorkingSet {
  /** Immutable id ("w" + 6 chars), minted at creation — renames don't move members. */
  id: string;
  name: string;
  /** Base-clone folder names (~/repos/<name>). Worktrees follow their base. */
  repos: string[];
  /** Conversation ids (UUID). */
  convs: string[];
  /** Session names ("s…") — repo-less sessions only; others inherit via repos. */
  sessions: string[];
  /** CP schedule ids — direct assignment only; most schedules DERIVE membership
   * from their repo / owner conversation / reuse target (scheduleInSet). */
  schedules: string[];
}

export type WorkingSetField = "repos" | "convs" | "sessions" | "schedules";

const strings = (v: unknown): string[] =>
  Array.isArray(v) ? v.filter((x): x is string => typeof x === "string") : [];

/** Fold whatever the server/localStorage holds into well-formed sets — the
 * hiddenModels-style fail-safe: a broken entry drops, never throws. */
export function normalizeWorkingSets(v: unknown): WorkingSet[] {
  if (!Array.isArray(v)) return [];
  const out: WorkingSet[] = [];
  for (const e of v) {
    if (!e || typeof e !== "object") continue;
    const o = e as Record<string, unknown>;
    if (typeof o.id !== "string" || !o.id || typeof o.name !== "string") continue;
    out.push({
      id: o.id,
      name: o.name,
      repos: strings(o.repos),
      convs: strings(o.convs),
      sessions: strings(o.sessions),
      schedules: strings(o.schedules),
    });
  }
  return out;
}

// Same alphabet/shape family as session ("s…") and conversation ("a…") slugs.
const ID_ALPHABET = "abcdefghijklmnopqrstuvwxyz234567";
export function newWorkingSetId(): string {
  let id = "w";
  const buf = new Uint8Array(6);
  crypto.getRandomValues(buf);
  for (const b of buf) id += ID_ALPHABET[b % ID_ALPHABET.length];
  return id;
}

/** The base-clone part of a working-copy folder name — a worktree folder is
 * "<base>@<slug>" (git.go), so membership can resolve even after the base repo
 * record (or the repo list itself) is gone. */
export function folderBase(folder: string): string {
  const at = folder.indexOf("@");
  return at > 0 ? folder.slice(0, at) : folder;
}

/** The folder name of a working copy given either a bare folder name or an
 * ABSOLUTE workspace path ("/home/dev/repos/<folder>"). A set stores folder
 * names, but the CP stores a schedule's launch target as the agent path it was
 * given, so the two only meet after this reduction — the same tail-segment step
 * sessionFolder does for a session's dir. */
export function folderNameOf(pathOrName: string): string {
  return pathOrName.split("/").filter(Boolean).pop() || "";
}

/** The folder name a working copy belongs to a set by: a base clone by its own
 * name, a worktree by its parent's (falling back to its folder's "<base>@" prefix
 * when the parent record is gone). */
export function repoBaseName(r: Pick<Repo, "name" | "worktree" | "parent">): string {
  return r.worktree ? r.parent || folderBase(r.name) : r.name;
}

export function repoInSet(set: WorkingSet, r: Pick<Repo, "name" | "worktree" | "parent">): boolean {
  return set.repos.includes(repoBaseName(r));
}

/** A session is in the set directly (repo-less assignment) or by inheriting its
 * working copy's membership — resolved from the folder name alone, so this also
 * works while the repo list is unavailable (stopped WS) or the repo was deleted. */
export function sessionInSet(set: WorkingSet, s: Session): boolean {
  if (set.sessions.includes(s.name)) return true;
  const folder = sessionFolder(s);
  return !!folder && set.repos.includes(folderBase(folder));
}

export function convInSet(set: WorkingSet, convId: string): boolean {
  return set.convs.includes(convId);
}

// --- Schedules (docs/log/52 — CP-persisted, so membership is derived where the
// schedule's own fields give it an unambiguous home, with direct assignment by
// schedule id as the fallback for the rest). The DTO fields involved:
// repo = the working copy a session_mode=new fire launches in, held as the
//   ABSOLUTE agent path (docs/log/38 P2 passes it to create_session as "dir"
//   verbatim, and the operator sources it from list_repos' path) — NOT a folder
//   name, so it needs folderNameOf before it can meet a set's repos;
// worktree = the same thing as a folder name, but no writer sets it today (the
//   operator's create/update tools have no such argument), so repo carries the
//   worktree case too — as its own absolute path;
// owner_conv = the operator conversation that created it (UUID);
// reuse_target = a conversation ("a…" slug or UUID, assistant fires) or a
// session name (session_mode=reuse).

/** The schedule fields membership derivation reads (subset of ScheduleDTO). */
export interface ScheduleLike {
  id: string;
  repo?: string;
  worktree?: string;
  owner_conv?: string;
  reuse_target?: string;
}

/** Lookups only the caller's stores can answer; both optional — a missing
 * resolver just skips that derivation path (fail-safe, never throws). */
export interface ScheduleSetContext {
  /** Conversation slug ("a…") → conversation id (UUID). */
  convIdBySlug?: (slug: string) => string | undefined;
  /** Session name → its working-copy folder (sessionFolder), "" / undefined = none. */
  folderOfSession?: (name: string) => string | undefined;
}

const CONV_SLUG_RE = /^a[a-z2-7]{6}$/;

export function scheduleInSet(set: WorkingSet, s: ScheduleLike, ctx: ScheduleSetContext = {}): boolean {
  if (set.schedules.includes(s.id)) return true;
  // Launch target: the worktree folder when set, else the repo the fire launches
  // in — which the CP holds as an absolute path, so reduce to the folder name
  // first and then to its base clone. A worktree target ("<base>@<slug>", the
  // usual case since the operator picks a worktree's own list_repos path) thus
  // inherits its base repo's membership, exactly as sessionInSet does.
  const folder = folderNameOf(s.worktree || s.repo || "");
  if (folder && set.repos.includes(folderBase(folder))) return true;
  // The conversation that authored it — a schedule created from a group's
  // operator chat follows that chat (the schedules' analog of auto-add: there is
  // no Console creation seam to hook, the operator creates them via MCP).
  if (s.owner_conv && set.convs.includes(s.owner_conv)) return true;
  const tgt = (s.reuse_target || "").trim();
  if (tgt) {
    if (CONV_SLUG_RE.test(tgt)) {
      const id = ctx.convIdBySlug?.(tgt);
      if (id && set.convs.includes(id)) return true;
    } else if (set.convs.includes(tgt)) {
      return true; // assistant fires may carry the conversation UUID directly
    }
    if (set.sessions.includes(tgt)) return true;
    const f = ctx.folderOfSession?.(tgt);
    if (f && set.repos.includes(folderBase(f))) return true;
  }
  return false;
}
