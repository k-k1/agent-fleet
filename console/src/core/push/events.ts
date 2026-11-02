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

export type PushStream = "workspace" | "sessions" | "stats" | "notifications" | "workitems" | "engines";
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
// True while the stream is deliberately closed for lack of a reader (see the idle pause
// below). Pollers must stand down then too, or the pause would just move the 4s GET
// /sessions from the CP's tick to the Console's own fallback poll.
let paused = false;
/** True while the push stream is live, or deliberately paused for idleness — pollers skip
 * their tick in both. */
export const pushHealthy = (): boolean => healthy || paused;
/** True while the stream is closed because the window has been unattended. */
export const pushPaused = (): boolean => paused;

const pauseHandlers = new Set<() => void>();
/** Subscribe to pause/resume edges (the "paused — click to resume" note). Returns the
 * unsubscriber. */
export function onPushPause(h: () => void): () => void {
  pauseHandlers.add(h);
  return () => {
    pauseHandlers.delete(h);
  };
}
function setPaused(v: boolean): void {
  if (paused === v) return;
  paused = v;
  for (const h of pauseHandlers) h();
}

// Idle pause. A visible window never turns document.hidden when it sits on a second
// monitor, is partly covered, or runs on Linux Chromium, so the hidden-tab close alone
// leaves an unattended Console costing the CP a 4s Agent tick forever. After IDLE_PAUSE_MS
// without input, and only while nothing is working, the stream is closed through the same
// path as a hidden tab; any input, focus or visibilitychange reopens it.
// 10 min: long enough that reading a long answer or a build log without touching the
// mouse never trips it (a working session blocks it anyway), short enough that a window
// left behind stops costing within the coffee break.
export const IDLE_PAUSE_MS = 10 * 60 * 1000;
const IDLE_RECHECK_MS = 60 * 1000; // re-ask "is anything working?" this often while blocked
const INPUT_THROTTLE_MS = 1000; // a mousemove burst costs one timestamp write per second

let busyProbe: () => boolean = () => false;
/** Register the "is any session working?" probe. A hook so this module imports no store
 * (wire.ts supplies it). Returns the unsubscriber. */
export function setPushBusyProbe(fn: () => boolean): () => void {
  busyProbe = fn;
  return () => {
    if (busyProbe === fn) busyProbe = () => false;
  };
}

let resumeHook: (() => void) | null = null;
/** Leave the idle pause now (the note's click). A no-op when not paused. */
export function resumePush(): void {
  if (paused) resumeHook?.();
}

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
  if (stopped || document.hidden || paused || ctrl) return;
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
    if (!stopped && !document.hidden && !paused) {
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
 * (the fallback pollers keep their existing hidden-tab behavior) or unattended, reconnects
 * on network recovery. Returns the cleanup — StrictMode-safe. */
export function startPushChannel(): () => void {
  stopped = false;
  setPaused(false);
  let lastInput = Date.now();
  let lastStamp = 0;
  let idleTimer = 0;

  const armIdle = (delay: number) => {
    window.clearTimeout(idleTimer);
    idleTimer = window.setTimeout(checkIdle, delay);
  };
  const checkIdle = () => {
    if (stopped) return;
    const left = IDLE_PAUSE_MS - (Date.now() - lastInput);
    if (left > 0) return armIdle(left);
    if (document.hidden || paused) return; // hidden has its own path; resume re-arms
    if (busyProbe()) return armIdle(IDLE_RECHECK_MS);
    // No live stream (old CP's 404, auth or network trouble) means the REST pollers are the
    // only thing keeping the data fresh and there is no CP tick to save: do not stop them.
    if (!healthy) return armIdle(IDLE_RECHECK_MS);
    setPaused(true);
    window.clearTimeout(retryTimer);
    ctrl?.abort();
  };
  const resume = () => {
    lastInput = Date.now();
    if (!paused) return;
    setPaused(false);
    restartPush(); // connect() fires onPushConnect: the full re-read
    armIdle(IDLE_PAUSE_MS);
  };
  const onInput = () => {
    if (paused) return resume();
    const now = Date.now();
    if (now - lastStamp < INPUT_THROTTLE_MS) return;
    lastStamp = now;
    lastInput = now;
  };
  const onVis = () => {
    if (document.hidden) {
      // Hand the notification / list fallback pollers their hidden-tab cadence back: a
      // paused flag left set would keep pushHealthy() true and silence them.
      setPaused(false);
      ctrl?.abort();
      return;
    }
    if (paused) resume();
    else {
      lastInput = Date.now();
      // A live stream stays: re-opening it on every visibilitychange made a rapid
      // toggle drop and re-dial the connection each time.
      if (!ctrl) restartPush();
      armIdle(IDLE_PAUSE_MS);
    }
  };
  const onFocus = () => {
    if (paused) resume();
    else lastInput = Date.now();
  };
  const onOnline = () => restartPush();
  const inputEvents = ["pointerdown", "pointermove", "keydown", "wheel", "touchstart"] as const;
  const passive = { passive: true, capture: true } as const;
  for (const ev of inputEvents) window.addEventListener(ev, onInput, passive);
  window.addEventListener("focus", onFocus);
  document.addEventListener("visibilitychange", onVis);
  window.addEventListener("online", onOnline);
  resumeHook = resume;
  armIdle(IDLE_PAUSE_MS);
  void connect();
  return () => {
    stopped = true;
    resumeHook = null;
    for (const ev of inputEvents) window.removeEventListener(ev, onInput, passive);
    window.removeEventListener("focus", onFocus);
    document.removeEventListener("visibilitychange", onVis);
    window.removeEventListener("online", onOnline);
    window.clearTimeout(idleTimer);
    window.clearTimeout(retryTimer);
    window.clearTimeout(watchdogTimer);
    ctrl?.abort();
    ctrl = null;
    healthy = false;
    setPaused(false);
  };
}
