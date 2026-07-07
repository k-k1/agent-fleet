// Sessions store (zustand): the canonical session list, polled every 4s and
// shared by the rail, pane headers and terminal attach-gating. Replaces the old
// God-context slice + its "bumpSessions" counter — callers just call refresh().
// Only publishes on an actual change (serialized compare) — an unconditional 4s
// repaint flickered the terminal cursor in the old console.
import { create } from "zustand";
import { api } from "../../core/api/client.ts";
import type { Session } from "../../types/session.ts";

interface SessionsStore {
  sessions: Session[];
  /** Global "open the New Session dialog" signal (old openNewSession): anything
   * (WS bar 新規, onboarding card) bumps this; SessionsSection — which owns the
   * modal — watches it and opens. App also watches to raise the mobile drawer
   * first (the modal mounts inside the rail; a CSS transform there would offset
   * its fixed positioning). */
  newSessionTick: number;
  openNewSession(): void;
  refresh(): Promise<void>;
  /** Resume/launch a stopped session (POST start). The caller re-attaches. */
  start(name: string): Promise<void>;
}

let ser = ""; // last published serialization (module-level: not render state)

export const useSessionsStore = create<SessionsStore>((set) => ({
  sessions: [],
  newSessionTick: 0,
  openNewSession: () => set((s) => ({ newSessionTick: s.newSessionTick + 1 })),

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

/** Poll every 4s while the tab is visible. Returns cleanup (StrictMode-safe). */
export function startSessionsPolling(): () => void {
  const load = () => {
    if (!document.hidden) void useSessionsStore.getState().refresh();
  };
  load();
  const t = setInterval(load, 4000);
  return () => clearInterval(t);
}
