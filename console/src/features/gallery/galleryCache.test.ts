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
  galleryImagesURL,
  galleryKey,
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

describe("読み始めた順に一覧を書く（遅い答えが新しい一覧を上書きしない）", () => {
  /** A fetch answer the test releases by hand, so two reads can finish in either order. */
  const held = (names: string[]) => {
    let release: () => void = () => {};
    const gate = new Promise<void>((r) => (release = r));
    fetchMock.mockImplementationOnce(async () => {
      await gate;
      return {
        ok: true,
        status: 200,
        statusText: "OK",
        headers: { get: () => null },
        text: async () => JSON.stringify({ entries: names.map(entry) }),
      };
    });
    return release;
  };

  it("先に始めた先読みが遅れて返っても、後から始めた画面の読みを上書きしない", async () => {
    const releasePrefetch = held(["old.png"]);
    prefetchGallery("gen", 512); // hover: begins first, answers last
    const releaseView = held(["new.png"]);
    const view = fetchGalleryListing("gen", 512); // the pane walked in: begins second
    releaseView();
    await view;
    expect(readGallery("gen")?.entries).toEqual([entry("new.png")]);

    releasePrefetch();
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    await new Promise((r) => setTimeout(r, 0)); // let the prefetch's answer be processed
    expect(readGallery("gen")?.entries).toEqual([entry("new.png")]);
  });

  it("遅れた読みは、書かなくても自分の呼び出し元には一覧を返す", async () => {
    const releaseOld = held(["old.png"]);
    const old = fetchGalleryListing("gen", 512);
    const releaseNew = held(["new.png"]);
    const newer = fetchGalleryListing("gen", 512);
    releaseNew();
    await newer;
    releaseOld();
    expect(await old).toEqual({ ok: true, entries: [entry("old.png")] });
    expect(readGallery("gen")?.entries).toEqual([entry("new.png")]);
  });

  it("後から始めた読みが先に始めた読みより早く返れば、そのまま書く（逆順は新しい方が勝つ）", async () => {
    const releaseOld = held(["old.png"]);
    const old = fetchGalleryListing("gen", 512);
    const releaseNew = held(["new.png"]);
    const newer = fetchGalleryListing("gen", 512);
    releaseOld();
    await old;
    expect(readGallery("gen")?.entries).toEqual([entry("old.png")]); // nothing newer has landed yet
    releaseNew();
    await newer;
    expect(readGallery("gen")?.entries).toEqual([entry("new.png")]);
  });

  it("順序はキーごと（別のフォルダの新しい読みは邪魔をしない）", async () => {
    const releaseA = held(["a.png"]);
    const a = fetchGalleryListing("a", 512);
    const releaseB = held(["b.png"]);
    const b = fetchGalleryListing("b", 512);
    releaseB();
    await b;
    releaseA();
    await a;
    expect(readGallery("a")?.entries).toEqual([entry("a.png")]);
    expect(readGallery("b")?.entries).toEqual([entry("b.png")]);
  });

  it("忘れた後に、忘れる前に始めた読みの答えが来ても一覧を戻さない（「無い」と同じ答えにする）", async () => {
    const releaseOld = held(["ghost.png"]);
    const old = fetchGalleryListing("gone", 512);
    forgetGallery("gone"); // a first read answered "not there"
    releaseOld();
    // Not shown either: a grid with no cache entry would never remember its scroll position.
    expect(await old).toEqual({ ok: false, retry: false, hard: true });
    expect(readGallery("gone")).toBeUndefined();

    // A read that begins after the folder was forgotten is a fresh start and does write.
    await fetchGalleryListing("gone", 512);
    expect(readGallery("gone")).toBeDefined();
  });

  it("読みが出ている間は、他の 30 フォルダを忘れても忘れた印は消えない", async () => {
    const releaseOld = held(["ghost.png"]);
    const old = fetchGalleryListing("gone", 512);
    forgetGallery("gone");
    for (let i = 0; i < 40; i++) forgetGallery(`other${i}`);
    releaseOld();
    expect(await old).toEqual({ ok: false, retry: false, hard: true });
    expect(readGallery("gone")).toBeUndefined();
  });

  it("読みが出ている間に新しい一覧が LRU で追い出されても、古い答えで書き戻さない", async () => {
    const releaseOld = held(["old.png"]);
    const old = fetchGalleryListing("gen", 512);
    await fetchGalleryListing("gen", 512); // newer read, cached
    for (let i = 0; i < 30; i++) writeGallery(`f${i}`, [entry(`${i}.png`)]); // pushes "gen" out
    expect(readGallery("gen")).toBeUndefined();
    releaseOld();
    expect(await old).toEqual({ ok: true, entries: [entry("old.png")] });
    expect(readGallery("gen")).toBeUndefined();
  });

  it("読みが全部終わって一覧も無いフォルダは、次の読みが普通に書ける", async () => {
    await fetchGalleryListing("gen", 512);
    for (let i = 0; i < 30; i++) writeGallery(`f${i}`, [entry(`${i}.png`)]);
    expect(readGallery("gen")).toBeUndefined();
    await fetchGalleryListing("gen", 512);
    expect(readGallery("gen")).toBeDefined();
  });

  it("writeGallery 単体は常に最新の読みとして書く", () => {
    writeGallery("gen", [entry("a.png")]);
    writeGallery("gen", [entry("b.png")]);
    expect(readGallery("gen")?.entries).toEqual([entry("b.png")]);
  });
});

describe("サブフォルダもまとめた一覧（flat）", () => {
  it("同じフォルダでも別の一覧として覚える（一段の一覧と混ざらない）", async () => {
    const answer = async (url?: unknown) => ({
      ok: true,
      status: 200,
      statusText: "OK",
      headers: { get: () => null },
      text: async () =>
        JSON.stringify(
          String(url).includes("fs/images")
            ? { entries: [entry("sub/a.png")], truncated: true }
            : { entries: [entry("top.png")] },
        ),
    });
    // Once per call, so the file's default answer is back for whatever runs next.
    fetchMock.mockImplementationOnce(answer).mockImplementationOnce(answer);
    const flat = await fetchGalleryListing("gen", 256, undefined, true);
    expect(flat).toEqual({ ok: true, entries: [entry("sub/a.png")], truncated: true });
    expect(String(fetchMock.mock.calls[0][0])).toContain(galleryImagesURL("gen", 256));

    await fetchGalleryListing("gen", 256);
    expect(readGallery(galleryKey("gen", true))?.entries).toEqual([entry("sub/a.png")]);
    expect(readGallery(galleryKey("gen", true))?.truncated).toBe(true);
    expect(readGallery("gen")?.entries).toEqual([entry("top.png")]);
    expect(galleryKey("gen", false)).toBe("gen");
    expect(galleryKey("gen", true)).not.toBe("gen");
  });

  it("URL は深さと上限を付けて fs/images を呼ぶ", () => {
    const u = galleryImagesURL("a b", 512);
    expect(u.startsWith("api/fs/images?path=a%20b&")).toBe(true);
    expect(u).toMatch(/depth=\d+/);
    expect(u).toMatch(/limit=\d+/);
    expect(u).toContain("warm=512");
  });
});
