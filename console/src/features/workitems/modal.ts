// The work item detail / report modals, opened from anywhere (#1659). The rail row and a ticket
// link in the mirror open the same single instance, rendered once by WorkItemModalHost: two
// copies would stack, and both the Esc layering and the focus trap assume one modal at a time
// (ADR 0061 decision 20).
import { create } from "zustand";
import type { WorkItem } from "./read.ts";

export interface WorkItemDetailTarget {
  item: WorkItem;
  /** Not in the inbox cache: the row is a stand-in carrying only the key and a tracker URL. */
  reference: boolean;
  /** A working copy to default the launch to ("" = the query's hint, as from the rail). */
  repoHint: string;
}

interface WorkItemModalState {
  detail: WorkItemDetailTarget | null;
  report: WorkItem | null;
  openDetail(item: WorkItem, opts?: { reference?: boolean; repoHint?: string }): void;
  openReport(item: WorkItem): void;
  close(): void;
}

export const useWorkItemModal = create<WorkItemModalState>((set) => ({
  detail: null,
  report: null,
  openDetail: (item, opts) =>
    set({ detail: { item, reference: !!opts?.reference, repoHint: opts?.repoHint || "" }, report: null }),
  openReport: (item) => set({ detail: null, report: item }),
  close: () => set({ detail: null, report: null }),
}));
