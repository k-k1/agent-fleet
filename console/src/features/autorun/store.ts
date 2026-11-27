// What the Auto-run view and the activity bar's badge for it share: the auto-run queues and the
// run each one reported for a session. The badge needs it while the view is off screen, so the
// read is not the view's. One poll serves every consumer: it runs while at least one of them
// asks for it.
import { useEffect } from "react";
import { create } from "zustand";
import { memoQueueDeliveries, memoQueueList } from "../memo/api.ts";
import { readDeliveries, readMemoQueues } from "../memo/queues.ts";
import type { MemoQueue, MemoQueuesWire } from "../../types/memo.ts";
import { useTenantStore } from "../../core/store/tenant.ts";
import { useQueueSettings, useQueueSettingsCache } from "../memo/queueSettingsCache.ts";
import type { RunDelivery } from "./runs.ts";

const POLL_MS = 30000;

interface AutoRunStore {
  queues: MemoQueue[];
  /** Whether the organisation has allowed auto-run (the queue list says so). */
  tenantEnabled: boolean;
  /** A run's session name -> what its queue reported. */
  deliveries: Map<string, RunDelivery>;
  /** True once a queue list has been read for this scope: "nothing yet" is not "no failures". */
  loaded: boolean;
  /** The tenant and account the data above was read for. */
  scope: string;
}

export const useAutoRunStore = create<AutoRunStore>(() => ({ queues: [], tenantEnabled: false, deliveries: new Map(), loaded: false, scope: "" }));

interface Loop {
  scope: string;
  load: () => Promise<void>;
  /** An explicit read: whatever is in flight may predate the change that asked for it, so its
   *  answers are dropped and a new read starts (after the queue list in flight, if any). */
  reload: () => void;
  stop: () => void;
}

let loop: Loop | null = null;
let users = 0;

/** Where the next read of a category starts looking for closings: the earlier of the latest
 *  closing known and the creation of the oldest run still known as unsettled. A run closes no
 *  earlier than it was created, so one that was open at the last read is found again however
 *  many runs closed since, and a closing stamped before a later one (replicas whose clocks
 *  differ) is not skipped. "" when nothing is known, which asks for no closings. Server stamps are
 *  fixed-width, so the text order is the time order. */
export function closedLowerBound(deliveries: Map<string, RunDelivery>, categoryId: string): string {
  let closed = "";
  let open = "";
  for (const d of deliveries.values()) {
    if (d.categoryId !== categoryId) continue;
    if (d.state === "closed") {
      if (d.closedAt > closed) closed = d.closedAt;
    } else if (d.createdAt && (!open || d.createdAt < open)) open = d.createdAt;
  }
  return open && (!closed || open < closed) ? open : closed;
}

/** Each category is read on its own, one read at a time, so a slow one holds nobody up, and an
 *  answer to a read the scope has outgrown is dropped. The read asks for every open run too, so
 *  a run older than the newest fifty keeps its category and state, and every run closed since
 *  closedLowerBound comes with it, so one that closes after it fell off the list gets its end. */
function startLoop(scope: string): Loop {
  let live = true;
  let listing = false;
  const reading = new Set<string>();
  let current = new Set<string>(); // categories the latest queue list names
  // Bumped when a category leaves the list, so a read begun before cannot land after it returns.
  const epoch = new Map<string, number>();
  const set = (f: (prev: Map<string, RunDelivery>) => Map<string, RunDelivery>) =>
    useAutoRunStore.setState((s) => {
      const next = f(s.deliveries);
      return next === s.deliveries ? s : { deliveries: next };
    });
  const readCategory = async (q: MemoQueue) => {
    if (reading.has(q.categoryId)) return;
    reading.add(q.categoryId);
    const mine = epoch.get(q.categoryId) ?? 0;
    try {
      const list = readDeliveries(await memoQueueDeliveries(q.categoryId, true, closedLowerBound(useAutoRunStore.getState().deliveries, q.categoryId)));
      if (!live || !list || !current.has(q.categoryId) || (epoch.get(q.categoryId) ?? 0) !== mine) return;
      set((prev) => {
        const next = new Map(prev);
        const seen = new Set(list.map((d) => d.session));
        const owners = new Map(list.map((d) => [d.id, d.session]));
        // A run the list no longer holds keeps its category and, once closed, its end; an
        // unsettled state is dropped (the sessions' own word is used for it).
        for (const [name, d] of prev) {
          if (d.categoryId !== q.categoryId || seen.has(name)) continue;
          if (owners.get(d.id)) next.delete(name);
          else if (d.state !== "closed") next.set(name, { ...d, state: "" });
        }
        for (const d of list) {
          if (d.session) {
            next.set(d.session, { id: d.id, categoryId: q.categoryId, state: d.state, outcome: d.outcome, turnOutcome: d.firstTurn?.outcome ?? "", sentAt: d.sentAt, createdAt: d.createdAt, closedAt: d.closedAt, heldBy: d.heldBy });
          }
        }
        return next;
      });
    } catch {
      /* keep what this category had */
    } finally {
      if ((epoch.get(q.categoryId) ?? 0) === mine) reading.delete(q.categoryId);
    }
  };
  // Bumped by an explicit reload: a queue list asked for before it may predate the change, so it is
  // never published, and the reload reads on its own instead of waiting for one that may hang.
  let gen = 0;
  const load = async () => {
    if (listing) return;
    listing = true;
    const mine = gen;
    try {
      const ticket = useQueueSettingsCache.getState().begin();
      const wire = readMemoQueues(await memoQueueList());
      if (!live || !wire || mine !== gen) return;
      useQueueSettingsCache.getState().publish(scope, ticket, wire);
      const now = new Set(wire.queues.map((q) => q.categoryId));
      for (const id of current) {
        if (now.has(id)) continue;
        epoch.set(id, (epoch.get(id) ?? 0) + 1);
        reading.delete(id);
      }
      current = now;
      useAutoRunStore.setState({ queues: wire.queues, tenantEnabled: wire.tenantEnabled, loaded: true });
      // A category that is gone is no longer read: whatever it left unsettled is dropped too.
      set((prev) => {
        let next: Map<string, RunDelivery> | null = null;
        for (const [name, d] of prev) {
          if (current.has(d.categoryId) || d.state === "closed" || d.state === "") continue;
          next ??= new Map(prev);
          next.set(name, { ...d, state: "" });
        }
        return next ?? prev;
      });
      for (const q of wire.queues) void readCategory(q);
    } catch {
      /* keep the last read */
    } finally {
      if (mine === gen) listing = false;
    }
  };
  const reload = () => {
    for (const id of current) {
      epoch.set(id, (epoch.get(id) ?? 0) + 1);
      reading.delete(id);
    }
    gen++;
    listing = false;
    void load();
  };
  void load();
  const id = setInterval(() => {
    if (!document.hidden) void load();
  }, POLL_MS);
  return {
    scope,
    load,
    reload,
    stop: () => {
      live = false;
      clearInterval(id);
    },
  };
}

/** Starts (or joins) the poll for a scope; the returned function leaves it. */
export function acquireAutoRuns(scope: string): () => void {
  users++;
  if (loop && loop.scope !== scope) {
    loop.stop();
    loop = null;
  }
  // Another tenant's or account's runs are not this scope's. The same scope keeps what it knew
  // across a pause of the poll: a closed run that left the newest fifty has nowhere else to
  // get its category and duration from.
  if (useAutoRunStore.getState().scope !== scope) useAutoRunStore.setState({ queues: [], tenantEnabled: false, deliveries: new Map(), loaded: false, scope });
  if (!loop) loop = startLoop(scope);
  let released = false;
  return () => {
    if (released) return;
    released = true;
    if (--users > 0) return;
    loop?.stop();
    loop = null;
  };
}

/** Read now unless a read is in flight: the view came on screen. */
export const refreshAutoRuns = (): void => void loop?.load();

/** Read again from scratch: something the member changed (a saved setting, "Reload") may be missing
 *  from a read in flight. */
export const reloadAutoRuns = (): void => loop?.reload();

/** The runs read for the scope in force: another scope's data is never returned, even before a poll
 *  has replaced it. */
export function useScopedAutoRuns(): Omit<AutoRunStore, "scope"> & Pick<MemoQueuesWire, "runCapacity"> {
  const tenant = useTenantStore((s) => s.tenant);
  const identity = useTenantStore((s) => s.identityRev);
  const scope = tenant + "\u0000" + identity;
  const st = useAutoRunStore();
  // The queues come from the cache the Memo queue view shares, so a settings change made there
  // (a save or a removal) reaches the view and the bar's badge together, whatever the next read does.
  const settings = useQueueSettings(scope);
  return st.scope === scope ? { ...st, queues: settings.queues, tenantEnabled: settings.tenantEnabled, runCapacity: settings.runCapacity } : EMPTY;
}
const EMPTY = { queues: [] as MemoQueue[], tenantEnabled: false, deliveries: new Map<string, RunDelivery>(), loaded: false };

/** Keeps the queues and their runs current while `on`. Safe to call from several places. */
export function useAutoRunFeed(on: boolean): void {
  const tenant = useTenantStore((s) => s.tenant);
  const identity = useTenantStore((s) => s.identityRev);
  const scope = tenant + "\u0000" + identity;
  useEffect(() => (on ? acquireAutoRuns(scope) : undefined), [on, scope]);
}
