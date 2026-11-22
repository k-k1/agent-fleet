// The working tick no longer restarts when the pane walks into another folder, so its reads race
// the new folder's first read (#4815). These hold the listing answers back and release them out
// of order: nothing read for the folder being left, and no answer older than one already landed,
// may reach the grid or the cache.
import {
  afterEach,
  beforeAll,
  beforeEach,
  describe,
  expect,
  it,
  vi,
} from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

type Pending = { url: string; resolve: (v: unknown) => void };
const pending: Pending[] = [];
vi.mock("../../core/api/client.ts", async (orig) => {
  const real = (await orig()) as Record<string, unknown>;
  return {
    ...real,
    api: (url: string) =>
      String(url).includes("fs/tree")
        ? new Promise((resolve) => pending.push({ url: String(url), resolve }))
        : Promise.resolve({}),
  };
});

const { GalleryView } = await import("./GalleryView.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { ConfirmProvider } = await import("../../ui/ConfirmProvider.tsx");
const { setLocale } = await import("../../lib/i18n/index.ts");
const { useLayoutStore } = await import("../../layout/store.ts");
const { useWorkspaceStore } = await import("../../core/store/workspace.ts");
const { allViews, freshLayout } = await import("../../layout/ops.ts");
const { useSessionsStore } = await import("../sessions/store.ts");
const { WORKING_TICK_MS } = await import("../files/refreshPolicy.ts");
const { clearGalleryCache, readGallery } = await import("./galleryCache.ts");

const img = (name: string, mtime: number) => ({
  name,
  type: "file",
  size: 1000,
  mtime,
});
const listing = (...names: string[]) => ({
  entries: names.map((n, i) => img(n, 100 + i)),
});

let host: HTMLDivElement;
let root: Root;
let paneId = "";

const view = (path: string) => (
  <ToastProvider>
    <ConfirmProvider>
      <GalleryView paneId={paneId} path={path} />
    </ConfirmProvider>
  </ToastProvider>
);
const show = (path: string) => act(async () => root.render(view(path)));
const answer = (i: number, v: unknown) =>
  act(async () => pending[i]!.resolve(v));
const tick = () =>
  act(async () => void vi.advanceTimersByTime(WORKING_TICK_MS));
const names = () =>
  [...host.querySelectorAll<HTMLElement>(".gal-card:not(.folder) .gal-name")]
    .map((n) => n.textContent)
    .sort();
const cached = (path: string) =>
  (readGallery(path)?.entries ?? []).map((e) => e.name).sort();

beforeAll(() => {
  (
    globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }
  ).IS_REACT_ACT_ENVIRONMENT = true;
  setLocale("en");
});

beforeEach(async () => {
  vi.useFakeTimers();
  pending.length = 0;
  clearGalleryCache();
  useWorkspaceStore.setState({ state: "running" });
  useLayoutStore.setState({ layout: freshLayout() });
  useLayoutStore
    .getState()
    .openTarget({ content: { kind: "gallery", galleryPath: "a" } });
  paneId = allViews(useLayoutStore.getState().layout).find(
    (v) => v.content.kind === "gallery",
  )!.id;
  // A running session keeps the working tick on.
  useSessionsStore.setState({
    sessions: [
      { name: "slot01", alive: true, state: "working" },
    ] as unknown as never[],
  });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await show("a");
  await answer(0, listing("a1.png"));
  expect(names()).toEqual(["a1.png"]);
});

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  useSessionsStore.setState({ sessions: [] });
  vi.useRealTimers();
});

describe("GalleryView reads across a change of folder", () => {
  it("drops a tick's read of the folder being left, on screen and in the cache", async () => {
    await tick();
    expect(pending).toHaveLength(2); // the tick, for "a"
    await show("b");
    expect(pending).toHaveLength(3); // the first read of "b"
    // The read for "a" answers first, while nothing for "b" has landed.
    await answer(1, listing("a1.png", "a2.png"));
    expect(names()).toEqual([]);
    expect(cached("a")).toEqual(["a1.png"]);
    await answer(2, listing("b1.png"));
    expect(names()).toEqual(["b1.png"]);
  });

  it("keeps a tick's newer listing when the new folder's first read answers after it", async () => {
    await show("b");
    await tick();
    expect(pending.slice(1).map((p) => p.url.includes("path=b"))).toEqual([
      true,
      true,
    ]);
    await answer(2, listing("b1.png", "b2.png")); // the tick, started after the first read
    await answer(1, listing("b1.png"));
    expect(names()).toEqual(["b1.png", "b2.png"]);
    expect(cached("b")).toEqual(["b1.png", "b2.png"]);
  });

  it("does not let an older answer of the same folder overwrite a newer one", async () => {
    await tick();
    await tick();
    expect(pending).toHaveLength(3);
    // Both in one turn, newer first: the older one's cache write then comes before the newer
    // one's state update, which is why fetchGalleryListing claims the read as it writes.
    await act(async () => {
      pending[2]!.resolve(listing("a1.png", "a2.png", "a3.png"));
      pending[1]!.resolve(listing("a1.png", "a2.png"));
    });
    expect(names()).toEqual(["a1.png", "a2.png", "a3.png"]);
    expect(cached("a")).toEqual(["a1.png", "a2.png", "a3.png"]);
  });

  it("does not restart the tick's period on a change of folder", async () => {
    await act(async () => void vi.advanceTimersByTime(WORKING_TICK_MS - 1000));
    await show("b");
    expect(pending).toHaveLength(2);
    await act(async () => void vi.advanceTimersByTime(1000));
    expect(pending).toHaveLength(3);
  });
});
