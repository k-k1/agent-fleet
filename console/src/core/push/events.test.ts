// Contract test for the unified push channel's receive hub (traffic reduction P3). Pins the
// per-stream dispatch, reassembly across chunk boundaries, the pushHealthy/pushStamp
// lifecycle, the old-CP (404) fallback and the isolation of a throwing handler.
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";

// events.ts imports api/client.ts, which binds window.fetch and reads document.baseURI, so
// the globals are stubbed before the import, as in repos/store.test.ts.
const values = new Map<string, string>();
vi.stubGlobal("localStorage", {
  getItem: (key: string) => values.get(key) ?? null,
  setItem: (key: string, value: string) => values.set(key, value),
  removeItem: (key: string) => values.delete(key),
});
const noopListeners = { addEventListener: () => {}, removeEventListener: () => {} };
vi.stubGlobal("document", { baseURI: "http://localhost/", hidden: false, ...noopListeners });
const fetchMock = vi.fn<(input: unknown, init?: unknown) => Promise<Response>>();
vi.stubGlobal("window", {
  fetch: fetchMock,
  setTimeout: globalThis.setTimeout.bind(globalThis),
  clearTimeout: globalThis.clearTimeout.bind(globalThis),
  ...noopListeners,
});
vi.stubGlobal("fetch", fetchMock);

let push: typeof import("./events.ts");
beforeAll(async () => {
  push = await import("./events.ts");
});

const enc = new TextEncoder();

// A hand-driven SSE response: enqueue pushes arbitrary bytes, close drops the connection.
function sseResponse() {
  let ctrl!: ReadableStreamDefaultController<Uint8Array>;
  const body = new ReadableStream<Uint8Array>({ start: (c) => (ctrl = c) });
  const res = new Response(body, {
    status: 200,
    headers: { "Content-Type": "text/event-stream; charset=utf-8" },
  });
  return {
    res,
    enqueue: (s: string) => ctrl.enqueue(enc.encode(s)),
    close: () => ctrl.close(),
  };
}

const frame = (stream: string, data: unknown) =>
  `data: ${JSON.stringify({ stream, data })}\n\n`;

// Turn the microtask + macrotask queues a few times so the reader loop catches up.
const settle = async () => {
  for (let i = 0; i < 5; i++) await new Promise((r) => setTimeout(r, 0));
};

let cleanup: (() => void) | null = null;
afterEach(() => {
  cleanup?.();
  cleanup = null;
  fetchMock.mockReset();
});

describe("push events hub", () => {
  it("delivers frames per stream, tracks health and stamps, ignores pings", async () => {
    const sse = sseResponse();
    fetchMock.mockResolvedValue(sse.res);
    const got: unknown[] = [];
    const un = push.onPush("sessions", (d) => got.push(d));
    const stamp0 = push.pushStamp("sessions");

    cleanup = push.startPushChannel();
    await settle();
    expect(push.pushHealthy()).toBe(true);

    sse.enqueue(frame("sessions", { sessions: [{ name: "s1" }] }));
    sse.enqueue(": ping\n\n");
    sse.enqueue(frame("workspace", { state: "running" }));
    await settle();
    expect(got).toEqual([{ sessions: [{ name: "s1" }] }]);
    expect(push.pushStamp("sessions")).toBe(stamp0 + 1);

    // A disconnect falls back: healthy drops, so the pollers take over from the next tick.
    sse.close();
    await settle();
    expect(push.pushHealthy()).toBe(false);
    un();
  });

  it("reassembles a frame split across chunks", async () => {
    const sse = sseResponse();
    fetchMock.mockResolvedValue(sse.res);
    const got: unknown[] = [];
    const un = push.onPush("stats", (d) => got.push(d));

    cleanup = push.startPushChannel();
    await settle();
    const whole = frame("stats", { running: true, mem_used: 8388608 });
    sse.enqueue(whole.slice(0, 20));
    await settle();
    expect(got).toEqual([]); // a partial frame is not applied yet
    sse.enqueue(whole.slice(20));
    await settle();
    expect(got).toEqual([{ running: true, mem_used: 8388608 }]);
    un();
  });

  it("stays unhealthy against an old CP (404) — pollers keep the wheel", async () => {
    fetchMock.mockResolvedValue(new Response("not found", { status: 404 }));
    cleanup = push.startPushChannel();
    await settle();
    expect(push.pushHealthy()).toBe(false);
    expect(fetchMock).toHaveBeenCalledTimes(1); // no immediate retry storm; re-check in 5 min
  });

  // Reconnect hook: the entry point for re-reading state that is only read at boot (whoami's
  // deployment capabilities) after a CP restart. Fires on every connect, never on disconnect.
  it("fires connect handlers on every (re)connection, not on disconnect", async () => {
    const sse1 = sseResponse();
    fetchMock.mockResolvedValue(sse1.res);
    let connects = 0;
    const un = push.onPushConnect(() => connects++);

    cleanup = push.startPushChannel();
    await settle();
    expect(connects).toBe(1);

    sse1.close(); // a disconnect alone does not increment
    await settle();
    expect(connects).toBe(1);

    const sse2 = sseResponse();
    fetchMock.mockResolvedValue(sse2.res);
    push.restartPush();
    await settle();
    expect(connects).toBe(2);

    un();
    const sse3 = sseResponse();
    fetchMock.mockResolvedValue(sse3.res);
    push.restartPush();
    await settle();
    expect(connects).toBe(2); // not called after unsubscribing
  });

  it("isolates a throwing connect handler from the stream", async () => {
    const sse = sseResponse();
    fetchMock.mockResolvedValue(sse.res);
    const un1 = push.onPushConnect(() => {
      throw new Error("boom");
    });
    let ok = 0;
    const un2 = push.onPushConnect(() => ok++);

    cleanup = push.startPushChannel();
    await settle();
    expect(ok).toBe(1);
    expect(push.pushHealthy()).toBe(true);
    un1();
    un2();
  });

  it("isolates a throwing handler from the rest", async () => {
    const sse = sseResponse();
    fetchMock.mockResolvedValue(sse.res);
    const got: unknown[] = [];
    const un1 = push.onPush("workspace", () => {
      throw new Error("boom");
    });
    const un2 = push.onPush("workspace", (d) => got.push(d));

    cleanup = push.startPushChannel();
    await settle();
    sse.enqueue(frame("workspace", { state: "running" }));
    await settle();
    expect(got).toEqual([{ state: "running" }]);
    expect(push.pushHealthy()).toBe(true); // a throwing handler does not kill the stream
    un1();
    un2();
  });
});
