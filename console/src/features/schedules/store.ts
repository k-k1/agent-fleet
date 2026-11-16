// Schedules store (zustand): a cross-section reveal signal, same shape as the Files
// tree's (features/files/store.ts). It exists because a schedule's failure surfaces
// somewhere else entirely — the notification center — and "why didn't it run?" is only
// answerable in the row's RUN HISTORY, which lives in the left rail.
import { create } from "zustand";

interface SchedulesStore {
  /** Reveal request: schedule id + a counter so repeats re-trigger. */
  reveal: { id: string | null; n: number };
  revealSchedule(id: string): void;
  /** failedScheduleKeys of the rows the section lists (the loaded list, narrowed by the active
   * working set). The list itself lives in the section; this is the one fact about it the
   * activity bar needs while the section is hidden. */
  failed: string[];
  /** The same for the whole loaded list. Acknowledged keys are pruned against this one, so a
   * working set that hides a schedule does not make its acknowledged failure unread again. */
  failedAll: string[];
  setFailed(keys: string[], all: string[]): void;
}

const sameKeys = (a: string[], b: string[]) => a.length === b.length && a.every((k, i) => k === b[i]);

export const useSchedulesStore = create<SchedulesStore>((set) => ({
  reveal: { id: null, n: 0 },
  revealSchedule: (id) => set((s) => ({ reveal: { id, n: s.reveal.n + 1 } })),
  failed: [],
  failedAll: [],
  setFailed: (keys, all) =>
    set((s) => (sameKeys(s.failed, keys) && sameKeys(s.failedAll, all) ? s : { failed: keys, failedAll: all })),
}));
