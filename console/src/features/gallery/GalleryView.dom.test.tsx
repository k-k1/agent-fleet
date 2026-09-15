// Render tests for the gallery pane (ADR 0080). What they pin down is the part a pure test
// cannot see: that a card is really two targets (body = enlarge, corner = pane), that the
// thumbnails are asked for at the shared size and lazily, that a folder whose Agent sends no
// mtime still draws — and that the lightbox pages through the same order the grid shows.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

interface Entry {
  name: string;
  type: string;
  size?: number;
  mtime?: number;
}

let served: Entry[] = [];
let listings = 0;

// The api client talks to global fetch; stubbing there (rather than mocking the module)
// keeps the stores this view reads — layout, workspace, sessions — the real ones.
const fetchMock = vi.fn(async (url: string) => {
  if (String(url).includes("fs/tree")) listings++;
  return {
    ok: true,
    status: 200,
    statusText: "OK",
    headers: { get: () => null },
    text: async () => JSON.stringify({ entries: served }),
  } as unknown as Response;
});
vi.stubGlobal("fetch", fetchMock);

const { GalleryView } = await import("./GalleryView.tsx");
const { useLayoutStore, wireLayoutHistory } = await import("../../layout/store.ts");
const { useWorkspaceStore } = await import("../../core/store/workspace.ts");
const { allViews, freshLayout } = await import("../../layout/ops.ts");
const { useSessionsStore } = await import("../sessions/store.ts");
const { WORKING_TICK_MS } = await import("../files/refreshPolicy.ts");
const { clearGalleryCache, readGallery } = await import("./galleryCache.ts");

let host: HTMLDivElement;
let root: Root;

const img = (name: string, mtime?: number, size = 1000): Entry => ({ name, type: "file", size, ...(mtime ? { mtime } : {}) });

/** Open a real gallery pane so the view's writes (sort, focus consumption) land somewhere
 *  observable, and return its id. */
const paneWithGallery = (path: string, focus?: string): string => {
  useLayoutStore.getState().openTarget({ content: { kind: "gallery", galleryPath: path, ...(focus ? { galleryFocus: focus } : {}) } });
  const view = allViews(useLayoutStore.getState().layout).find((v) => v.content.kind === "gallery");
  return view!.id;
};

const render = async (props: Partial<Parameters<typeof GalleryView>[0]> = {}) => {
  const path = props.path ?? "gen";
  const paneId = props.paneId ?? paneWithGallery(path);
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root.render(<GalleryView paneId={paneId} path={path} {...props} />);
  });
  return paneId;
};

// Image cards only: the grid now starts with "Up" and the subfolders, and every assertion
// below is about pictures. Folder cards have their own test.
const cards = () => [...host.querySelectorAll<HTMLElement>(".gal-card:not(.folder)")];
const folderCards = () => [...host.querySelectorAll<HTMLElement>(".gal-card.folder")];
const folderNames = () => folderCards().map((c) => c.querySelector(".gal-name")?.textContent);
const names = () => cards().map((c) => c.querySelector(".gal-name")?.textContent);
const thumbs = () => [...host.querySelectorAll<HTMLImageElement>(".gal-thumb img")];
const lightbox = () => document.querySelector(".mirror-lightbox");
const byText = (text: string) =>
  [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.includes(text));

const click = async (el: Element | null | undefined) => {
  if (!el) throw new Error("not in the DOM");
  await act(async () => {
    el.dispatchEvent(new MouseEvent("click", { bubbles: true }));
  });
};

/** popstate arrives asynchronously even in jsdom (history.back() is queued as a task) — same
 *  helper layout/history.dom.test.tsx uses. */
const back = (): Promise<void> =>
  new Promise((resolve) => {
    window.addEventListener("popstate", () => setTimeout(resolve, 0), { once: true });
    history.back();
  });

beforeEach(() => {
  listings = 0;
  fetchMock.mockClear();
  // The folder cache is module-level and deliberately outlives a mount (that is what makes
  // walking back into a folder free) — so it also outlives a TEST unless it is cleared, and the
  // next one would paint the previous one's `served` listing before its own arrives.
  clearGalleryCache();
  useWorkspaceStore.setState({ state: "running" });
  // A fresh layout per test: openTarget dedupes galleries by folder, so a pane left over
  // from the previous test would be re-selected (keeping its old content) instead of opened.
  useLayoutStore.setState({ layout: freshLayout() });
  useSessionsStore.setState({ sessions: [] });
});

afterEach(async () => {
  await act(async () => root.unmount());
  document.body.innerHTML = "";
});

describe("画像ギャラリーのペイン", () => {
  it("画像だけをカードにし、新しい順に並べ、サムネイルは 512 で遅延読み込み", async () => {
    served = [img("a.png", 100), img("notes.md", 300), img("c.png", 300), { name: "sub", type: "dir" }];
    await render();
    expect(names()).toEqual(["c.png", "a.png"]);
    expect(thumbs()).toHaveLength(2);
    expect(thumbs()[0].getAttribute("loading")).toBe("lazy");
    expect(thumbs()[0].getAttribute("decoding")).toBe("async");
    expect(thumbs()[0].src).toContain("thumb=512");
    expect(thumbs()[0].src).toContain(encodeURIComponent("gen/c.png"));
    // The listing's mtime rides in the URL, so the Agent may answer `immutable` and coming
    // back to the tab costs no request at all (a cold thumbnail is ~95 ms, a cached one 44 µs).
    expect(thumbs()[0].src).toContain("v=300");
    // And the listing itself asks the Agent to decode the folder while it answers.
    expect(String(fetchMock.mock.calls[0][0])).toContain("warm=512");
  });

  it("mtime を返さない Agent では版を URL に載せない（載せると古い絵を永久に掴む）", async () => {
    served = [img("a.png")];
    await render();
    expect(thumbs()[0].src).toContain("thumb=512");
    expect(thumbs()[0].src).not.toContain("v=");
  });

  it("画像が 1 枚も無ければ空状態（読み込み中の空白ではなく）", async () => {
    served = [{ name: "notes.md", type: "file" }];
    await render();
    expect(cards()).toHaveLength(0);
    expect(host.querySelector(".ui-empty-title")?.textContent).toBe("このフォルダに画像はありません。");
  });

  it("本体は拡大、角のボタンはファイルペイン（転写のカードと同じ分け方）", async () => {
    served = [img("a.png", 100)];
    await render();
    expect(lightbox()).toBeNull();

    await click(host.querySelector(".gal-zoom"));
    expect(lightbox()).not.toBeNull();
    await click(document.querySelector(".mirror-lightbox-bar button:last-child"));
    expect(lightbox()).toBeNull();

    await click(host.querySelector(".gal-pane"));
    const views = allViews(useLayoutStore.getState().layout);
    const opened = views.find((v) => v.content.kind === "file");
    expect(opened && opened.content.kind === "file" && opened.content.filePath).toBe("gen/a.png");
    // Beside it, not instead of it: replacing this pane would take the grid away.
    expect(views.filter((v) => v.content.kind === "gallery")).toHaveLength(1);
  });

  it("サムネイルが出ない画像はカードごとペインを開く（拡大する絵が無いため）", async () => {
    served = [img("a.png", 100)];
    await render();
    await act(async () => {
      thumbs()[0].dispatchEvent(new Event("error", { bubbles: false }));
    });
    expect(thumbs()).toHaveLength(0);
    expect(host.querySelector(".gal-pane")).toBeNull();
    await click(host.querySelector(".gal-zoom"));
    expect(lightbox()).toBeNull();
    const opened = allViews(useLayoutStore.getState().layout).find((v) => v.content.kind === "file");
    expect(opened && opened.content.kind === "file" && opened.content.filePath).toBe("gen/a.png");
  });

  it("ライトボックスは同じ並びを ←/→ で送り、現在位置を出す", async () => {
    served = [img("a.png", 100), img("b.png", 200), img("c.png", 300)];
    await render();
    await click(host.querySelectorAll(".gal-zoom")[0]); // newest first: c.png

    const pos = () => document.querySelector(".mirror-lightbox-pos")?.textContent;
    expect(pos()).toBe("1 / 3");
    expect((document.querySelector(".mirror-lightbox-prev") as HTMLButtonElement).disabled).toBe(true);

    await click(document.querySelector(".mirror-lightbox-next"));
    expect(pos()).toBe("2 / 3");
    await act(async () => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowRight" }));
    });
    expect(pos()).toBe("3 / 3");
    expect((document.querySelector(".mirror-lightbox-next") as HTMLButtonElement).disabled).toBe(true);
  });

  it("拡大したまま新しい画像が届いても、見ている 1 枚は入れ替わらない", async () => {
    vi.useFakeTimers();
    try {
      served = [img("a.png", 100), img("b.png", 200)];
      await render();
      await click(host.querySelectorAll(".gal-zoom")[0]); // b.png, the newest
      const pos = () => document.querySelector(".mirror-lightbox-pos")?.textContent;
      const shownSrc = () => (document.querySelector(".mirror-lightbox img") as HTMLImageElement).src;
      expect(pos()).toBe("1 / 2");
      expect(shownSrc()).toContain(encodeURIComponent("gen/b.png"));

      await act(async () => {
        useSessionsStore.setState({
          sessions: [{ name: "slot01", alive: true, state: "working" }] as unknown as never[],
        });
      });
      served = [img("a.png", 100), img("b.png", 200), img("c.png", 300)];
      await act(async () => {
        vi.advanceTimersByTime(WORKING_TICK_MS);
      });
      await act(async () => {});

      // b.png slid to second place under "newest first" — an index would now be showing c.png.
      expect(pos()).toBe("2 / 3");
      expect(shownSrc()).toContain(encodeURIComponent("gen/b.png"));
    } finally {
      vi.useRealTimers();
    }
  });

  it("mtime を返さない Agent でも壊れず、相対時刻を出さずに名前順で並ぶ", async () => {
    served = [img("b.png"), img("a.png")];
    await render();
    expect(names()).toEqual(["a.png", "b.png"]);
    // The meta line falls back to the size; no "…前" / "… ago" is invented from the name.
    const meta = cards().map((c) => c.querySelector(".gal-meta")?.textContent || "");
    expect(meta.every((m) => /KB|B/.test(m))).toBe(true);
  });

  it("並び替えはペインの内容に書く（タブを切り替えても戻らない）", async () => {
    served = [img("a.png", 100), img("b.png", 200)];
    const paneId = await render();
    await click(byText("名前順"));
    const view = allViews(useLayoutStore.getState().layout).find((v) => v.id === paneId);
    expect(view?.content).toMatchObject({ kind: "gallery", sort: "name" });
  });

  it("300 枚で打ち切り、「さらに表示」で続きを出す", async () => {
    served = Array.from({ length: 305 }, (_, n) => img(`i${String(n).padStart(3, "0")}.png`, 1000 + n));
    await render();
    expect(cards()).toHaveLength(300);
    expect(host.querySelector(".gal-count")?.textContent).toContain("305");

    await click(byText("さらに表示"));
    expect(cards()).toHaveLength(305);
    expect(byText("さらに表示")).toBeUndefined();
  });

  it("galleryFocus で開くと、その 1 枚を拡大してから要求を内容から消す", async () => {
    served = [img("a.png", 100), img("b.png", 200)];
    // The pane really carries the request, so "it was taken back out" is observable.
    const paneId = paneWithGallery("gen", "a.png");
    await render({ paneId, focus: "a.png" });
    expect(document.querySelector(".mirror-lightbox-pos")?.textContent).toBe("2 / 2"); // a.png is the older one
    const view = allViews(useLayoutStore.getState().layout).find((v) => v.id === paneId);
    expect(view?.content).toMatchObject({ kind: "gallery" });
    expect(view?.content && "galleryFocus" in view.content).toBe(false);
  });

  it("走っているセッションがある間だけ遅い間隔で読み直し、増えた分をハイライトする", async () => {
    vi.useFakeTimers();
    try {
      served = [img("a.png", 100)];
      await render();
      expect(listings).toBe(1);

      // Nothing running: the timer does not exist at all (decision 6).
      await act(async () => {
        vi.advanceTimersByTime(WORKING_TICK_MS * 2);
      });
      expect(listings).toBe(1);

      await act(async () => {
        useSessionsStore.setState({
          sessions: [{ name: "slot01", alive: true, state: "working" }] as unknown as never[],
        });
      });
      served = [img("a.png", 100), img("b.png", 200)];
      await act(async () => {
        vi.advanceTimersByTime(WORKING_TICK_MS);
      });
      await act(async () => {});
      expect(listings).toBe(2);
      expect(names()).toEqual(["b.png", "a.png"]);
      expect(cards()[0].classList.contains("gal-new")).toBe(true); // the one that just landed
      expect(cards()[1].classList.contains("gal-new")).toBe(false);
    } finally {
      vi.useRealTimers();
    }
  });

  it("フォルダもカードで並び、押すとそのペインが中へ移動する（別ペインは開かない）", async () => {
    served = [img("a.png", 100), { name: "console", type: "dir" }, { name: "trial", type: "dir" }];
    const paneId = await render({ path: ".cache/agent-fleet/generated" });
    expect(folderNames()).toEqual(["上へ", "console", "trial"]);

    served = [img("x.png", 200)];
    await click(folderCards()[1].querySelector(".gal-enter"));
    const panes = allViews(useLayoutStore.getState().layout).filter((v) => v.content.kind === "gallery");
    expect(panes).toHaveLength(1); // 遷移であって、開き直しではない
    expect(panes[0].id).toBe(paneId);
    expect(panes[0].content).toEqual({ kind: "gallery", galleryPath: ".cache/agent-fleet/generated/console" });
  });

  it("「上へ」は親へ、ルートまで戻れる", async () => {
    served = [{ name: "console", type: "dir" }];
    const paneId = await render({ path: "a/b" });
    await click(folderCards()[0].querySelector(".gal-enter")); // 上へ
    const content = () => allViews(useLayoutStore.getState().layout).find((v) => v.id === paneId)!.content;
    expect(content()).toEqual({ kind: "gallery", galleryPath: "a" });
  });

  it("ヘッダの「上へ」は常に出ており（スクロールで隠れるグリッドの札とは別）、押すと親フォルダへ移動する", async () => {
    served = [img("a.png", 100)];
    const paneId = await render({ path: "a/b" });
    const upBtn = host.querySelector<HTMLButtonElement>(".gal-path button");
    expect(upBtn).not.toBeNull();
    expect(upBtn!.disabled).toBe(false);
    await click(upBtn);
    const content = allViews(useLayoutStore.getState().layout).find((v) => v.id === paneId)!.content;
    expect(content).toEqual({ kind: "gallery", galleryPath: "a" });
  });

  it("ルートではヘッダの「上へ」が無効になる（グリッドに札そのものが無いのと揃える）", async () => {
    served = [img("a.png", 100)];
    await render({ path: "" });
    expect(host.querySelector<HTMLButtonElement>(".gal-path button")!.disabled).toBe(true);
    expect(folderCards()).toHaveLength(0); // グリッド側にも「上へ」の札が無い
  });

  it("戻るボタンでひとつ前のフォルダへ戻る（「上へ」・パンくず・カードのどれで来ても同じ経路）", async () => {
    const unwire = wireLayoutHistory();
    try {
      served = [{ name: "b", type: "dir" }];
      const paneId = await render({ path: "a" });
      // A clean standing entry to leave the first navigate's push something correct to restamp
      // (same convention layout/history.dom.test.tsx's beforeEach uses).
      history.replaceState({ __af: true, layout: useLayoutStore.getState().layout }, "");
      const pathOf = () =>
        (allViews(useLayoutStore.getState().layout).find((v) => v.id === paneId)!.content as { galleryPath: string })
          .galleryPath;

      served = [img("x.png", 1)];
      await click(folderCards().find((c) => c.textContent?.includes("b"))?.querySelector(".gal-enter"));
      expect(pathOf()).toBe("a/b");

      await back();
      expect(pathOf()).toBe("a");
    } finally {
      unwire();
    }
  });

  it("セッションのフォルダは UUID でなくセッション名と枚数で出る（追加の問い合わせ無しで）", async () => {
    useSessionsStore.setState({
      sessions: [
        { name: "slot01", kind: "claude", title: "絵を描く", generatedImages: 3, generatedImagesPath: "gen/uuid-1" } as never,
      ],
    });
    served = [
      { name: "uuid-1", type: "dir" },
      { name: "uuid-2", type: "dir" },
    ];
    await render({ path: "gen" });
    expect(folderNames()).toEqual(["上へ", "絵を描く", "uuid-2"]);
    expect(folderCards()[1].querySelector(".gal-meta")?.textContent).toBe("3 枚");
    expect(folderCards()[2].querySelector(".gal-meta")?.textContent).toBe("");
    expect(listings).toBe(1); // カードごとに一覧を引いてはいない
  });

  it("画像が無くてもフォルダがあれば空状態にしない（生成物の親フォルダがまさにそれ）", async () => {
    served = [{ name: "console", type: "dir" }];
    await render({ path: "gen" });
    expect(host.querySelector(".ui-empty-title")).toBeNull();
    expect(folderCards().length).toBe(2); // 上へ ＋ console
  });

  it("セッションの題は、そのフォルダから出た時点で外れる（タブが嘘をつかない）", async () => {
    served = [{ name: "sub", type: "dir" }];
    const paneId = await render({ path: "gen/uuid-1", sessionName: "slot01" });
    await click(folderCards()[1].querySelector(".gal-enter"));
    const content = allViews(useLayoutStore.getState().layout).find((v) => v.id === paneId)!.content;
    expect(content).toEqual({ kind: "gallery", galleryPath: "gen/uuid-1/sub" });
  });

  it("パンくずのどの段からでも飛べる", async () => {
    served = [img("a.png", 1)];
    const paneId = await render({ path: "a/b/c" });
    const crumbs = [...host.querySelectorAll<HTMLButtonElement>(".gal-crumb")];
    expect(crumbs.map((c) => c.textContent)).toEqual(["ホーム", "a", "b", "c"]);
    expect(crumbs[3].disabled).toBe(true); // 今いる段
    await click(crumbs[1]);
    const content = allViews(useLayoutStore.getState().layout).find((v) => v.id === paneId)!.content;
    expect(content).toEqual({ kind: "gallery", galleryPath: "a" });
  });

  it("拡大は先にサムネイルを出し、隣の 1 枚を先読みする", async () => {
    // jsdom は画像を読まないので、裏で作られる Image を捕まえて中身を見る。
    const probes: { src: string }[] = [];
    class FakeImage {
      src = "";
      decoding = "";
      complete = false;
      constructor() {
        probes.push(this);
      }
      addEventListener() {}
      removeEventListener() {}
    }
    // NOT vi.unstubAllGlobals() at the end: the fetch stub every test here depends on is a
    // global stub too, and unstubbing all of them leaves the next test with no fetch at all.
    const realImage = globalThis.Image;
    vi.stubGlobal("Image", FakeImage);
    vi.useFakeTimers();
    try {
      served = [img("a.png", 100), img("b.png", 200), img("c.png", 300)];
      await render();
      await click(host.querySelector(".gal-card:not(.folder) .gal-zoom"));

      // 出ているのは縮小版（原寸は約 1MB あり、届くまで真っ白になるのを避ける）。
      const shown = document.querySelector<HTMLImageElement>(".imgview-img")!;
      expect(shown.getAttribute("src")).toContain("thumb=512");
      expect(shown.getAttribute("src")).toContain("v=300"); // 版付き＝2 度目は無通信

      await act(async () => {
        vi.advanceTimersByTime(500);
      });
      // 隣（次の 1 枚）の原寸を先読みしている＝←/→ が待たされない。
      const prefetched = probes.map((p) => p.src).filter((u) => u.includes("a.png") || u.includes("b.png"));
      expect(prefetched.some((u) => u.includes("b.png") && !u.includes("thumb="))).toBe(true);
    } finally {
      vi.useRealTimers();
      vi.stubGlobal("Image", realImage);
    }
  });

  it("読み込みが終わるまでは読み込み中を出す（「上へ」だけの半端な一覧ではない）", async () => {
    let resolveFetch!: (v: Response) => void;
    fetchMock.mockImplementationOnce(
      () =>
        new Promise<Response>((res) => {
          resolveFetch = res;
        }),
    );
    served = [img("a.png", 100), { name: "sub", type: "dir" }];
    const paneId = paneWithGallery("gen");
    host = document.createElement("div");
    document.body.appendChild(host);
    root = createRoot(host);
    await act(async () => {
      root.render(<GalleryView paneId={paneId} path="gen" />);
    });
    // The grid branch would draw "Up" alone here (folders/images are both empty on a null
    // listing) — a partial page that reads as stuck rather than loading.
    expect(folderCards()).toHaveLength(0);
    expect(cards()).toHaveLength(0);
    expect(host.querySelector(".ui-empty-title")?.textContent).toBe("読み込み中…");

    await act(async () => {
      resolveFetch({
        ok: true,
        status: 200,
        statusText: "OK",
        headers: { get: () => null },
        text: async () => JSON.stringify({ entries: served }),
      } as unknown as Response);
    });
    expect(host.querySelector(".ui-empty-title")).toBeNull();
    expect(names()).toEqual(["a.png"]);
  });

  it("サムネイルはギャラリー自身の枠に近づくまで要求しない（一括取得しない）", async () => {
    // The default jsdom shell (domSetup.ts) reports every observed element as intersecting
    // right away — fine for every other test here, but this one is ABOUT the gating, so it
    // takes manual control of the callback instead (the pattern BrowserSurface's own test uses).
    const realIO = globalThis.IntersectionObserver;
    const observers: { cb: IntersectionObserverCallback; el: Element }[] = [];
    class CapturingIO {
      #cb: IntersectionObserverCallback;
      constructor(cb: IntersectionObserverCallback) {
        this.#cb = cb;
      }
      observe(el: Element) {
        observers.push({ cb: this.#cb, el });
      }
      unobserve() {}
      disconnect() {}
      takeRecords() {
        return [];
      }
    }
    vi.stubGlobal("IntersectionObserver", CapturingIO);
    try {
      served = [img("a.png", 100)];
      await render();
      // Not armed yet: the thumb span exists (it is the observed element) but no <img>.
      expect(thumbs()).toHaveLength(0);
      expect(host.querySelector(".gal-thumb")).not.toBeNull();
      expect(observers).toHaveLength(1);

      await act(async () => {
        observers[0].cb(
          [{ isIntersecting: true, target: observers[0].el } as IntersectionObserverEntry],
          {} as IntersectionObserver,
        );
      });
      expect(thumbs()).toHaveLength(1);
      expect(thumbs()[0].src).toContain("thumb=512");
      expect(thumbs()[0].getAttribute("fetchpriority")).toBe("high");
    } finally {
      vi.stubGlobal("IntersectionObserver", realIO);
    }
  });

  it("フォルダを移った瞬間に前のフォルダのカードを消す（残すと存在しないパスのサムネイルを取りに行く）", async () => {
    served = [img("a.png", 100)];
    const paneId = await render({ path: "gen" });
    expect(names()).toEqual(["a.png"]);

    // The new folder's listing never answers, so anything still drawn can only be left over
    // from the old one — with the new folder's path glued onto its file names.
    fetchMock.mockImplementationOnce((() => new Promise(() => {})) as never);
    await act(async () => {
      root.render(<GalleryView paneId={paneId} path="gen/sub" />);
    });
    expect(cards()).toHaveLength(0);
    expect(thumbs()).toHaveLength(0);
    expect(host.querySelector(".ui-empty-title")?.textContent).toBe("読み込み中…");
  });

  it("一度見たフォルダは、一覧の往復を待たずに前のカードで開く（戻るでいちばん効く）", async () => {
    served = [img("a.png", 100), img("b.png", 200)];
    await render();
    expect(names()).toEqual(["b.png", "a.png"]);
    await act(async () => root.unmount());

    // This time the listing never answers, so anything on screen can only have come from the
    // cache — which is exactly the window the reader used to spend looking at "読み込み中…".
    fetchMock.mockImplementationOnce((() => new Promise(() => {})) as never);
    await render();
    expect(host.querySelector(".ui-empty")).toBeNull();
    expect(names()).toEqual(["b.png", "a.png"]);
    // Nothing is tinted: walking back into a folder is not "two pictures just arrived".
    expect(cards().some((c) => c.classList.contains("gal-new"))).toBe(false);
    // And the header says where the grid came from rather than pretending it is confirmed.
    expect(host.querySelector(".gal-count")?.textContent).toContain("更新中");
  });

  it("戻ったあとに一覧が届いたら、本当に増えた 1 枚だけに色が付く", async () => {
    served = [img("a.png", 100)];
    await render();
    await act(async () => root.unmount());

    served = [img("a.png", 100), img("b.png", 200)];
    await render();
    expect(names()).toEqual(["b.png", "a.png"]);
    expect(cards()[0].classList.contains("gal-new")).toBe(true);
    expect(cards()[1].classList.contains("gal-new")).toBe(false);
    expect(host.querySelector(".gal-count")?.textContent).not.toContain("更新中");
  });

  it("フォルダでの居場所（スクロール位置と「さらに表示」）も覚えていて、戻ると同じ場所に出る", async () => {
    served = Array.from({ length: 305 }, (_, n) => img(`i${String(n).padStart(3, "0")}.png`, 1000 + n));
    await render();
    await click(byText("さらに表示"));
    expect(cards()).toHaveLength(305);

    const body = () => host.querySelector<HTMLDivElement>(".gal-body")!;
    body().scrollTop = 420;
    await act(async () => {
      body().dispatchEvent(new Event("scroll", { bubbles: true }));
    });
    expect(readGallery("gen")).toMatchObject({ scrollTop: 420, limit: 600 });

    await act(async () => root.unmount());
    await render();
    expect(cards()).toHaveLength(305);
    // jsdom has no layout, so this pins the WIRING (the position is put back before paint),
    // not that 420px lands on the same row — that needs a real browser.
    expect(body().scrollTop).toBe(420);
  });

  it("消えたフォルダでは、覚えている一覧ではなくエラーを出す（キャッシュが吐ける唯一の嘘）", async () => {
    served = [img("a.png", 100)];
    await render();
    await act(async () => root.unmount());

    fetchMock.mockImplementationOnce((async () => ({
      ok: false,
      status: 404,
      statusText: "Not Found",
      headers: { get: () => null },
      text: async () => JSON.stringify({ error: { code: "not_dir" } }),
    })) as never);
    await render();
    expect(host.querySelector(".ui-empty-title")?.textContent).toBe("フォルダを読み込めませんでした");
    expect(cards()).toHaveLength(0);
    expect(readGallery("gen")).toBeUndefined();
  });

  it("フォルダを指しただけで、その一覧を先に取りに行く（押したときには手元にある）", async () => {
    served = [{ name: "sub", type: "dir" }];
    await render({ path: "gen" });
    expect(listings).toBe(1);

    const enter = folderCards().find((c) => c.textContent?.includes("sub"))!.querySelector(".gal-enter")!;
    await act(async () => {
      enter.dispatchEvent(new Event("pointerdown", { bubbles: true }));
    });
    expect(listings).toBe(2);
    expect(String(fetchMock.mock.calls[1][0])).toContain(encodeURIComponent("gen/sub"));
    expect(readGallery("gen/sub")).toBeDefined();
  });

  it("マウント時に 1 回だけ読み、常駐ポーラーにはしない", async () => {
    served = [img("a.png", 100)];
    await render();
    expect(listings).toBe(1);
    await act(async () => {
      await new Promise((r) => setTimeout(r, 50));
    });
    expect(listings).toBe(1);
  });
});
