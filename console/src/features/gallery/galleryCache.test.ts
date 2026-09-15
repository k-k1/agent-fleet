// What the gallery remembers between two visits to the same folder (ADR 0080 P2). The rules
// worth pinning here are the ones whose failure is silent: a bound that never evicts, a place
// remembered for a folder that was never read, and a prefetch that asks again for something it
// already has (hovering along a row of folder cards is a stream of pointer events).
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// The api client reads the tenant out of storage, resolves URLs against document.baseURI and
// WRAPS window.fetch, all at module load (client.ts) — so a node-project suite has to hand it a
// browser-shaped shell before importing anything that pulls it in. The shell is the whole
// browser this file needs: the cache is a Map plus one request.
const fetchMock = vi.fn(async (_url?: unknown) => ({
  ok: true,
  status: 200,
  statusText: "OK",
  headers: { get: () => null },
  text: async () => JSON.stringify({ entries: [{ name: "a.png", type: "file", size: 10 }] }),
}));
vi.stubGlobal("localStorage", { getItem: () => null, setItem: () => {}, removeItem: () => {} });
vi.stubGlobal("sessionStorage", { getItem: () => null, setItem: () => {}, removeItem: () => {} });
vi.stubGlobal("document", { baseURI: "http://af.test/" });
// Both: client.ts binds `window.fetch` at load (for its 401 latch) but api() calls the bare
// global. Stubbing only one of them leaves the requests going to node's real fetch, where they
// fail and every result reads as a transport error.
vi.stubGlobal("window", { fetch: fetchMock });
vi.stubGlobal("fetch", fetchMock);

const {
  clearGalleryCache,
  fetchGalleryListing,
  forgetGallery,
  galleryTreeURL,
  prefetchGallery,
  readGallery,
  rememberGalleryView,
  writeGallery,
} = await import("./galleryCache.ts");
const { PAGE_SIZE } = await import("./gallery.ts");

const entry = (name: string) => ({ name, type: "file", size: 1 });

beforeEach(() => {
  clearGalleryCache();
  fetchMock.mockClear();
});

afterEach(() => {
  vi.useRealTimers();
});

describe("ギャラリーのフォルダキャッシュ", () => {
  it("一覧と、そのフォルダでの居場所を覚える", () => {
    writeGallery("gen", [entry("a.png")]);
    expect(readGallery("gen")?.entries).toEqual([entry("a.png")]);
    expect(readGallery("gen")?.limit).toBe(PAGE_SIZE);

    rememberGalleryView("gen", { scrollTop: 420, limit: 600 });
    expect(readGallery("gen")).toMatchObject({ scrollTop: 420, limit: 600 });

    // A refresh that lands one new picture must not send the reader back to the top.
    writeGallery("gen", [entry("a.png"), entry("b.png")]);
    expect(readGallery("gen")).toMatchObject({ scrollTop: 420, limit: 600 });
  });

  it("読んでいないフォルダの居場所は覚えない（一覧の無い場所へは戻れない）", () => {
    rememberGalleryView("never-read", { scrollTop: 99 });
    expect(readGallery("never-read")).toBeUndefined();
  });

  it("30 フォルダで打ち切り、捨てるのは一番長く見ていないもの", () => {
    for (let i = 0; i < 30; i++) writeGallery(`f${i}`, [entry(`${i}.png`)]);
    expect(readGallery("f0")).toBeDefined(); // reading it makes f0 the most recently used
    writeGallery("f30", [entry("30.png")]);
    expect(readGallery("f0")).toBeDefined();
    expect(readGallery("f1")).toBeUndefined(); // the oldest USE, not the oldest write
    expect(readGallery("f30")).toBeDefined();
  });

  it("忘れると次は空から読む（消えたフォルダの一覧を持ち続けない）", () => {
    writeGallery("gone", [entry("a.png")]);
    forgetGallery("gone");
    expect(readGallery("gone")).toBeUndefined();
  });

  it("一覧を読むと覚え、答えの種類で「もう一度試すか」を分ける", async () => {
    const ok = await fetchGalleryListing("gen", 512);
    expect(ok).toEqual({ ok: true, entries: [{ name: "a.png", type: "file", size: 10 }] });
    expect(readGallery("gen")?.entries).toHaveLength(1);
    expect(String(fetchMock.mock.calls[0][0])).toContain(galleryTreeURL("gen", 512));

    // A 5xx is the CP's answer while the agent restarts: worth another try, and nothing on
    // screen should change.
    fetchMock.mockImplementationOnce(async () => ({
      ok: false,
      status: 502,
      statusText: "Bad Gateway",
      headers: { get: () => null },
      text: async () => JSON.stringify({ error: { code: "agent_unreachable" } }),
    }));
    expect(await fetchGalleryListing("gen", 512)).toEqual({ ok: false, retry: true, hard: false });

    // "The folder is not there" is settled, and is the one answer a caller must not paper over.
    fetchMock.mockImplementationOnce(async () => ({
      ok: false,
      status: 404,
      statusText: "Not Found",
      headers: { get: () => null },
      text: async () => JSON.stringify({ error: { code: "not_dir" } }),
    }));
    expect(await fetchGalleryListing("gen", 512)).toEqual({ ok: false, retry: false, hard: true });

    // A transport failure is not an answer at all.
    fetchMock.mockImplementationOnce(() => Promise.reject(new Error("offline")) as never);
    expect(await fetchGalleryListing("gen", 512)).toEqual({ ok: false, retry: true, hard: false });
  });

  it("指しただけの先読みは 1 本にまとめ、さっき読んだフォルダには行かない", async () => {
    prefetchGallery("gen", 512);
    prefetchGallery("gen", 512); // the pointer is still moving over the same card
    expect(fetchMock).toHaveBeenCalledTimes(1);
    await vi.waitFor(() => expect(readGallery("gen")).toBeDefined());

    prefetchGallery("gen", 512); // read a moment ago: the click itself will refresh it
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("先読みが失敗しても黙って終わる（当たらなかった先読みの費用は要求 1 本きり）", async () => {
    fetchMock.mockImplementationOnce(() => Promise.reject(new Error("offline")) as never);
    prefetchGallery("nope", 512);
    await new Promise((r) => setTimeout(r, 0)); // let the request settle, not just fire
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(readGallery("nope")).toBeUndefined();
    // And the folder is not left marked as in flight — the next pointer may try again.
    prefetchGallery("nope", 512);
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });
});
