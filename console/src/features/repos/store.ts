// Repos store (zustand): working copies under ~/repos. Replaces the old
// reposKey bump counter — sections call refresh() directly.
import { create } from "zustand";
import { api } from "../../core/api/client.ts";

// A working copy from GET /api/repos.
export interface Repo {
  name: string;
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
  refresh(): Promise<void>;
}

export const useReposStore = create<ReposStore>((set) => ({
  repos: [],
  async refresh() {
    try {
      const d = await api("api/repos");
      set({ repos: d.repos || [] });
    } catch {
      set({ repos: [] });
    }
  },
}));

/** Cross-surface "open 作業を始める on this working copy" signal (起動導線
 * Ph2/Ph3): the はじめる hub's repo pick and the clone-only toast's このまま
 * はじめる both land here; StartHost renders the LaunchModal on it. */
interface LaunchTargetStore {
  target: Repo | null;
  open(r: Repo): void;
  clear(): void;
}

export const useLaunchTarget = create<LaunchTargetStore>((set) => ({
  target: null,
  open: (r) => set({ target: r }),
  clear: () => set({ target: null }),
}));

/** Poll every 60s while the tab is visible, so the origin-ahead badge (kept
 * fresh server-side by the Agent's auto-fetch) updates without a manual
 * refresh. Returns cleanup (StrictMode-safe). */
export function startReposPolling(): () => void {
  const load = () => {
    if (!document.hidden) void useReposStore.getState().refresh();
  };
  load();
  const t = setInterval(load, 60000);
  return () => clearInterval(t);
}
