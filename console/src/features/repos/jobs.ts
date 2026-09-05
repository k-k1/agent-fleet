// Repository import jobs (docs/log/78). `git clone` / `svn checkout` run as background
// jobs on the Agent, and the Console watches their ACTUAL progress through
// GET /api/repo-jobs. Waiting on the POST response alone meant that when no response came
// back (the upstream proxy gave up at 60 seconds) a folder on disk was read as success, so
// a still-running working copy was listed as imported.
//
// Properties this layer holds:
//   - Closing the tab does not lose a job: the progress lives on the server, so the same row
//     shows up after a reload and in another tab.
//   - A settled job stays until it is acknowledged (failed, interrupted). Dropping it before
//     the outcome is seen puts us back at "it failed silently".
//   - Polling speeds up only while something runs (2s); otherwise 60s, like the repo list.
import { create } from "zustand";
import { api, isTransientErr, raw } from "../../core/api/client.ts";
import { useWorkspaceStore, wsRunning } from "../../core/store/workspace.ts";
import { useReposStore } from "./store.ts";

export type RepoJobState = "running" | "done" | "failed" | "canceled" | "interrupted";

export interface RepoJob {
  id: string;
  kind: "git" | "svn";
  name: string;
  path?: string;
  url?: string;
  state: RepoJobState;
  /** Last output line seen (svn's "A path" / git's "Receiving objects: …"). */
  progress?: string;
  /** Lines fetched. Neither svn nor git announces a total up front, so this cannot be a
   *  percentage. */
  items?: number;
  error?: string;
  /** Failed, but the working copy survives (for svn, refresh resumes from where it stopped). */
  kept?: boolean;
  startedAt: string;
  endedAt?: string;
}

export const isRepoJobRunning = (j: RepoJob) => j.state === "running";

const FAST_POLL_MS = 2000;
const IDLE_POLL_MS = 60000;

interface RepoJobsStore {
  jobs: RepoJob[];
  /** Re-fetch the list. A transient failure (the 502 right after a start) keeps the previous
   *  contents. */
  refresh(): Promise<RepoJob[]>;
  /** Cancel while running, acknowledge once settled (both DELETE /api/repo-jobs/{id}). */
  remove(id: string): Promise<void>;
  /** Wait until id is no longer running. null when the job is gone. */
  wait(id: string): Promise<RepoJob | null>;
}

// Collapse concurrent refreshes into one: during an import both the display poller and
// wait() come looking, and without this the same GET goes out twice.
let inflight: Promise<RepoJob[]> | null = null;

export const useRepoJobsStore = create<RepoJobsStore>((set, get) => ({
  jobs: [],
  refresh() {
    if (inflight) return inflight;
    inflight = (async () => {
      let d: { jobs?: RepoJob[] };
      try {
        d = await api("api/repo-jobs");
      } catch {
        return get().jobs; // network drop — keep what we have
      } finally {
        inflight = null;
      }
      if (isTransientErr(d)) return get().jobs;
      const jobs = Array.isArray(d.jobs) ? d.jobs : [];
      const before = get().jobs;
      set({ jobs });
      // The moment a job settles, its folder stops being "importing" and becomes a real
      // working copy (the Agent does not list running ones under GET /repos). Re-fetch the
      // list so the row appears.
      const settled = before.some((b) => isRepoJobRunning(b) && !jobs.some((j) => j.id === b.id && isRepoJobRunning(j)));
      if (settled) void useReposStore.getState().refresh();
      return jobs;
    })();
    return inflight;
  },
  async remove(id) {
    await raw(`api/repo-jobs/${encodeURIComponent(id)}`, { method: "DELETE" });
    set({ jobs: get().jobs.filter((j) => j.id !== id) });
    await get().refresh();
  },
  async wait(id) {
    for (;;) {
      const jobs = await get().refresh();
      const j = jobs.find((x) => x.id === id);
      if (!j) return null; // acknowledged in another tab / forgotten by the Agent
      if (!isRepoJobRunning(j)) return j;
      await new Promise((r) => setTimeout(r, FAST_POLL_MS));
    }
  },
}));

/** Poll every 2s while a job runs, every 60s otherwise. Returns a stop function (for StrictMode). */
export function startRepoJobsPolling(): () => void {
  let stopped = false;
  let timer: ReturnType<typeof setTimeout> | undefined;
  const tick = async () => {
    if (!document.hidden && wsRunning(useWorkspaceStore.getState().state)) {
      await useRepoJobsStore.getState().refresh();
    }
    if (stopped) return;
    const fast = useRepoJobsStore.getState().jobs.some(isRepoJobRunning);
    timer = setTimeout(() => void tick(), fast ? FAST_POLL_MS : IDLE_POLL_MS);
  };
  void tick();
  return () => {
    stopped = true;
    if (timer) clearTimeout(timer);
  };
}
