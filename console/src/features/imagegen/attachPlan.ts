// attachPlan — the pure half of "start a studio" / "attach an agent": where the session runs
// (which working copy, whether a new worktree is cut and from what), the settings the dialog
// remembers per tenant, and the order the location picker lists working copies in.
//
// No React, no api.ts (it reads localStorage at module scope and the node test project cannot
// load it): the dialog and the tests both drive these functions directly.
import type { Repo } from "../repos/store.ts";
import { worktreeOptional, type StudioDriver } from "./studioSync.ts";

/** Why the chosen place cannot host the chosen agent at all. */
export type PlaceBlock = "needs_repo" | "no_worktree";

/** What the session will actually be started with, and what the dialog says about it. */
export interface PlacePlan {
  /** The directory `POST api/sessions` gets ("" = home). */
  dir: string;
  worktree: boolean;
  /** The branch the new worktree starts from; "" when there is no new worktree. */
  base: string;
  /** Whether the member can turn the worktree on or off here. */
  worktreeChoice: boolean;
  /** A worktree row whose agent needs a worktree: a new one is cut from the row's branch. */
  forcedFromWorktree: boolean;
  blocked?: PlaceBlock;
}

/** Worktree rows start in place (the member picked that checkout); base clones start a new one. */
export const defaultWorktree = (repo: Repo | null): boolean => !!repo && !repo.worktree && canCutWorktree(repo);

// svn has no worktrees at all, and an unborn git copy has no HEAD for `git worktree add`.
const canCutWorktree = (repo: Repo): boolean => repo.vcs !== "svn" && !repo.unborn;

/**
 * Where the session runs. `want` is the member's worktree checkbox, `base` their base-branch
 * pick (only read for a base clone).
 *
 * A new worktree is never cut FROM a worktree's folder: the Agent names it after the folder it
 * was cut from, so the result reads `repo@a@b` and sits under the wrong node. It is cut from the
 * parent clone instead, starting at the worktree's branch — the same commit, since linked
 * worktrees share their refs.
 */
export function planPlace(p: {
  repo: Repo | null;
  repos: readonly Repo[];
  kind: string;
  driver: StudioDriver;
  want: boolean;
  base?: string;
}): PlacePlan {
  const optional = worktreeOptional(p.kind, p.driver);
  const none = { worktree: false, base: "", worktreeChoice: false, forcedFromWorktree: false };
  const r = p.repo;
  if (!r) return { dir: "", ...none, ...(optional ? {} : { blocked: "needs_repo" as const }) };
  const dir = r.path || "";
  if (!canCutWorktree(r)) return { dir, ...none, ...(optional ? {} : { blocked: "no_worktree" as const }) };
  const worktree = optional ? p.want : true;
  if (r.worktree) {
    if (!worktree) return { dir, ...none, worktreeChoice: true };
    const parent = (r.parent && p.repos.find((x) => x.name === r.parent && x.path)) || null;
    return {
      dir: parent?.path || dir,
      worktree: true,
      base: r.branch || "",
      worktreeChoice: optional,
      forcedFromWorktree: !optional,
    };
  }
  return {
    dir,
    worktree,
    base: worktree ? p.base || r.branch || "" : "",
    worktreeChoice: optional,
    forcedFromWorktree: false,
  };
}

/**
 * The working copies in picker order: each base clone followed by its own worktrees, so a
 * worktree reads as belonging to its parent. A worktree whose parent is not listed goes last.
 */
export function orderedPlaces(repos: readonly Repo[]): { repo: Repo; nested: boolean }[] {
  const withPath = repos.filter((r) => r.path);
  const bases = withPath.filter((r) => !r.worktree || !r.parent || !withPath.some((b) => b.name === r.parent && !b.worktree));
  const out: { repo: Repo; nested: boolean }[] = [];
  for (const b of bases) {
    out.push({ repo: b, nested: false });
    if (b.worktree) continue;
    for (const w of withPath) if (w.worktree && w.parent === b.name) out.push({ repo: w, nested: true });
  }
  return out;
}

// --- the remembered settings -------------------------------------------------------------------

/** What the dialog restores next time: every choice it offers, per tenant. */
export interface AttachLast {
  driver: StudioDriver;
  kind: string;
  model: string;
  effort: string;
  /** Absent when the kind offers no permission choice. */
  skipPermissions?: boolean;
  /** The working copy's name; "" = home. */
  repo: string;
  worktree: boolean;
  imageProvider: string;
  imageModel: string;
}

export const attachLastKey = (tenant: string): string => `af.imagegen-attach-last.${tenant || "default"}`;

export function readAttachLast(tenant: string): AttachLast | null {
  try {
    const raw = localStorage.getItem(attachLastKey(tenant));
    if (!raw) return null;
    const v = JSON.parse(raw) as Partial<AttachLast>;
    if (!v || typeof v !== "object" || typeof v.kind !== "string" || !v.kind) return null;
    const s = (x: unknown): string => (typeof x === "string" ? x : "");
    return {
      driver: v.driver === "managed" ? "managed" : "tui",
      kind: v.kind,
      model: s(v.model),
      effort: s(v.effort),
      ...(typeof v.skipPermissions === "boolean" ? { skipPermissions: v.skipPermissions } : {}),
      repo: s(v.repo),
      worktree: v.worktree !== false,
      imageProvider: s(v.imageProvider),
      imageModel: s(v.imageModel),
    };
  } catch {
    return null;
  }
}

export function writeAttachLast(tenant: string, v: AttachLast): void {
  try {
    localStorage.setItem(attachLastKey(tenant), JSON.stringify(v));
  } catch {
    /* a blocked store only means the dialog opens unfolded next time */
  }
}

/**
 * Whether the remembered settings can still be used as they are, so the dialog may open folded
 * to its one-line summary. Any piece gone (the kind no longer offered for that execution method,
 * the working copy deleted) opens it unfolded instead: a summary must never show a choice the
 * launch would silently replace.
 */
export function lastUsable(
  last: AttachLast | null,
  p: { offered: (kind: string, driver: StudioDriver) => boolean; repos: readonly Repo[]; fixedRepo: boolean },
): last is AttachLast {
  if (!last) return false;
  if (!p.offered(last.kind, last.driver)) return false;
  if (!p.fixedRepo && last.repo && !p.repos.some((r) => r.name === last.repo && r.path)) return false;
  return true;
}
