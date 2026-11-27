// One shared copy of the queue settings (GET /api/memo-queues) that the Memo queue view and the
// Auto-run view both read. Each view keeps its own poll, but both publish into and render from
// this cache, and a successful save writes its result in at once: a dialog opened from either view
// right after a save starts from the saved values, not from whatever the last poll saw.
import { create } from "zustand";
import { useTenantStore } from "../../core/store/tenant.ts";
import type { MemoQueue, MemoQueuesWire } from "../../types/memo.ts";

/** Who the settings belong to: the tenant and the signed-in account (a reconnect moves identityRev).
 *  A save carries the scope it was sent in, so an answer that arrives after a switch is dropped. */
export function queueScope(): string {
  const s = useTenantStore.getState();
  return (s.tenant ?? "") + "\u0000" + s.identityRev;
}

/** The run state of a queue: written by the Control Plane's own loops, not by a settings save, and
 *  absent from (or defaulted in) the save's answer. A save keeps what the last list read said. */
function withRunState(saved: MemoQueue, old: MemoQueue | undefined): MemoQueue {
  if (!old) return saved;
  return {
    ...saved,
    paused: old.paused,
    pausedAt: old.pausedAt,
    pausedReason: old.pausedReason,
    draining: old.draining,
    noReportAt: old.noReportAt,
    noRefsAt: old.noRefsAt,
    noTidyAt: old.noTidyAt,
    waiting: old.waiting,
    // A save may raise the slot limit that a "no_place" was read against; the next read says.
    fireBlock: old.fireBlock?.reason === "no_place" ? undefined : old.fireBlock,
  };
}

interface QueueSettingsCache {
  /** The scope (queueScope) the cached settings were read for; a read for another replaces them. */
  tenant: string;
  queues: MemoQueue[];
  tenantEnabled: boolean;
  runCapacity?: MemoQueuesWire["runCapacity"];
  /** The Control Plane offers the repository settings check and export (#4881). */
  repoSettings: boolean;
  /** False until a read has succeeded: tenantEnabled's initial false is "not read yet". */
  known: boolean;
  /** Categories whose settings may have changed without this cache seeing the row (an approval
   *  whose answer was lost). Their settings dialog waits for a read begun after the mark. */
  unsure: string[];
  /** Numbers a read when its request STARTS, so a save can outrank the reads already in flight. */
  begin(): number;
  /** A read's answer. Dropped when a save or a later read already landed after the read began. */
  publish(tenant: string, ticket: number, wire: MemoQueuesWire): void;
  /** A successful save: the saved row goes in now, and every read begun before it is dropped. */
  applySaved(tenant: string, queue: MemoQueue): void;
  /** A successful removal of a category's settings. */
  applyRemoved(tenant: string, categoryId: string): void;
  /** A write to a category's settings whose outcome is unknown: every read begun before it is
   *  dropped, and the category stays unsure until a later read lands. */
  markUnsure(tenant: string, categoryId: string): void;
  reset(): void;
}

let seq = 0; // tickets handed out
let floor = 0; // reads begun at or before this ticket may no longer land

export const useQueueSettingsCache = create<QueueSettingsCache>((set, get) => ({
  tenant: "",
  queues: [],
  tenantEnabled: false,
  repoSettings: false,
  known: false,
  unsure: [],
  begin: () => ++seq,
  publish: (tenant, ticket, wire) => {
    if (ticket <= floor) return;
    floor = ticket; // a slower, older read must not land over this one
    // A read that lands began after every mark (earlier ones were dropped above), so it holds the
    // rows the marks were waiting for.
    set({ tenant, queues: wire.queues, tenantEnabled: wire.tenantEnabled, runCapacity: wire.runCapacity, repoSettings: wire.repoSettings, known: true, unsure: [] });
  },
  applySaved: (tenant, queue) => {
    const cur = get();
    // Nothing read for this tenant yet: there is no stale copy to fix, and a lone row would pass
    // for the whole list.
    // The scope must also still be the live one: an answer from before a switch must not move the
    // shared floor past a read the new scope has already started.
    if (!cur.known || cur.tenant !== tenant || tenant !== queueScope()) return;
    floor = ++seq;
    const i = cur.queues.findIndex((q) => q.categoryId === queue.categoryId);
    const queues = i < 0 ? [...cur.queues, queue] : cur.queues.map((q, j) => (j === i ? withRunState(queue, q) : q));
    set({ queues, unsure: cur.unsure.filter((id) => id !== queue.categoryId) });
  },
  applyRemoved: (tenant, categoryId) => {
    const cur = get();
    if (!cur.known || cur.tenant !== tenant || tenant !== queueScope()) return;
    floor = ++seq;
    set({ queues: cur.queues.filter((q) => q.categoryId !== categoryId) });
  },
  markUnsure: (tenant, categoryId) => {
    const cur = get();
    if (tenant !== queueScope()) return;
    floor = ++seq;
    if (!cur.unsure.includes(categoryId)) set({ unsure: [...cur.unsure, categoryId] });
  },
  reset: () => {
    floor = ++seq;
    set({ tenant: "", queues: [], tenantEnabled: false, runCapacity: undefined, repoSettings: false, known: false, unsure: [] });
  },
}));

const NONE: MemoQueue[] = [];

/** The cached settings as seen from `tenant`: empty and unknown while the cache holds another's. */
export function useQueueSettings(tenant: string): Pick<QueueSettingsCache, "queues" | "tenantEnabled" | "runCapacity" | "known"> {
  const t = useQueueSettingsCache((s) => s.tenant);
  const queues = useQueueSettingsCache((s) => s.queues);
  const tenantEnabled = useQueueSettingsCache((s) => s.tenantEnabled);
  const known = useQueueSettingsCache((s) => s.known);
  const runCapacity = useQueueSettingsCache((s) => s.runCapacity);
  return t === tenant ? { queues, tenantEnabled, known, runCapacity } : { queues: NONE, tenantEnabled: false, known: false };
}
