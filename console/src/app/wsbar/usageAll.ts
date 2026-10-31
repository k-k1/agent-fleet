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
  // The follow-up read for kinds the Agent answered as pending, and how many in a row so far.
  followUp: ReturnType<typeof setTimeout> | null;
  followUps: number;
}

const hubs = new Map<string, Hub>();

// The 5-minute re-read only recovers a chip whose workspace started after mount and keeps tabs
// in sync; the Agent caches every source, so it is not a fast background timer.
const POLL_MS = 300000;

// The Agent answers within a few seconds and lists the kinds still reading (a cold agy scrape
// takes up to 25 s) as pending; they keep running there and fill their caches, so a read a
// little later finds them. Bounded, so a kind that never finishes does not turn into a fast
// background timer.
const FOLLOW_UP_MS = 5000;
const MAX_FOLLOW_UPS = 6;

// deliver hands each chip its kind's reading. A forced refresh delivers only the kind that was
// asked for: every delivery is a new object to the chip, and claude/codex/muse chips post a
// usage observation on each one.
function deliver(
  hub: Hub,
  kinds: Record<string, UsageReading>,
  only: string | null,
) {
  for (const s of hub.subs) {
    if (only && s.kind !== only) continue;
    const r = kinds[s.kind];
    if (r) s.listener(r);
  }
}

function followUp(hub: Hub, pending: unknown) {
  const waiting =
    Array.isArray(pending) &&
    [...hub.subs].some((s) => pending.includes(s.kind));
  if (!waiting) {
    // Nothing is pending any more (a refresh may have answered it): drop a follow-up that
    // would only repeat this read.
    if (hub.followUp) clearTimeout(hub.followUp);
    hub.followUp = null;
    hub.followUps = 0;
    return;
  }
  if (hub.followUp || hub.followUps >= MAX_FOLLOW_UPS) return;
  hub.followUps++;
  hub.followUp = setTimeout(() => {
    hub.followUp = null;
    // A hidden tab reads nothing; showing it again polls (onVis), which picks this up.
    if (document.hidden) return;
    if (hub.subs.size) void fetchAll(hub, null);
  }, FOLLOW_UP_MS);
}

// Fallback: each chip's own endpoint, as before /usage/all existed. A refresh reads only the
// chip that asked for it, forced.
function fetchEach(hub: Hub, refreshKind: string | null): Promise<void> {
  return Promise.all(
    [...hub.subs]
      .filter((s) => !refreshKind || s.kind === refreshKind)
      .map((s) =>
        api(s.endpoint + (refreshKind ? "?refresh=1" : ""))
          .then((d: UsageReading | null) => d && s.listener(d))
          .catch(() => {}),
      ),
  ).then(() => {});
}

function fetchAll(hub: Hub, refreshKind: string | null): Promise<void> {
  const q = refreshKind
    ? `?refresh=1&kind=${encodeURIComponent(refreshKind)}`
    : "";
  return api(USAGE_ALL + q).then(
    // An Agent without the route answers 404, which api() resolves as {error}: no kinds.
    (
      d: { kinds?: Record<string, UsageReading>; pending?: string[] } | null,
    ) => {
      if (!d || typeof d.kinds !== "object" || d.kinds === null)
        return fetchEach(hub, refreshKind);
      deliver(hub, d.kinds, refreshKind);
      followUp(hub, d.pending);
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
    // Each regular read starts a fresh follow-up budget, so one slow stretch does not leave a
    // kind waiting for a manual reload.
    hub.followUps = 0;
    if (hub.subs.size) void fetchAll(hub, null);
  });
}

// subscribeUsage registers one chip for tenant's shared poll and asks for a read. The returned
// function unsubscribes; the last one out stops the tenant's timer.
export function subscribeUsage(
  tenant: string | null,
  kind: string,
  endpoint: string,
  listener: Listener,
): () => void {
  const key = tenant ?? "";
  let hub = hubs.get(key);
  if (!hub) {
    const h: Hub = {
      subs: new Set(),
      queued: false,
      timer: setInterval(() => poll(h), POLL_MS),
      onVis: () => !document.hidden && poll(h),
      followUp: null,
      followUps: 0,
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
    if (h.followUp) clearTimeout(h.followUp);
    document.removeEventListener("visibilitychange", h.onVis);
    if (hubs.get(key) === h) hubs.delete(key);
  };
}

// refreshUsage forces kind's source to re-read (the dropdown's reload) and delivers the answer
// to that kind's chip. It runs even while the tab is hidden: the user asked.
export function refreshUsage(
  tenant: string | null,
  kind: string,
): Promise<void> {
  const hub = hubs.get(tenant ?? "");
  if (!hub) return Promise.resolve();
  return fetchAll(hub, kind);
}
