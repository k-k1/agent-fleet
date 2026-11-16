// Schedules store (zustand): a cross-section reveal signal, same shape as the Files
// tree's (features/files/store.ts). It exists because a schedule's failure surfaces
// somewhere else entirely — the notification center — and "why didn't it run?" is only
// answerable in the row's RUN HISTORY, which lives in the left rail.
import { create } from "zustand";

interface SchedulesStore {
  /** Reveal request: schedule id + a counter so repeats re-trigger. */
  reveal: { id: string | null; n: number };
  revealSchedule(id: string): void;
  /** failedScheduleKeys of the list the section last loaded. The list itself lives in the
   * section; this is the one fact about it the activity bar needs while the section is hidden. */
  failed: string[];
  setFailed(keys: string[]): void;
}

export const useSchedulesStore = create<SchedulesStore>((set) => ({
  reveal: { id: null, n: 0 },
  revealSchedule: (id) => set((s) => ({ reveal: { id, n: s.reveal.n + 1 } })),
  failed: [],
  setFailed: (keys) =>
    set((s) => (s.failed.length === keys.length && s.failed.every((k, i) => k === keys[i]) ? s : { failed: keys })),
}));
