// The W×H loader's promise to the Agent (imageSize.ts): many asks become few requests, never
// more than one in flight, and an answer is remembered per (path, mtime).
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { BATCH_MAX, clearImageSizeCache, imageSize, knownImageSize, useImageSize } from "./imageSize.ts";
import { setTenant } from "../../core/api/client.ts";

let bodies: string[][] = [];
/** The X-AF-Tenant each request carried, in order. */
let tenants: (string | undefined)[] = [];
/** What a file measures; tenant "b" holds a different picture under the same path. */
let dims = { w: 832, h: 1216 };
let inFlight = 0;
let peak = 0;
let fail = false;
let latency = 5;
/** Answer like the CP does while the agent restarts: a 503 with an error body. */
let unavailable = false;

const answer = (paths: string[], tenant?: string) =>
  Object.fromEntries(paths.filter((p) => p.endsWith(".png")).map((p) => [p, tenant === "b" ? { w: 100, h: 50 } : dims]));

const fetchMock = vi.fn(async (_url: string, opts?: RequestInit) => {
  const paths = JSON.parse(String(opts?.body)).paths as string[];
  const tenant = new Headers(opts?.headers).get("X-AF-Tenant") ?? undefined;
  bodies.push(paths);
  tenants.push(tenant);
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
    text: async () => JSON.stringify({ sizes: answer(paths, tenant) }),
  } as unknown as Response;
});

beforeEach(() => {
  vi.stubGlobal("fetch", fetchMock);
  clearImageSizeCache();
  bodies = [];
  tenants = [];
  dims = { w: 832, h: 1216 };
  setTenant("a");
  inFlight = 0;
  peak = 0;
  fail = false;
  unavailable = false;
  latency = 5;
  fetchMock.mockClear();
});

afterEach(() => {
  setTenant("");
  vi.unstubAllGlobals();
});

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

  it("テナントを切り替えたら前のテナントの答えを使わず、切り替え後の要求はそのテナントへ送る", async () => {
    expect(await imageSize("pics/a.png", 1)).toEqual({ w: 832, h: 1216 });
    setTenant("b");
    expect(knownImageSize("pics/a.png", 1)).toBeUndefined();
    expect(await imageSize("pics/a.png", 1)).toEqual({ w: 100, h: 50 });
    expect(tenants).toEqual(["a", "b"]);
  });

  it("待ち窓の途中で切り替えても、A の問い合わせは A に、B のは B に別々の要求で送る", async () => {
    const fromA = imageSize("pics/a.png", 1);
    setTenant("b");
    const fromB = imageSize("pics/a.png", 1);
    expect(await fromA).toEqual({ w: 832, h: 1216 });
    expect(await fromB).toEqual({ w: 100, h: 50 });
    expect(tenants).toEqual(["a", "b"]);
    expect(knownImageSize("pics/a.png", 1)).toEqual({ w: 100, h: 50 });
    setTenant("a");
    expect(knownImageSize("pics/a.png", 1)).toEqual({ w: 832, h: 1216 });
  });

  it("要求中に切り替えて遅れて返った答えは、切り替え後のテナントには入らない", async () => {
    latency = 80;
    const fromA = imageSize("pics/a.png", 1);
    await new Promise((r) => setTimeout(r, 50)); // out, under tenant a
    setTenant("b");
    await fromA;
    expect(knownImageSize("pics/a.png", 1)).toBeUndefined();
  });

  it("mtime の無い答えは覚えない（同じパスが上書きされても次に見たとき新しい寸法になる）", async () => {
    expect(await imageSize("shared.png")).toEqual({ w: 832, h: 1216 });
    expect(knownImageSize("shared.png")).toBeUndefined();
    dims = { w: 640, h: 480 };
    expect(await imageSize("shared.png")).toEqual({ w: 640, h: 480 });
    expect(bodies.length).toBe(2);
  });

  it("mtime の無いフックは開き直すたびに聞き直す（ミラーの拡大の (i)）", async () => {
    const host = document.createElement("div");
    document.body.appendChild(host);
    const Probe = () => {
      const s = useImageSize("shared.png");
      return <span>{s ? `${s.w}x${s.h}` : "-"}</span>;
    };
    const mountOnce = async () => {
      const root = createRoot(host);
      await act(async () => root.render(<Probe />));
      await act(async () => {
        await new Promise((r) => setTimeout(r, 60));
      });
      const text = host.textContent;
      await act(async () => root.unmount());
      return text;
    };
    expect(await mountOnce()).toBe("832x1216");
    dims = { w: 640, h: 480 };
    expect(await mountOnce()).toBe("640x480");
    host.remove();
  });

  // The batch window's timer must not outlive the mounts that asked: a test (or a gallery
  // scrolled away) that unmounts inside the 30 ms window would otherwise fire a request later,
  // in the case of a torn-down jsdom as an unhandled "document is not defined".
  describe("待っている間に外れた問い合わせ", () => {
    const Probe = ({ path }: { path: string }) => {
      const s = useImageSize(path);
      return <span>{s ? `${s.w}x${s.h}` : "-"}</span>;
    };
    const settle = () => act(async () => void (await new Promise((r) => setTimeout(r, 80))));

    it("窓が閉じる前に全員がアンマウントしたら、要求もタイマーも残らない", async () => {
      const root = createRoot(document.createElement("div"));
      await act(async () => root.render(<Probe path="gone.png" />));
      await act(async () => root.unmount());
      await settle();
      expect(fetchMock).not.toHaveBeenCalled();
    });

    it("1 人でも残っていれば聞く。imageSize() の直接の呼び出しも消えない", async () => {
      const a = createRoot(document.createElement("div"));
      const b = createRoot(document.createElement("div"));
      await act(async () => {
        a.render(<Probe path="kept.png" />);
        b.render(<Probe path="kept.png" />);
      });
      const direct = imageSize("pinned.png");
      await act(async () => b.unmount());
      await settle();
      expect(await direct).toEqual({ w: 832, h: 1216 });
      expect(bodies).toEqual([["kept.png", "pinned.png"]]);
      await act(async () => a.unmount());
    });

    it("古い hook の後片付けは、同じキーの新しい問い合わせを取り消さない", async () => {
      const hostA = document.createElement("div");
      const hostB = document.createElement("div");
      const a = createRoot(hostA);
      const b = createRoot(hostB);
      await act(async () => a.render(<Probe path="same.png" />));
      await settle();
      expect(hostA.textContent).toBe("832x1216");
      dims = { w: 10, h: 20 };
      await act(async () => b.render(<Probe path="same.png" />));
      await act(async () => a.unmount());
      await settle();
      expect(fetchMock).toHaveBeenCalledTimes(2);
      expect(hostB.textContent).toBe("10x20");
      await act(async () => b.unmount());
    });
  });
});
