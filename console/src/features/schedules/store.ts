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
   * working set that hides a schedule does not make its acknowledged failure unread again.
   * null until this tenant's list has loaded: "not known yet" must never be read as "no
   * failures exist", or a reload would prune every acknowledged key before the list arrives. */
  failedAll: string[] | null;
  setFailed(keys: string[], all: string[]): void;
  /** The list is not known (not loaded yet, another tenant's, the section gone). */
  clearFailed(): void;
  /** Ids of the schedules set to also show their worktree children in the Sessions view. Like
   * `failedAll` it comes from the section's list; empty until that list has loaded, which keeps
   * the default (hidden) rather than guessing. */
  shownIds: ReadonlySet<string>;
  setShownIds(ids: string[]): void;
}

const NO_IDS: ReadonlySet<string> = new Set();

const sameKeys = (a: string[], b: string[]) => a.length === b.length && a.every((k, i) => k === b[i]);

export const useSchedulesStore = create<SchedulesStore>((set) => ({
  reveal: { id: null, n: 0 },
  revealSchedule: (id) => set((s) => ({ reveal: { id, n: s.reveal.n + 1 } })),
  failed: [],
  failedAll: null,
  setFailed: (keys, all) =>
    set((s) => (s.failedAll && sameKeys(s.failed, keys) && sameKeys(s.failedAll, all) ? s : { failed: keys, failedAll: all })),
  shownIds: NO_IDS,
  setShownIds: (ids) =>
    set((s) => {
      if (ids.length === s.shownIds.size && ids.every((i) => s.shownIds.has(i))) return s;
      return { shownIds: ids.length ? new Set(ids) : NO_IDS };
    }),
  clearFailed: () => set((s) => (s.failedAll === null && s.failed.length === 0 ? s : { failed: [], failedAll: null })),
}));

/** Schedules whose worktree children the Sessions view lists too (see sessionAutoKind). */
export const useShownSchedules = (): ReadonlySet<string> => useSchedulesStore((s) => s.shownIds);
