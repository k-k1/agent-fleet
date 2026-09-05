// core/push/events — receive hub for the unified push channel (traffic reduction P3).
//
// A single CP GET /api/events (SSE) carries the workspace / sessions / stats /
// notifications / workitems frames to the registered handlers (wire.ts wires them to the
// stores). A frame's data has the same shape as the existing REST response, so the apply
// logic is shared with the polling path.
//
// Fallback policy: the existing always-on pollers are NOT removed. Each poller skips its
// tick only while pushHealthy(), and takes over the moment the stream breaks (an old CP's
// 404, a network drop, the tab going hidden) — so a new Console against an old CP loses no
// functionality.
//
// Reception uses a fetch stream rather than EventSource (as the chat stream does). The fetch
// wrapper injects cookie auth, the X-AF-Tenant header and the 401 → AuthExpiredModal latch,
// so there is no need for the WS-style query-param auth special case.
import { rel } from "../api/client.ts";

export type PushStream = "workspace" | "sessions" | "stats" | "notifications" | "workitems";
// data is exactly the per-stream REST response shape; validating it is the applying side's
// (the store's) job.
// eslint-disable-next-line @typescript-eslint/no-explicit-any
type Handler = (data: any) => void;

const handlers = new Map<PushStream, Set<Handler>>();

/** Register a handler for one stream. Returns the unsubscriber. */
export function onPush(stream: PushStream, h: Handler): () => void {
  let set = handlers.get(stream);
  if (!set) handlers.set(stream, (set = new Set()));
  set.add(h);
  return () => set?.delete(h);
}

let healthy = false;
/** True while the push stream is live — pollers skip their tick then. */
export const pushHealthy = (): boolean => healthy;

// Stream-established handlers. A hook for re-reading state that no frame carries because it
// is read once at boot (whoami's deployment capabilities, say) after a CP restart, i.e. a
// reconnect. No store is imported here either — wiring is wire.ts's job.
const connectHandlers = new Set<() => void>();

/** Register a callback fired each time the stream (re)connects. Returns the
 * unsubscriber. */
export function onPushConnect(h: () => void): () => void {
  connectHandlers.add(h);
  return () => {
    connectHandlers.delete(h);
  };
}

// Per-stream receive counter. A poller compares it before and after its fetch and discards
// its own (possibly older) result if a push frame was applied meanwhile — otherwise a polling
// response that arrives seconds late on a slow mobile link overwrites the push and the view
// stays stale until the next change.
const stamps = new Map<PushStream, number>();
export const pushStamp = (stream: PushStream): number => stamps.get(stream) || 0;

const RETRY_MAX = 30000;
const RETRY_UNSUPPORTED = 300000; // old CP (404/405): just re-check every 5 minutes
const WATCHDOG_MS = 45000; // the server pings ~every 20s even when quiet — 2 misses = dead

let stopped = true;
let ctrl: AbortController | null = null;
let retryTimer = 0;
let watchdogTimer = 0;
let backoff = 1000;

function dispatch(frame: string): void {
  // One SSE frame, already split on \n\n. Comment lines (": ping") are ignored.
  const line = frame.startsWith("data:") ? frame.slice(5).trim() : "";
  if (!line) return;
  let obj: { stream?: string; data?: unknown };
  try {
    obj = JSON.parse(line);
  } catch {
    return;
  }
  const stream = obj.stream as PushStream | undefined;
  if (!stream || obj.data == null) return;
  stamps.set(stream, (stamps.get(stream) || 0) + 1);
  for (const h of handlers.get(stream) || []) {
    try {
      h(obj.data);
    } catch {
      /* one handler throwing must not kill the stream */
    }
  }
}

function scheduleRetry(delay: number): void {
  window.clearTimeout(retryTimer);
  retryTimer = window.setTimeout(() => void connect(), delay);
}

async function connect(): Promise<void> {
  if (stopped || document.hidden || ctrl) return;
  const my = new AbortController();
  ctrl = my;
  const armWatchdog = () => {
    window.clearTimeout(watchdogTimer);
    watchdogTimer = window.setTimeout(() => my.abort(), WATCHDOG_MS);
  };
  let unsupported = false;
  const startedAt = Date.now();
  try {
    // The connect phase needs a deadline too: if an unresponsive proxy never returns the
    // headers we never reach the watchdog (re-armed on every read) and never reconnect.
    // Once established the read-side armWatchdog takes over, so long streams stay up.
    armWatchdog();
    const res = await fetch(rel("api/events"), { signal: my.signal });
    const ct = res.headers.get("Content-Type") || "";
    if (!res.ok || !ct.startsWith("text/event-stream") || !res.body) {
      // An old CP does not know this route (404). Show no error — the pollers are still
      // live, so nothing is lost; traffic simply returns to the previous level.
      unsupported = res.status === 404 || res.status === 405;
      // Release a body we will not read, so the connection is not left dangling.
      void res.body?.cancel().catch(() => {});
      return;
    }
    healthy = true;
    for (const h of connectHandlers) {
      try {
        h();
      } catch {
        /* one handler throwing must not kill the stream (same policy as dispatch) */
      }
    }
    armWatchdog();
    const reader = res.body.getReader();
    const dec = new TextDecoder();
    let buf = "";
    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      armWatchdog();
      buf += dec.decode(value, { stream: true });
      let idx: number;
      while ((idx = buf.indexOf("\n\n")) >= 0) {
        const frame = buf.slice(0, idx);
        buf = buf.slice(idx + 2);
        // Frames still buffered after an abort (restartPush, tab hidden) may belong to the
        // previous tenant — do not apply them.
        if (my.signal.aborted) return;
        dispatch(frame);
      }
    }
  } catch {
    /* abort (hidden tab, tenant switch, watchdog) or a network drop — reconnect below */
  } finally {
    window.clearTimeout(watchdogTimer);
    healthy = false;
    if (ctrl === my) ctrl = null;
    if (!stopped && !document.hidden) {
      // A stream that lived 30s+ counts as a transient drop and reconnects at once; one
      // that dies immediately backs off exponentially, so a server restart draws no storm.
      if (Date.now() - startedAt > 30000) backoff = 1000;
      const delay = unsupported ? RETRY_UNSUPPORTED : backoff;
      backoff = Math.min(backoff * 2, RETRY_MAX);
      scheduleRetry(delay);
    }
  }
}

/** Drop the current stream (if any) and reconnect shortly — tenant switch. Call
 * BEFORE resetting tenant-scoped stores so no old-tenant frame lands after. */
export function restartPush(): void {
  window.clearTimeout(retryTimer);
  backoff = 1000;
  if (ctrl) ctrl.abort();
  else void connect();
}

/** Boot wiring (App effect). Connects while visible, disconnects while hidden
 * (the fallback pollers keep their existing hidden-tab behavior), reconnects on
 * network recovery. Returns the cleanup — StrictMode-safe. */
export function startPushChannel(): () => void {
  stopped = false;
  const onVis = () => {
    if (document.hidden) ctrl?.abort();
    else restartPush();
  };
  const onOnline = () => restartPush();
  document.addEventListener("visibilitychange", onVis);
  window.addEventListener("online", onOnline);
  void connect();
  return () => {
    stopped = true;
    document.removeEventListener("visibilitychange", onVis);
    window.removeEventListener("online", onOnline);
    window.clearTimeout(retryTimer);
    window.clearTimeout(watchdogTimer);
    ctrl?.abort();
    ctrl = null;
    healthy = false;
  };
}
