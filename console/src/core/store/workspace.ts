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
  /** Tear the container down and start fresh from the current image. Logins +
   * connections persist; cloned repos and running sessions are wiped — the caller
   * guards this behind a warning dialog (設定 > 環境) and resets the layout first.
   * Returns the error message on failure (the caller toasts). */
  recreate(): Promise<string | null>;
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

  async recreate() {
    set({ state: "recreating…" });
    let err: string | null = null;
    try {
      const res = await api("api/workspace/recreate", { method: "POST" });
      if (res && res.error) err = res.error.message || String(res.error);
    } catch {
      err = "作り直しに失敗しました";
    }
    await get().refresh();
    return err;
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
