// One shared copy of the queue settings (GET /api/memo-queues) that the Memo queue view and the
// Auto-run view both read. Each view keeps its own poll, but both publish into and render from
// this cache, and a successful save writes its result in at once: a dialog opened from either view
// right after a save starts from the saved values, not from whatever the last poll saw.
import { create } from "zustand";
import type { MemoQueue, MemoQueuesWire } from "../../types/memo.ts";

interface QueueSettingsCache {
  /** The tenant the cached settings were read for; a read for another tenant replaces them. */
  tenant: string;
  queues: MemoQueue[];
  tenantEnabled: boolean;
  /** False until a read has succeeded: tenantEnabled's initial false is "not read yet". */
  known: boolean;
  /** Numbers a read when its request STARTS, so a save can outrank the reads already in flight. */
  begin(): number;
  /** A read's answer. Dropped when a save or a later read already landed after the read began. */
  publish(tenant: string, ticket: number, wire: MemoQueuesWire): void;
  /** A successful save: the saved row goes in now, and every read begun before it is dropped. */
  applySaved(tenant: string, queue: MemoQueue): void;
  /** A successful removal of a category's settings. */
  applyRemoved(tenant: string, categoryId: string): void;
  reset(): void;
}

let seq = 0; // tickets handed out
let floor = 0; // reads begun at or before this ticket may no longer land

export const useQueueSettingsCache = create<QueueSettingsCache>((set, get) => ({
  tenant: "",
  queues: [],
  tenantEnabled: false,
  known: false,
  begin: () => ++seq,
  publish: (tenant, ticket, wire) => {
    if (ticket <= floor) return;
    floor = ticket; // a slower, older read must not land over this one
    set({ tenant, queues: wire.queues, tenantEnabled: wire.tenantEnabled, known: true });
  },
  applySaved: (tenant, queue) => {
    const cur = get();
    // Nothing read for this tenant yet: there is no stale copy to fix, and a lone row would pass
    // for the whole list.
    if (!cur.known || cur.tenant !== tenant) return;
    floor = ++seq;
    const i = cur.queues.findIndex((q) => q.categoryId === queue.categoryId);
    const queues = i < 0 ? [...cur.queues, queue] : cur.queues.map((q, j) => (j === i ? queue : q));
    set({ queues });
  },
  applyRemoved: (tenant, categoryId) => {
    const cur = get();
    if (!cur.known || cur.tenant !== tenant) return;
    floor = ++seq;
    set({ queues: cur.queues.filter((q) => q.categoryId !== categoryId) });
  },
  reset: () => {
    floor = ++seq;
    set({ tenant: "", queues: [], tenantEnabled: false, known: false });
  },
}));

const NONE: MemoQueue[] = [];

/** The cached settings as seen from `tenant`: empty and unknown while the cache holds another's. */
export function useQueueSettings(tenant: string): { queues: MemoQueue[]; tenantEnabled: boolean; known: boolean } {
  const t = useQueueSettingsCache((s) => s.tenant);
  const queues = useQueueSettingsCache((s) => s.queues);
  const tenantEnabled = useQueueSettingsCache((s) => s.tenantEnabled);
  const known = useQueueSettingsCache((s) => s.known);
  return t === tenant ? { queues, tenantEnabled, known } : { queues: NONE, tenantEnabled: false, known: false };
}
