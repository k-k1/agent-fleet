// Work item inbox store (docs/log/80). Fed by two sources with the same shape: the SSE
// `workitems` frame (core/push/wire) and a slow poll while the section is mounted, which
// takes over whenever the stream is down (the fallback rule every other store follows).
//
// ⚠️ A failed load never blanks `payload`. The rail's whole point is to still show
// something while the Workspace is stopped; committing an empty list on a transient
// error would make it look like the tickets went away.
import { create } from "zustand";
import { errText } from "../../core/api/client.ts";
import { pushHealthy } from "../../core/push/events.ts";
import { t } from "../../lib/i18n/index.ts";
import { workItemList, workItemRefresh } from "./api.ts";
import { readWorkItems, type WorkItemPayload } from "./read.ts";

interface WorkItemState {
  payload: WorkItemPayload | null;
  loaded: boolean;
  /** "" = fine. Kept alongside the last good payload, never instead of it. */
  loadErr: string;
  /** True while the 更新 button's forced refresh is in flight. */
  refreshing: boolean;
  applyPush(d: unknown): void;
  refresh(): Promise<void>;
  forceRefresh(): Promise<void>;
  reset(): void;
}

export const useWorkItemStore = create<WorkItemState>((set) => ({
  payload: null,
  loaded: false,
  loadErr: "",
  refreshing: false,
  applyPush(d) {
    const { payload } = readWorkItems(d);
    if (payload) set({ payload, loaded: true, loadErr: "" });
  },
  async refresh() {
    let res: unknown;
    try {
      res = await workItemList();
    } catch {
      set({ loadErr: t("wi.load_failed") });
      return;
    }
    const { payload, error } = readWorkItems(res);
    if (!payload) {
      set({ loadErr: errText(error) || t("wi.load_failed") });
      return;
    }
    set({ payload, loaded: true, loadErr: "" });
  },
  async forceRefresh() {
    set({ refreshing: true });
    try {
      const res = await workItemRefresh();
      const { payload, error } = readWorkItems(res);
      if (payload) set({ payload, loaded: true, loadErr: "" });
      else set({ loadErr: errText(error) || t("wi.load_failed") });
    } catch {
      set({ loadErr: t("wi.load_failed") });
    } finally {
      set({ refreshing: false });
    }
  },
  reset: () => set({ payload: null, loaded: false, loadErr: "", refreshing: false }),
}));

const POLL_MS = 60000;

/** Poll while the section is mounted and the push stream is NOT carrying the data.
 * Returns the cleanup (StrictMode-safe). */
export function startWorkItemPolling(): () => void {
  const load = () => {
    if (document.hidden || pushHealthy()) return;
    void useWorkItemStore.getState().refresh();
  };
  void useWorkItemStore.getState().refresh(); // first load regardless of the stream
  const id = setInterval(load, POLL_MS);
  return () => clearInterval(id);
}
