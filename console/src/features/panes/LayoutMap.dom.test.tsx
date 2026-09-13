// The rail's layout map. It hides itself with a single pane, and that early return is where
// a hook placed below it took the whole Console down: with one pane the component ran N
// hooks, with two it ran N+1, and React answered the changed count with error #310 and an
// empty root (2026-09-14, seen on the screenshot harness the moment a second pane opened).
// So the first check is the one that would have caught it: one pane, then two, no throw.
import { describe, it, expect, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { Layout } from "../../layout/types.ts";

let imagegenAvailable = true;
// The mock must be a HOOK (it calls one), or the count check below tests nothing: a plain
// function returning a boolean adds no hook, and the misplaced call it guards against would
// pass unnoticed — which is exactly what the first positive control of this file showed.
vi.mock("../imagegen/available.ts", async () => {
  const { useState } = await import("react");
  return {
    useImagegenAvailable: () => {
      useState(0);
      return imagegenAvailable;
    },
  };
});
vi.mock("../overview/open.ts", () => ({ openSessionsOverview: () => {} }));
vi.mock("../imagegen/open.ts", () => ({ openImagegen: () => {} }));
vi.mock("../gallery/open.ts", () => ({ openGeneratedGallery: () => {} }));

const { useLayoutStore } = await import("../../layout/store.ts");
const { LayoutMap } = await import("./LayoutMap.tsx");

const cell = (id: string) => ({
  id,
  selectedViewId: `${id}-v`,
  views: [{ id: `${id}-v`, session: null, content: { kind: "sessions" as const, showStopped: false }, wrap: null }],
});
const layout = (n: 1 | 2): Layout => ({
  version: 3,
  mode: "split",
  cols: n === 1 ? [{ id: "c0", rowRatio: 1, cells: [cell("a")] }] : [
    { id: "c0", rowRatio: 1, cells: [cell("a")] },
    { id: "c1", rowRatio: 1, cells: [cell("b")] },
  ],
  colRatios: n === 1 ? [1] : [0.5, 0.5],
  activeCellId: "a",
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
const mount = async () => {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<LayoutMap />);
  });
};
const setLayout = async (l: Layout) => {
  await act(async () => {
    useLayoutStore.setState({ layout: l, hydrated: true });
  });
};
const buttons = () => [...(host?.querySelectorAll(".lm-cap .ui-iconbtn") || [])].map((b) => b.getAttribute("aria-label"));

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  imagegenAvailable = true;
});

describe("LayoutMap", () => {
  it("survives going from one pane to two (the hook count must not change)", async () => {
    await setLayout(layout(1));
    await mount();
    expect(host?.querySelector(".layoutmap")).toBeNull();
    await setLayout(layout(2));
    expect(host?.querySelector(".layoutmap")).not.toBeNull();
    expect(host?.querySelectorAll(".lm-col").length).toBe(2);
  });

  it("keeps both caption buttons in one group at the edge, and drops the image one without an engine", async () => {
    await setLayout(layout(2));
    await mount();
    expect(host?.querySelectorAll(".lm-cap > .lm-cap-actions").length).toBe(1);
    expect(buttons().length).toBe(3);
    imagegenAvailable = false;
    await setLayout({ ...layout(2), activeCellId: "b" });
    expect(buttons().length).toBe(2);
  });
});
