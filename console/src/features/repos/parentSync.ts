// Compact, directional summary of a linked worktree relative to its parent: the
// parent branch's upstream (e.g. origin/develop) when it has one, else the parent's
// HEAD. This intentionally differs from ahead/behind, which is about the worktree's
// own upstream.
import { t } from "../../lib/i18n/index.ts";
import type { Repo } from "./store.ts";

type Integration = NonNullable<Repo["integration"]>;

/** Only this relation can advance this worktree to the parent's HEAD without a merge commit. */
export function canFastForwardFromParent(r: Repo): boolean {
  return r.worktree === true && r.integration?.relation === "contained";
}

/** The chip's tooltip: which branch it was compared against, and what the relation means.
 *  Shared with the sessions overview's card (ADR 0078), which shows the same chip beside the
 *  session's branch — the wording must not fork. */
export function parentSyncTitle(i: Integration): string {
  const target = i.targetBranch || t("repo.parent_head");
  switch (i.relation) {
    case "same":
      return t("repo.sync_title.same", { target });
    case "contained":
      return t("repo.sync_title.contained", { target, n: i.targetUnique });
    case "unmerged":
      return t("repo.sync_title.unmerged", { target, n: i.worktreeUnique });
    case "diverged":
      return t("repo.sync_title.diverged", { target, w: i.worktreeUnique, t: i.targetUnique });
    case "unknown":
      return t("repo.sync_title.unknown", { target });
  }
}

/** The upstream ref the parent fast-forward goes to, or "" when it targets the parent's
 *  HEAD — the menu item and its toasts name it so "parent" is not read as the parent clone. */
export function parentFFTarget(r: Repo): string {
  const i = r.integration;
  return i?.targetUpstream && i.targetBranch ? i.targetBranch : "";
}

export function parentFFMenuLabel(r: Repo): string {
  const target = parentFFTarget(r);
  return target ? t("repo.ff_upstream", { target }) : t("repo.ff_parent");
}

export function parentFFSuccessText(r: Repo): string {
  const target = parentFFTarget(r);
  return target ? t("rp.upstream_ff_success", { name: r.name, target }) : t("rp.parent_ff_success", { name: r.name });
}

export function parentFFFailedText(r: Repo, err: string): string {
  const target = parentFFTarget(r);
  return target ? t("rp.upstream_ff_failed", { target, err }) : t("rp.parent_ff_failed", { err });
}

export function parentSyncLabel(i: Integration): string {
  switch (i.relation) {
    case "same": return t("repo.sync.same");
    // The worktree is already in the parent's history, but the parent has moved
    // on. It can therefore advance from the parent without a merge commit.
    case "contained": return t("repo.sync.contained", { n: i.targetUnique });
    // The worktree has commits the parent does not have yet.
    case "unmerged": return t("repo.sync.unmerged", { n: i.worktreeUnique });
    case "diverged": return t("repo.sync.diverged", { a: i.worktreeUnique, b: i.targetUnique });
    case "unknown": return t("repo.sync.unknown");
  }
}
