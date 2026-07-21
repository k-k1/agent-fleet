// Sessions store (zustand): the canonical session list, polled every 4s and
// shared by the rail, pane headers and terminal attach-gating. Replaces the old
// God-context slice + its "bumpSessions" counter — callers just call refresh().
// Only publishes on an actual change (serialized compare) — an unconditional 4s
// repaint flickered the terminal cursor in the old console.
import { create } from "zustand";
import { api } from "../../core/api/client.ts";
import { useWorkspaceStore, wsRunning } from "../../core/store/workspace.ts";
import type { Session } from "../../types/session.ts";

interface SessionsStore {
  sessions: Session[];
  /** Global "open the はじめる hub" signal (起動導線 Ph2): WS bar はじめる /
   * onboarding bump this; StartHost — which owns the StartModal — watches it.
   * (The old openNewSession/newSessionTick went with the NewSessionModal, Ph3.) */
  startTick: number;
  openStart(): void;
  refresh(): Promise<void>;
  /** Resume/launch a stopped session (POST start). The caller re-attaches. */
  start(name: string): Promise<void>;
}

let ser = ""; // last published serialization (module-level: not render state)

export const useSessionsStore = create<SessionsStore>((set) => ({
  sessions: [],
  startTick: 0,
  openStart: () => set((s) => ({ startTick: s.startTick + 1 })),

  async refresh() {
    try {
      const d = await api("api/sessions");
      const list: Session[] = d.sessions || [];
      const s = JSON.stringify(list);
      if (s !== ser) {
        ser = s;
        set({ sessions: list });
      }
    } catch {
      if (ser !== "[]") {
        ser = "[]";
        set({ sessions: [] });
      }
    }
  },

  async start(name: string) {
    try {
      await api(`api/sessions/${encodeURIComponent(name)}/start`, { method: "POST" });
    } catch {
      /* attach still tries; a failure surfaces as [disconnected] */
    }
    await useSessionsStore.getState().refresh();
  },
}));

/** Poll every 4s while the tab is visible AND the workspace is running — a
 * stopped/booting agent only 502s, so polling it is pure waste (docs/35 §35.9-9).
 * The running edge is picked up on the next tick (and wireWorkspaceRefresh fires
 * an immediate refetch). Returns cleanup (StrictMode-safe). */
export function startSessionsPolling(): () => void {
  const load = () => {
    if (document.hidden || !wsRunning(useWorkspaceStore.getState().state)) return;
    void useSessionsStore.getState().refresh();
  };
  load();
  const t = setInterval(load, 4000);
  return () => clearInterval(t);
}
