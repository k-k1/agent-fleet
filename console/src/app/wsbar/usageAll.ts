import { api } from "../../core/api/client.ts";
import type { UsageReading } from "./usageCommon.tsx";

// One poll for every usage chip (#3127). The Agent's GET /usage/all answers each kind's chip
// payload, in the shape its own endpoint answers, under `kinds.<kind>`. Each chip subscribes
// here with its kind and endpoint, and every chip of a tenant shares one request.
//
// When /usage/all fails — an Agent older than this Console has no such route (404), or the
// request just failed — the chips fall back to their own endpoints for that poll, so a skew
// between the Console and a not-yet-updated workspace costs one extra request, never a blank
// chip.

export const USAGE_ALL = "api/usage/all";

type Listener = (reading: UsageReading) => void;

interface Sub {
  kind: string;
  endpoint: string;
  listener: Listener;
}

interface Hub {
  subs: Set<Sub>;
  queued: boolean;
  timer: ReturnType<typeof setInterval>;
  onVis: () => void;
}

const hubs = new Map<string, Hub>();

// The 5-minute re-read only recovers a chip whose workspace started after mount and keeps tabs
// in sync; the Agent caches every source, so it is not a fast background timer.
const POLL_MS = 300000;

function deliver(hub: Hub, kinds: Record<string, UsageReading>) {
  for (const s of hub.subs) {
    const r = kinds[s.kind];
    if (r) s.listener(r);
  }
}

// Fallback: each chip's own endpoint, as before /usage/all existed. Only the chip that asked for
// a refresh forces one.
function fetchEach(hub: Hub, refreshKind: string | null): Promise<void> {
  return Promise.all(
    [...hub.subs].map((s) =>
      api(s.endpoint + (s.kind === refreshKind ? "?refresh=1" : ""))
        .then((d: UsageReading | null) => d && s.listener(d))
        .catch(() => {}),
    ),
  ).then(() => {});
}

function fetchAll(hub: Hub, refreshKind: string | null): Promise<void> {
  const q = refreshKind ? `?refresh=1&kind=${encodeURIComponent(refreshKind)}` : "";
  return api(USAGE_ALL + q).then(
    (d: { kinds?: Record<string, UsageReading> } | null) => {
      if (!d || typeof d.kinds !== "object" || d.kinds === null) return fetchEach(hub, refreshKind);
      deliver(hub, d.kinds);
    },
    () => fetchEach(hub, refreshKind),
  );
}

// poll runs one shared read. Several chips mounting in the same commit each ask; the microtask
// folds them into one request.
function poll(hub: Hub) {
  if (hub.queued || document.hidden) return;
  hub.queued = true;
  queueMicrotask(() => {
    hub.queued = false;
    if (hub.subs.size) void fetchAll(hub, null);
  });
}

// subscribeUsage registers one chip for tenant's shared poll and asks for a read. The returned
// function unsubscribes; the last one out stops the tenant's timer.
export function subscribeUsage(tenant: string | null, kind: string, endpoint: string, listener: Listener): () => void {
  const key = tenant ?? "";
  let hub = hubs.get(key);
  if (!hub) {
    const h: Hub = {
      subs: new Set(),
      queued: false,
      timer: setInterval(() => poll(h), POLL_MS),
      onVis: () => !document.hidden && poll(h),
    };
    document.addEventListener("visibilitychange", h.onVis);
    hubs.set(key, h);
    hub = h;
  }
  const sub: Sub = { kind, endpoint, listener };
  hub.subs.add(sub);
  poll(hub);
  const h = hub;
  return () => {
    h.subs.delete(sub);
    if (h.subs.size) return;
    clearInterval(h.timer);
    document.removeEventListener("visibilitychange", h.onVis);
    if (hubs.get(key) === h) hubs.delete(key);
  };
}

// refreshUsage forces kind's source to re-read (the dropdown's reload) and delivers the answer
// to every chip of the tenant. It runs even while the tab is hidden: the user asked.
export function refreshUsage(tenant: string | null, kind: string): Promise<void> {
  const hub = hubs.get(tenant ?? "");
  if (!hub) return Promise.resolve();
  return fetchAll(hub, kind);
}
