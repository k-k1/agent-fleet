// Compact, directional summary of a linked worktree relative to its parent.
// This intentionally differs from ahead/behind, which is always about origin.
import { t } from "../../lib/i18n/index.ts";
import type { Repo } from "./store.ts";

type Integration = NonNullable<Repo["integration"]>;

export function parentSyncLabel(i: Integration): string {
  switch (i.relation) {
    case "same": return t("repo.sync.same");
    // The worktree is already in the parent's history, but the parent has moved
    // on. Show that lag instead of the ambiguous old "merged" label.
    case "contained": return t("repo.sync.contained", { n: i.targetUnique });
    // The parent is an ancestor of the worktree, so advancing the parent is a
    // safe fast-forward in this direction.
    case "unmerged": return t("repo.sync.unmerged", { n: i.worktreeUnique });
    case "diverged": return t("repo.sync.diverged", { a: i.worktreeUnique, b: i.targetUnique });
    case "unknown": return t("repo.sync.unknown");
  }
}
