// Sessions store (zustand): the canonical session list, polled every 4s and
// shared by the rail, pane headers and terminal attach-gating. Replaces the old
// God-context slice + its "bumpSessions" counter — callers just call refresh().
// Only publishes on an actual change (serialized compare) — an unconditional 4s
// repaint flickered the terminal cursor in the old console.
import { create } from "zustand";
import { api } from "../../core/api/client.ts";
import { pushHealthy, pushStamp } from "../../core/push/events.ts";
import { useWorkspaceStore, wsRunning } from "../../core/store/workspace.ts";
import { toast } from "../../ui/toast.ts";
import { t as tr } from "../../lib/i18n/index.ts";
import type { Session } from "../../types/session.ts";

interface SessionsStore {
  sessions: Session[];
  /** Global "open the はじめる hub" signal (起動導線 Ph2): WS bar はじめる /
   * onboarding bump this; StartHost — which owns the StartModal — watches it.
   * (The old openNewSession/newSessionTick went with the NewSessionModal, Ph3.) */
  startTick: number;
  openStart(): void;
  refresh(): Promise<void>;
  /** Publish a session list (poll result or pushed api/events frame) — only on
   * an actual change (serialized compare, see the header note). */
  applyList(list: Session[]): void;
  /** Reflect a successful deletion-lock toggle before the next list refresh. */
  setLocked(name: string, locked: boolean): void;
  /** Resume/launch a stopped session (POST start). Resolves true when the backend
   * accepted the resume; false (with a toast already shown) when it did not, so the
   * caller can leave its 再開 affordance armed instead of waiting forever. */
  start(name: string): Promise<boolean>;
}

let ser = ""; // last published serialization (module-level: not render state)

export const useSessionsStore = create<SessionsStore>((set, get) => ({
  sessions: [],
  startTick: 0,
  openStart: () => set((s) => ({ startTick: s.startTick + 1 })),

  applyList(list: Session[]) {
    const s = JSON.stringify(list);
    if (s !== ser) {
      ser = s;
      set({ sessions: list });
    }
  },

  setLocked(name: string, locked: boolean) {
    const list = get().sessions.map((s) => (s.name === name ? { ...s, locked } : s));
    ser = JSON.stringify(list);
    set({ sessions: list });
  },

  async refresh() {
    const stamp = pushStamp("sessions");
    try {
      const d = await api("api/sessions");
      // A pushed frame that arrived while this fetch was in flight is at least
      // as fresh — a slow (mobile) response must not clobber it and stick.
      if (pushStamp("sessions") !== stamp) return;
      get().applyList(d.sessions || []);
    } catch {
      // KEEP the last known list. Publishing [] here wiped every row on any transient
      // failure — most reliably in the 502 window while the workspace agent comes up
      // after a restart — and the rows are what every resume affordance is gated on
      // (a pane resolves its session by name out of this list; not finding it hides
      // the 再開 button entirely). A stale row is recoverable on the next tick; a
      // vanished one is a dead end the user can only escape by reloading.
    }
  },

  async start(name: string) {
    let ok = true;
    try {
      await api(`api/sessions/${encodeURIComponent(name)}/start`, { method: "POST" });
    } catch {
      // Never silent. Swallowing this left the caller to "resume" into a pane that
      // waits on a session nobody started, with no error and no way to retry.
      ok = false;
      toast(tr("srow.resume_failed"), { kind: "error" });
    }
    await useSessionsStore.getState().refresh();
    return ok;
  },
}));

/** Poll every 4s while the tab is visible AND the workspace is running — a
 * stopped/booting agent only 502s, so polling it is pure waste (docs/35 §35.9-9).
 * The running edge is picked up on the next tick (and wireWorkspaceRefresh fires
 * an immediate refetch). Skipped while the push channel covers this stream
 * (api/events — the poll is the fallback). Returns cleanup (StrictMode-safe). */
export function startSessionsPolling(): () => void {
  const load = () => {
    if (document.hidden || pushHealthy() || !wsRunning(useWorkspaceStore.getState().state)) return;
    void useSessionsStore.getState().refresh();
  };
  load();
  const t = setInterval(load, 4000);
  return () => clearInterval(t);
}
