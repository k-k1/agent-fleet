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
