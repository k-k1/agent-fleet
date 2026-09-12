// Compact, directional summary of a linked worktree relative to its parent.
// This intentionally differs from ahead/behind, which is always about origin.
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
