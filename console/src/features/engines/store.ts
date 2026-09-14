// Engine indicator store (ADR 0084 decision 1). Fed by two sources with the same shape: the
// SSE `engines` frame (core/push/wire) and a slow poll while the push stream is not carrying
// it — the same fallback rule every other pushed store follows (core/push/events.ts).
//
// `rows === null` means "never loaded" (the pill renders nothing); an empty array is a real
// answer ("no engine role is visible to this tenant" — decision 5) and must not be confused
// with it. A failed refresh leaves the previous rows in place rather than blanking the
// pills on a transient error, same as the work-item store.
import { create } from "zustand";
import { pushHealthy } from "../../core/push/events.ts";
import { engineStatus } from "./api.ts";
import { readEngines, type EngineMemberRow } from "./wire.ts";

interface EnginesState {
  rows: EngineMemberRow[] | null;
  applyPush(d: unknown): void;
  refresh(): Promise<void>;
  reset(): void;
}

export const useEnginesStore = create<EnginesState>((set) => ({
  rows: null,
  applyPush(d) {
    const rows = readEngines(d);
    if (rows) set({ rows });
  },
  async refresh() {
    let res: unknown;
    try {
      res = await engineStatus();
    } catch {
      return;
    }
    const rows = readEngines(res);
    if (rows) set({ rows });
  },
  reset: () => set({ rows: null }),
}));

// A topbar pill, not a chat cost meter — 60s matches the work-item rail's fallback cadence
// (features/workitems/store.ts), not the 4s the push stream itself ticks at.
const POLL_MS = 60000;

/** Poll while the push stream is not carrying `engines` — same fallback rule every other
 *  store follows. The first call runs regardless of the stream, so the pill has something to
 *  show before the first push tick or on a CP too old to have the stream at all (decision 1's
 *  REST fallback); later ticks skip themselves once the stream is healthy or the tab is
 *  hidden. Returns the cleanup (StrictMode-safe). */
export function startEnginesPolling(): () => void {
  const load = () => {
    if (document.hidden || pushHealthy()) return;
    void useEnginesStore.getState().refresh();
  };
  void useEnginesStore.getState().refresh();
  const id = setInterval(load, POLL_MS);
  return () => clearInterval(id);
}
