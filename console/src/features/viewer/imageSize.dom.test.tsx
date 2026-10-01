// The W×H loader's promise to the Agent (imageSize.ts): many asks become few requests, never
// more than one in flight, and an answer is remembered per (path, mtime).
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { BATCH_MAX, clearImageSizeCache, imageSize, knownImageSize } from "./imageSize.ts";

let bodies: string[][] = [];
let inFlight = 0;
let peak = 0;
let fail = false;
let latency = 5;
/** Answer like the CP does while the agent restarts: a 503 with an error body. */
let unavailable = false;

const answer = (paths: string[]) => Object.fromEntries(paths.filter((p) => p.endsWith(".png")).map((p) => [p, { w: 832, h: 1216 }]));

const fetchMock = vi.fn(async (_url: string, opts?: RequestInit) => {
  const paths = JSON.parse(String(opts?.body)).paths as string[];
  bodies.push(paths);
  inFlight++;
  peak = Math.max(peak, inFlight);
  await new Promise((r) => setTimeout(r, latency));
  inFlight--;
  if (fail) throw new TypeError("network");
  if (unavailable)
    return {
      ok: false,
      status: 503,
      statusText: "Service Unavailable",
      headers: { get: () => null },
      text: async () => JSON.stringify({ error: { code: "agent_unavailable" } }),
    } as unknown as Response;
  return {
    ok: true,
    status: 200,
    statusText: "OK",
    headers: { get: () => null },
    text: async () => JSON.stringify({ sizes: answer(paths) }),
  } as unknown as Response;
});

beforeEach(() => {
  vi.stubGlobal("fetch", fetchMock);
  clearImageSizeCache();
  bodies = [];
  inFlight = 0;
  peak = 0;
  fail = false;
  unavailable = false;
  latency = 5;
  fetchMock.mockClear();
});

afterEach(() => vi.unstubAllGlobals());

describe("画像の W×H をまとめて聞く", () => {
  it("同じ瞬間の問い合わせは 1 本の要求に相乗りする", async () => {
    const got = await Promise.all(["a.png", "b.png", "c.txt"].map((p) => imageSize(p, 1)));
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(String(fetchMock.mock.calls[0][0])).toContain("api/fs/imagesize");
    expect(got).toEqual([{ w: 832, h: 1216 }, { w: 832, h: 1216 }, null]);
  });

  it("500 枚でも BATCH_MAX ずつ、同時に 1 本しか飛ばない", async () => {
    const paths = Array.from({ length: 500 }, (_, i) => `gen/${i}.png`);
    await Promise.all(paths.map((p) => imageSize(p)));
    expect(bodies.length).toBe(Math.ceil(500 / BATCH_MAX));
    expect(bodies.every((b) => b.length <= BATCH_MAX)).toBe(true);
    expect(peak).toBe(1);
  });

  it("要求が返る前に来た問い合わせは、それが返るまで待って次の 1 本に乗る", async () => {
    latency = 80;
    const first = imageSize("a.png");
    await new Promise((r) => setTimeout(r, 50)); // the first request is out now
    const later = ["b.png", "c.png"].map((p) => imageSize(p));
    await new Promise((r) => setTimeout(r, 50)); // past the batch window, first still out
    await Promise.all([first, ...later]);
    expect(peak).toBe(1);
    expect(bodies).toEqual([["a.png"], ["b.png", "c.png"]]);
  });

  it("答えは (path, mtime) で覚え、二度目は聞かない。mtime が変われば聞き直す", async () => {
    await imageSize("a.png", 1);
    await imageSize("a.png", 1);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(knownImageSize("a.png", 1)).toEqual({ w: 832, h: 1216 });
    await imageSize("a.png", 2);
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("通信の失敗は「大きさが無い」として覚えない（次に見たとき聞き直す）", async () => {
    fail = true;
    expect(await imageSize("a.png", 1)).toBeNull();
    expect(knownImageSize("a.png", 1)).toBeUndefined();
    fail = false;
    expect(await imageSize("a.png", 1)).toEqual({ w: 832, h: 1216 });
  });

  it("エラーの応答（エージェント再起動中の 503）も覚えない", async () => {
    unavailable = true;
    expect(await imageSize("b.png", 1)).toBeNull();
    expect(knownImageSize("b.png", 1)).toBeUndefined();
    unavailable = false;
    expect(await imageSize("b.png", 1)).toEqual({ w: 832, h: 1216 });
  });

  it("問い合わせ中の同じ絵は二重に聞かない", async () => {
    const [a, b] = await Promise.all([imageSize("a.png"), imageSize("a.png")]);
    expect(a).toEqual(b);
    expect(bodies).toEqual([["a.png"]]);
  });
});
