// The low-disk toast body: the estimate and the button appear only with a recent walk, are
// withdrawn after a cleanup, and the button only OPENS the confirm.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

vi.mock("../../core/api/client.ts", () => ({ api: vi.fn(), apiJSON: async () => ({}), getTenant: () => "t" }));

const { HomeLowNotice } = await import("./HomeLowNotice.tsx");
const { useSessionUI } = await import("./ui.ts");
const { setLocale } = await import("../../lib/i18n/index.ts");
const store = await import("../../core/store/homeReclaim.ts");

let host: HTMLDivElement;
let root: Root;
const g = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };
const GIB = 1024 ** 3;
const walk = (ageMs: number) => ({
  measured_at: new Date(Date.now() - ageMs).toISOString(),
  categories: [],
  repos: [{ bytes: 5 * GIB, regenerable: 2 * GIB }],
});

beforeEach(async () => {
  g.IS_REACT_ACT_ENVIRONMENT = true;
  setLocale("en");
  store.__test.reset();
  useSessionUI.setState({ reclaimOpen: false });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => root.render(<HomeLowNotice free="1.0" total="100.0" />));
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

describe("HomeLowNotice", () => {
  it("is the plain notice without a walk", () => {
    expect(host.textContent).toContain("1.0 GiB free of 100.0 GiB");
    expect(host.textContent).not.toContain("reclaimed");
    expect(host.querySelector("button")).toBeNull();
  });
  it("shows the figure and the button with a recent walk; the button opens the confirm and deletes nothing", async () => {
    await act(async () => store.publishWalk(walk(1000)));
    expect(host.textContent).toContain("2.0 GB");
    const btn = host.querySelector("button")!;
    expect(btn.textContent).toBe("Clean regenerable items…");
    await act(async () => btn.click());
    expect(useSessionUI.getState().reclaimOpen).toBe(true);
  });
  it("shows nothing for a stale walk", async () => {
    await act(async () => store.publishWalk(walk(store.RECENT_WALK_MS + 1000)));
    expect(host.querySelector("button")).toBeNull();
  });
  it("drops the figure when a cleanup has run", async () => {
    await act(async () => store.publishWalk(walk(1000)));
    await act(async () => store.markCleaned());
    expect(host.querySelector("button")).toBeNull();
  });
});
