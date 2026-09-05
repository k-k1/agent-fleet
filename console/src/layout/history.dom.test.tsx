// History <-> layout sync. A history entry is a snapshot of the layout, so a commit that pushes
// no entry (tab selection, pane activation, wrap, divider drag) must replace the standing entry;
// otherwise the next popstate restores a stale snapshot. Modals close by firing history.back()
// (the back-to-close guard in lib/backClose), so the bug always surfaces as "opening and closing
// a modal switches the tab on its own".
import { describe, expect, it, beforeEach } from "vitest";
import { useLayoutStore, wireLayoutHistory } from "./store.ts";
import type { Layout } from "./types.ts";

const tabbedLayout = (selected: string): Layout => ({
  version: 3,
  mode: "tabs",
  cols: [{
    id: "c0",
    rowRatio: 0.5,
    cells: [{
      id: "g0",
      selectedViewId: selected,
      views: [
        { id: "p0", session: "alpha", content: { kind: "terminal", chat: false }, wrap: null },
        { id: "p1", session: "beta", content: { kind: "terminal", chat: false }, wrap: null },
      ],
    }],
  }],
  colRatios: [1],
  activeCellId: "g0",
});

const selectedTab = (): string | null =>
  useLayoutStore.getState().layout.cols[0].cells[0].selectedViewId;

/** popstate arrives asynchronously even in jsdom (history.back() is queued as a task). */
const back = (): Promise<void> =>
  new Promise((resolve) => {
    window.addEventListener("popstate", () => setTimeout(resolve, 0), { once: true });
    history.back();
  });

/** The modal's back-to-close guard (the same entry lib/backClose pushes). */
const openModal = (): void => {
  history.pushState({ __af: true, afModal: true }, "");
};

describe("layout history", () => {
  let unwire: (() => void) | null = null;

  beforeEach(() => {
    unwire?.();
    const l = tabbedLayout("p0");
    useLayoutStore.setState({ layout: l, hydrated: true });
    history.replaceState({ __af: true, layout: l }, "");
    unwire = wireLayoutHistory();
    return () => {
      unwire?.();
      unwire = null;
    };
  });

  it("keeps the selected tab when a modal is opened and closed", async () => {
    useLayoutStore.getState().selectTab("p1");
    expect(selectedTab()).toBe("p1");

    openModal();
    await back(); // closing via ✕ / Esc / backdrop click consumes the guard entry

    expect(selectedTab()).toBe("p1");
  });

  it("keeps the selected tab when the modal opens right after the tab switch", async () => {
    useLayoutStore.getState().selectTab("p1");
    openModal();
    await back();

    expect(selectedTab()).toBe("p1");
  });

  it("does not reset the layout when popping onto a modal guard entry", async () => {
    // Close only the top of two stacked modals: the entry we land on is a guard entry with no
    // layout, so reading "no layout" as "initial layout" would wipe everything.
    openModal();
    openModal();
    await back();

    expect(useLayoutStore.getState().layout.cols[0].cells[0].views).toHaveLength(2);
    expect(selectedTab()).toBe("p0");
  });

  it("restores the tab that was on screen when the browser back button is used", async () => {
    // Not just modals: the entry any entry-pushing operation leaves behind is stale the same way.
    useLayoutStore.getState().selectTab("p1");
    useLayoutStore.getState().splitRight();
    await back(); // undo the split -> back to what was on screen (p1 selected)

    expect(useLayoutStore.getState().layout.cols).toHaveLength(1);
    expect(selectedTab()).toBe("p1");
  });
});
