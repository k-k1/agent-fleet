// Repos store (zustand): working copies under ~/repos. Replaces the old
// reposKey bump counter — sections call refresh() directly.
import { create } from "zustand";
import { api, isTransientErr } from "../../core/api/client.ts";
import { useWorkspaceStore, wsRunning } from "../../core/store/workspace.ts";

// A working copy from GET /api/repos.
export interface Repo {
  name: string;
  workingCopyId?: string;
  path?: string;
  branch?: string;
  dirty?: boolean;
  ahead?: number;
  behind?: number;
  provider?: string; // origin host slug: github/bitbucket/gitlab, or a bare host
  remote?: string; // origin host (tooltip)
  worktree?: boolean; // linked git worktree (not a standalone clone)
  parent?: string; // for a worktree, the parent working copy's folder name
  createdAt?: string; // for a worktree, its creation time (RFC3339); orders worktrees under a base
  /** 削除ロック（docs/45）: true の間、削除は force 付きでも 403 で拒否され、
   * 空になった worktree の自動 prune も対象外になる。 */
  locked?: boolean;

  /** Working-copy kind (docs/41): "git" (default/omitted) or "svn". SVN copies are
   * flat — no branch/ahead/behind/worktree — so the Console gates git-only actions
   * on it and shows the revision/URL below instead. */
  vcs?: "git" | "svn";
  revision?: string; // SVN: current working-copy revision
  url?: string; // SVN: repository URL of the working copy

  /** Commit relationship to the parent working copy's current HEAD. This is
   * independent of ahead/behind above, which are relative to the upstream. */
  integration?: {
    targetBranch?: string;
    targetUnique: number;
    worktreeUnique: number;
    relation: "same" | "contained" | "unmerged" | "diverged" | "unknown";
  };
}

interface ReposStore {
  repos: Repo[];
  /** Loads GET /api/repos. Resolves false when the load failed TRANSIENTLY — the CP's
   * plain-text 502 while the workspace agent is still booting, or a dropped fetch — and
   * leaves `repos` untouched: a 502 carries no repos, which is NOT the same as "there are
   * no repos", and committing its empty body is what used to wedge the rail on
   * リポジトリがありません. Resolves true once a real result is committed.
   * A *stopped* workspace answers with the very same 502 (control-plane/proxy.go), so only
   * a caller that knows the workspace state can tell "booting → retry" from "stopped →
   * show empty"; see ProjectTree's useRetryLoad + clear(). */
  refresh(): Promise<boolean>;
  /** Reflect a successful deletion-lock toggle before the next list refresh. */
  setLocked(name: string, locked: boolean): void;
  /** Settle to empty. For a caller that knows the repos really are gone/unreachable
   * (a stopped workspace), since refresh() alone never blanks the list. */
  clear(): void;
}

export const useReposStore = create<ReposStore>((set) => ({
  repos: [],
  async refresh() {
    let d: { repos?: Repo[] };
    try {
      d = await api("api/repos");
    } catch {
      return false; // network drop — transient; keep what the rail has.
    }
    if (isTransientErr(d)) return false;
    set({ repos: d.repos || [] });
    return true;
  },
  setLocked(name, locked) {
    set((s) => ({ repos: s.repos.map((r) => (r.name === name ? { ...r, locked } : r)) }));
  },
  clear: () => set({ repos: [] }),
}));

/** Reveal-a-repo-in-the-rail signal (mirrors useFilesStore.revealInFiles): the
 * command palette's repo row lands here so the project tree expands the working
 * copy's node (and its base, for a worktree), scrolls it into view, and focuses
 * its row. `n` bumps per request so identical repeat reveals still fire. */
interface RepoRevealStore {
  name: string | null;
  n: number;
  reveal(name: string): void;
}

export const useRepoReveal = create<RepoRevealStore>((set) => ({
  name: null,
  n: 0,
  reveal: (name) => set((s) => ({ name, n: s.n + 1 })),
}));

/** Cross-surface "open 作業を始める on this working copy" signal (起動導線
 * Ph2/Ph3): the はじめる hub's repo pick and the clone-only toast's このまま
 * はじめる both land here; StartHost renders the LaunchModal on it. */
interface LaunchTargetStore {
  target: Repo | null;
  /** Preselect this EXISTING branch in the launch dialog instead of the usual
   * new-branch flow ("" = new branch). Set by the SCM view's「このブランチで作業を
   * 始める」, which knows the branch before the dialog exists. */
  existingBranch: string;
  open(r: Repo, existingBranch?: string): void;
  clear(): void;
}

export const useLaunchTarget = create<LaunchTargetStore>((set) => ({
  target: null,
  existingBranch: "",
  open: (r, existingBranch = "") => set({ target: r, existingBranch }),
  clear: () => set({ target: null, existingBranch: "" }),
}));

/** A first-prompt seed for the next launch (docs/21 UI刷新): the memo send modal's
 * "新規セッションを起動" stashes the composed memo text here, then opens the launch hub.
 * LaunchModal reads it once to prefill its prompt field, then it's cleared. */
interface LaunchSeedStore {
	prompt: string;
	title: string;
	/** Session whose handoff proposal seeded this launch ("" = not a handoff). The
	 *  dialog's success path badges that proposal 起動済み; a cancelled dialog leaves it
	 *  untouched, which is why the mark can't happen when the seed is set. */
	handoffSession: string;
	/** Which of that session's (possibly several) outstanding proposals this is — a
	 *  session may propose more than one handoff in a turn, so the session name alone
	 *  no longer identifies the card to badge. */
	handoffId: string;
	/** メンバーから受け取った引き継ぎ（docs/77）の offer id ("" = 該当なし)。起動が成功した
	 *  時点で受諾を申告するために持つ —— キャンセルされた起動で受諾済みにしてはいけないので、
	 *  handoffId と同じく「種を置く時点」では申告できない。 */
	handoffOfferId: string;
	/** 作業項目（docs/80）から起動したときの相手。起動が**成功したあと**に台帳へ
	 *  1 行入れるために持つ —— handoffOfferId と同じ理由で、種を置く時点では
	 *  「着手した」と言ってはいけない（キャンセルされうる）。branch は
	 *  LaunchModal の新規ブランチ欄に入る提案値。 */
	workItem: { provider: string; key: string; branch: string } | null;
	set(p: string, title?: string, handoffSession?: string, handoffId?: string, handoffOfferId?: string, workItem?: { provider: string; key: string; branch: string } | null): void;
  clear(): void;
}

export const useLaunchSeed = create<LaunchSeedStore>((set) => ({
	prompt: "",
	title: "",
	handoffSession: "",
	handoffId: "",
	handoffOfferId: "",
	workItem: null,
	set: (prompt, title = "", handoffSession = "", handoffId = "", handoffOfferId = "", workItem = null) =>
		set({ prompt, title, handoffSession, handoffId, handoffOfferId, workItem }),
	clear: () => set({ prompt: "", title: "", handoffSession: "", handoffId: "", handoffOfferId: "", workItem: null }),
}));

/** Poll every 60s while the tab is visible AND the workspace is running, so the
 * origin-ahead badge (kept fresh server-side by the Agent's auto-fetch) updates
 * without a manual refresh. Skipped while stopped/booting — the agent proxy only
 * 502s then (docs/35 §35.9-9). Returns cleanup (StrictMode-safe). */
export function startReposPolling(): () => void {
  const load = () => {
    if (document.hidden || !wsRunning(useWorkspaceStore.getState().state)) return;
    void useReposStore.getState().refresh();
  };
  load();
  const t = setInterval(load, 60000);
  return () => clearInterval(t);
}
