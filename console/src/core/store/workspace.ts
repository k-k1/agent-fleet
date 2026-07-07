// Workspace lifecycle store (zustand). Replaces the workspace slice of the old
// God-context: the per-membership container's state plus start/stop.
//
// state values come from the CP ("running" / "stopped" / …); "unknown" = fetch
// failed, and a trailing "…" marks an optimistic in-flight transition (the old
// convention — pollers and buttons treat it as busy and keep their hands off).
import { create } from "zustand";
import { api } from "../api/client.ts";

interface WorkspaceStore {
  state: string;
  refresh(): Promise<void>;
  start(): Promise<void>;
  stop(): Promise<void>;
}

export const useWorkspaceStore = create<WorkspaceStore>((set, get) => ({
  state: "…",

  async refresh() {
    try {
      const w = await api("api/workspace");
      set({ state: w.state || "unknown" });
    } catch {
      set({ state: "unknown" });
    }
  },

  async start() {
    set({ state: "starting…" });
    await api("api/workspace/start", { method: "POST" });
    await get().refresh();
  },

  async stop() {
    // Optimistic transition so the toggle goes inert (busy = trailing "…") and the
    // poll skips mid-stop — otherwise a second click re-issues the stop / a poll
    // clobbers the state during the multi-second docker stop.
    set({ state: "stopping…" });
    await api("api/workspace/stop", { method: "POST" });
    await get().refresh();
  },
}));

/** True while a start/stop transition is in flight (or state not yet fetched). */
export const wsBusy = (state: string): boolean => state.endsWith("…");

// Auto-sync every 4s so an externally-changed workspace (admin stop, OOM death,
// crash) reflects on its own. Skipped while hidden or mid-transition. Returns the
// cleanup, so the caller (App boot effect) is StrictMode-safe.
export function startWorkspacePolling(): () => void {
  const t = setInterval(() => {
    if (document.hidden || wsBusy(useWorkspaceStore.getState().state)) return;
    useWorkspaceStore.getState().refresh();
  }, 4000);
  return () => clearInterval(t);
}
