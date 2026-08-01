// Working sets (作業グループ, docs/52 + ADR 0036) — named groups of { working
// copies, assistant conversations, repo-less sessions } that scope what the
// left rail shows. This module owns the vocabulary: the pure membership
// predicates plus the settings-backed mutations, shared by the rail sections,
// the row menus and the rail-top switcher.
//
// Membership rules (docs/52 §1):
// - a base clone belongs by its FOLDER NAME; its worktrees follow the base
//   (worktrees are never assigned individually);
// - a session inherits its working copy's membership (sessionFolder → base);
//   repo-less sessions (shell/ssm, or one whose repo is gone) are assigned
//   directly by session name;
// - a conversation is assigned explicitly by conversation id.
// Dangling references (deleted repo / conversation) are harmless — predicates
// simply stop matching — and are NOT auto-pruned: at read time "not loaded yet"
// and "deleted" are indistinguishable (docs/52 §2).
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
}

export type WorkingSetField = "repos" | "convs" | "sessions";

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
    out.push({ id: o.id, name: o.name, repos: strings(o.repos), convs: strings(o.convs), sessions: strings(o.sessions) });
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
