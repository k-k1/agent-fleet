// Idle pause of the push stream (issue #3235): an unattended but visible window closes the
// stream after IDLE_PAUSE_MS unless a session is working, and any input / focus /
// visibilitychange reopens it with the same full re-read a reconnect does.
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

const values = new Map<string, string>();
vi.stubGlobal("localStorage", {
  getItem: (key: string) => values.get(key) ?? null,
  setItem: (key: string, value: string) => values.set(key, value),
  removeItem: (key: string) => values.delete(key),
});
type L = (e?: unknown) => void;
const winL = new Map<string, Set<L>>();
const docL = new Map<string, Set<L>>();
const reg = (m: Map<string, Set<L>>) => ({
  addEventListener: (t: string, f: L) => (m.get(t) ?? m.set(t, new Set()).get(t)!).add(f),
  removeEventListener: (t: string, f: L) => m.get(t)?.delete(f),
});
const fire = (m: Map<string, Set<L>>, t: string) => [...(m.get(t) ?? [])].forEach((f) => f());
const doc = { baseURI: "http://localhost/", hidden: false, ...reg(docL) };
vi.stubGlobal("document", doc);
const fetchMock = vi.fn<(input: unknown, init?: unknown) => Promise<Response>>();
vi.stubGlobal("window", {
  fetch: fetchMock,
  setTimeout: (...a: Parameters<typeof setTimeout>) => globalThis.setTimeout(...a),
  clearTimeout: (...a: Parameters<typeof clearTimeout>) => globalThis.clearTimeout(...a),
  ...reg(winL),
});
vi.stubGlobal("fetch", fetchMock);

let push: typeof import("./events.ts");
beforeAll(async () => {
  push = await import("./events.ts");
});

// A stream that stays open until aborted and pings every 20s, like the real one (without
// the pings the 45s watchdog would reconnect it).
function openStream(init?: { signal?: AbortSignal }) {
  const body = new ReadableStream<Uint8Array>({
    start(c) {
      const tick = setInterval(() => c.enqueue(new TextEncoder().encode(": ping\n\n")), 20000);
      init?.signal?.addEventListener("abort", () => {
        clearInterval(tick);
        c.error(new Error("abort"));
      });
    },
  });
  return Promise.resolve(
    new Response(body, { status: 200, headers: { "Content-Type": "text/event-stream" } }),
  );
}

const settle = () => vi.advanceTimersByTimeAsync(0);
const min = (n: number) => vi.advanceTimersByTimeAsync(n * 60 * 1000);

let cleanup: (() => void) | null = null;
let connects = 0;
let offConnect: () => void;
beforeEach(() => {
  vi.useFakeTimers();
  connects = 0;
  fetchMock.mockImplementation((_u, init) => openStream(init as { signal?: AbortSignal }));
  offConnect = push.onPushConnect(() => connects++);
});
afterEach(() => {
  cleanup?.();
  cleanup = null;
  offConnect();
  fetchMock.mockReset();
  doc.hidden = false;
  vi.useRealTimers();
});

const start = async (busy = false) => {
  const offBusy = push.setPushBusyProbe(() => busy);
  const stop = push.startPushChannel();
  cleanup = () => {
    stop();
    offBusy();
  };
  await settle();
};

describe("push idle pause", () => {
  it("pauses after IDLE_PAUSE_MS without input when nothing is working", async () => {
    await start();
    expect(push.pushHealthy()).toBe(true);
    expect(connects).toBe(1);
    await vi.advanceTimersByTimeAsync(push.IDLE_PAUSE_MS - 1000);
    expect(push.pushPaused()).toBe(false);
    await vi.advanceTimersByTimeAsync(2000);
    expect(push.pushPaused()).toBe(true);
    // Pollers read pushHealthy(): it stays true so they do not take over the 4s tick.
    expect(push.pushHealthy()).toBe(true);
    await min(30); // no reconnect while paused
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("never pauses while a session is working, and pauses once it stops", async () => {
    let busy = true;
    const off = push.setPushBusyProbe(() => busy);
    cleanup = push.startPushChannel();
    await settle();
    await min(40);
    expect(push.pushPaused()).toBe(false);
    busy = false;
    await min(2);
    expect(push.pushPaused()).toBe(true);
    off();
  });

  it("input keeps the window alive: idle time counts from the last input", async () => {
    await start();
    await min(9);
    fire(winL, "keydown");
    await min(9);
    expect(push.pushPaused()).toBe(false);
    await min(2);
    expect(push.pushPaused()).toBe(true);
  });

  for (const [name, trigger] of [
    ["pointer input", () => fire(winL, "pointerdown")],
    ["key input", () => fire(winL, "keydown")],
    ["wheel", () => fire(winL, "wheel")],
    ["touch", () => fire(winL, "touchstart")],
    ["focus", () => fire(winL, "focus")],
    ["visibilitychange", () => fire(docL, "visibilitychange")],
  ] as const) {
    it(`resumes on ${name} with one reconnect (full re-read)`, async () => {
      await start();
      await min(11);
      expect(push.pushPaused()).toBe(true);
      expect(connects).toBe(1);
      trigger();
      trigger(); // a rapid second toggle must not open a second stream
      await settle();
      expect(push.pushPaused()).toBe(false);
      expect(push.pushHealthy()).toBe(true);
      expect(connects).toBe(2); // onPushConnect fired again: wire.ts re-reads sessions
      expect(fetchMock).toHaveBeenCalledTimes(2);
      await min(5);
      expect(fetchMock).toHaveBeenCalledTimes(2);
    });
  }

  it("rapid input/focus bursts while paused cause no reconnect storm", async () => {
    await start();
    await min(11);
    for (let i = 0; i < 20; i++) {
      fire(winL, "pointermove");
      fire(winL, "focus");
      fire(docL, "visibilitychange");
    }
    await settle();
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("pauses again after the next idle period", async () => {
    await start();
    await min(11);
    fire(winL, "pointerdown");
    await settle();
    expect(push.pushPaused()).toBe(false);
    await min(11);
    expect(push.pushPaused()).toBe(true);
  });

  it("notifies pause edges and cleans up listeners and timers on stop", async () => {
    const edges: boolean[] = [];
    const off = push.onPushPause(() => edges.push(push.pushPaused()));
    await start();
    await min(11);
    expect(edges).toEqual([true]);
    cleanup!();
    cleanup = null;
    expect(edges).toEqual([true, false]);
    for (const m of [winL, docL]) for (const set of m.values()) expect(set.size).toBe(0);
    expect(vi.getTimerCount()).toBe(0);
    off();
  });
});
