// Shape of the /sessions/cleanup list, and the two-level tree the Cleanup modal draws
// from it: base repo → working copy → rows.
//
// The flat list is unreadable once a fleet has drifted — a dozen worktrees of the same
// repo read as a dozen unrelated rows. Grouping restores what they actually are: one
// repo, several working copies, and inside each working copy everything that goes away
// with it (its own worktree row, the sessions living in it, the branches left in the
// clone). That nesting is not cosmetic — delete_worktree prunes the sessions inside,
// so "what am I about to take with it" is exactly the group.
//
// The Agent supplies the grouping key: `repo` is the working-copy FOLDER basename
// (workspace/agent/session_cleanup.go). A worktree folder is "<repo>@<seg>" — the "@"
// is the only signal that separates a worktree from a plain clone, the same convention
// ArchivedModal's group headings use.
import { compareText } from "../../lib/intl.ts";

export interface CleanupCandidate {
  type: "session" | "worktree" | "branch";
  action?: "archive_session" | "delete_session" | "delete_worktree" | "delete_branch";
  id: string;
  display?: string;
  kind?: string;
  path?: string;
  repo?: string; // working-copy folder basename; "" = unknown (orphan pane)
  branch?: string;
  relation?: string;
  dirty?: boolean;
  ahead?: number;
  safety: "safe" | "review" | "keep";
  reason_key?: string;
  reason: string;
}

// One working copy: a linked worktree, or the clone itself (the base copy, isWorktree false).
export interface CleanupCopyGroup {
  key: string; // folder basename; "" = the catch-all for rows with no known working copy
  repo: string; // base repo name (before the "@")
  isWorktree: boolean;
  suffix: string; // "@wip-sw32vcm" for a worktree, "" for the clone
  branch: string; // the worktree's current branch, when a row reported one
  rows: CleanupCandidate[];
  safeCount: number; // actionable + safe — what "select all safe" would take here
}

export interface CleanupRepoGroup {
  repo: string; // "" = rows whose working copy is unknown
  copies: CleanupCopyGroup[];
  count: number;
  safeCount: number;
}

// Row order inside a working copy: the copy itself, then the branches left behind, then
// the sessions in it — coarse to fine, which is also the order a cleanup is done in.
const TYPE_ORDER: Record<CleanupCandidate["type"], number> = { worktree: 0, branch: 1, session: 2 };
const SAFETY_ORDER: Record<CleanupCandidate["safety"], number> = { safe: 0, review: 1, keep: 2 };

const baseRepo = (folder: string) => {
  const at = folder.indexOf("@");
  return at > 0 ? folder.slice(0, at) : folder;
};

// The row's own label — what the target column shows. A worktree row inside its own
// group needs none: the heading already names the folder and branch.
export function rowLabel(c: CleanupCandidate): string {
  if (c.type === "worktree") return "";
  if (c.type === "branch") return c.branch || c.id;
  return c.display || c.id;
}

const isSelectable = (c: CleanupCandidate) => !!c.action && c.safety !== "keep";

export function groupCandidates(items: CleanupCandidate[]): CleanupRepoGroup[] {
  const byCopy = new Map<string, CleanupCandidate[]>();
  for (const c of items) {
    const key = c.repo || "";
    const rows = byCopy.get(key);
    if (rows) rows.push(c);
    else byCopy.set(key, [c]);
  }

  const copies: CleanupCopyGroup[] = [...byCopy.entries()].map(([key, rows]) => {
    rows.sort(
      (a, b) =>
        TYPE_ORDER[a.type] - TYPE_ORDER[b.type] ||
        SAFETY_ORDER[a.safety] - SAFETY_ORDER[b.safety] ||
        compareText(rowLabel(a), rowLabel(b)),
    );
    const at = key.indexOf("@");
    return {
      key,
      repo: baseRepo(key),
      isWorktree: at > 0,
      suffix: at > 0 ? key.slice(at) : "",
      // The worktree row carries the working copy's live branch; fall back to whatever
      // other row reported one (a session records the branch it started on).
      branch: rows.find((r) => r.type === "worktree" && r.branch)?.branch || rows.find((r) => r.branch)?.branch || "",
      rows,
      safeCount: rows.filter((r) => isSelectable(r) && r.safety === "safe").length,
    };
  });

  const byRepo = new Map<string, CleanupCopyGroup[]>();
  for (const g of copies) {
    const list = byRepo.get(g.repo);
    if (list) list.push(g);
    else byRepo.set(g.repo, [g]);
  }

  const repos: CleanupRepoGroup[] = [...byRepo.entries()].map(([repo, list]) => {
    // Worktrees first (name asc), the clone itself last: its rows are the leftovers
    // (merged branches) rather than the working copies the user came to clear.
    list.sort((a, b) => (a.isWorktree === b.isWorktree ? compareText(a.key, b.key) : a.isWorktree ? -1 : 1));
    return {
      repo,
      copies: list,
      count: list.reduce((n, g) => n + g.rows.length, 0),
      safeCount: list.reduce((n, g) => n + g.safeCount, 0),
    };
  });
  // Unknown working copy ("") sorts last — it is the "everything else" bucket.
  repos.sort((a, b) => (!a.repo ? 1 : !b.repo ? -1 : compareText(a.repo, b.repo)));
  return repos;
}
