// 統合 push チャネル受信ハブ（通信量削減 P3）の契約テスト。SSE フレームの
// 分配・チャンク跨ぎ再組み立て・pushHealthy/pushStamp のライフサイクル・
// 旧 CP(404) フォールバック・ハンドラ例外の隔離を固定する。
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";

// events.ts は api/client.ts (window.fetch 束縛・document.baseURI) を import
// するので、repos/store.test.ts と同じくグローバルを先に stub してから import。
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

// 手動制御の SSE レスポンス: enqueue で任意のバイト列を流し、close で切断。
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

// マイクロタスク+マクロタスクを数回回して reader ループに追いつかせる。
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

    // 切断でフォールバックへ: healthy が下りる（ポーラーが次 tick から引き継ぐ）。
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
    expect(got).toEqual([]); // 半端なフレームはまだ適用されない
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
    expect(fetchMock).toHaveBeenCalledTimes(1); // 即リトライの嵐にしない（5 分後に再確認）
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
    expect(push.pushHealthy()).toBe(true); // ハンドラ例外でストリームは死なない
    un1();
    un2();
  });
});
