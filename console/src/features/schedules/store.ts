// Schedules store (zustand): a cross-section reveal signal, same shape as the Files
// tree's (features/files/store.ts). It exists because a schedule's failure surfaces
// somewhere else entirely — the notification center — and "why didn't it run?" is only
// answerable in the row's RUN HISTORY, which lives in the left rail.
import { create } from "zustand";

interface SchedulesStore {
  /** Reveal request: schedule id + a counter so repeats re-trigger. */
  reveal: { id: string | null; n: number };
  revealSchedule(id: string): void;
}

export const useSchedulesStore = create<SchedulesStore>((set) => ({
  reveal: { id: null, n: 0 },
  revealSchedule: (id) => set((s) => ({ reveal: { id, n: s.reveal.n + 1 } })),
}));
