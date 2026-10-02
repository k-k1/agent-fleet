// The pop-out tab seeds its single pane, and App's mode-sync effect then loads the layout
// profile whenever the layout's mode differs. A seed built in the other mode was replaced by
// that profile at once, so the popped session or file vanished from the new tab — for every
// user once Tabbed grid became the default. A minimal pop-out runs split regardless, because
// its one-pane rules only hold there.
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { getTenant } from "../core/api/client.ts";
import { layoutModeFor, setPopoutMode } from "../lib/popoutMode.ts";
import { activePane, allCells, allViews, relabelSingleCell } from "./ops.ts";
import { useLayoutStore } from "./store.ts";

describe("pop-out seed", () => {
  beforeEach(() => {
    sessionStorage.clear();
    localStorage.clear();
  });
  afterEach(() => setPopoutMode(null));

  for (const mode of ["split", "tabs"] as const) {
    it(`keeps the popped pane in the ${mode} profile`, () => {
      useLayoutStore.getState().initSinglePane({ kind: "terminal", chat: true }, "sess-a", null, mode);
      expect(useLayoutStore.getState().layout.mode).toBe(mode);

      // What the mode-sync effect and a reload of the pop-out tab both read back.
      useLayoutStore.getState().loadMode(getTenant(), mode);
      const p = activePane(useLayoutStore.getState().layout)!;
      expect(p.session).toBe("sess-a");
      expect(p.content).toEqual({ kind: "terminal", chat: true });
    });
  }

  it("runs a minimal pop-out split even when the preference is tabs", () => {
    expect(layoutModeFor("popout", "tabs")).toBe("split");
    expect(layoutModeFor("full", "tabs")).toBe("tabs");
    expect(layoutModeFor(null, "tabs")).toBe("tabs");
    expect(layoutModeFor(null, "split")).toBe("split");
  });

  it("keeps a minimal pop-out at one pane when a link opens", () => {
    setPopoutMode("popout");
    useLayoutStore.getState().initSinglePane({ kind: "terminal", chat: true }, "sess-a", null, layoutModeFor("popout", "tabs"));
    useLayoutStore.getState().openTargetInNew({ content: { kind: "file", filePath: "/repo/a.ts" } });
    const l = useLayoutStore.getState().layout;
    expect(allCells(l)).toHaveLength(1);
    expect(allViews(l)).toHaveLength(1);
    expect(activePane(l)!.content).toEqual({ kind: "file", filePath: "/repo/a.ts" });
  });

  it("re-labels only a one-cell layout", () => {
    useLayoutStore.getState().initSinglePane({ kind: "terminal", chat: true }, "sess-a", null, "split");
    const one = useLayoutStore.getState().layout;
    expect(relabelSingleCell(one, "tabs")).toEqual({ ...one, mode: "tabs" });
    useLayoutStore.getState().splitRight();
    expect(relabelSingleCell(useLayoutStore.getState().layout, "tabs")).toBeNull();
  });

  // Minimal pop-out → open a link (replaces in place, pushes history) → Expand → Back: the entry
  // Back restores was recorded in split, and re-labelling it must bring the session back.
  it("brings the session back when Back restores a pre-expand entry", () => {
    setPopoutMode("popout");
    useLayoutStore.getState().initSinglePane({ kind: "terminal", chat: true }, "sess-a", null, "split");
    const beforeLink = useLayoutStore.getState().layout;
    useLayoutStore.getState().openTargetInNew({ content: { kind: "file", filePath: "/repo/a.ts" } });
    setPopoutMode("full");
    useLayoutStore.getState().commit(relabelSingleCell(useLayoutStore.getState().layout, "tabs")!, false);

    useLayoutStore.getState().setFromHistory(beforeLink);
    const back = relabelSingleCell(useLayoutStore.getState().layout, "tabs")!;
    useLayoutStore.getState().commit(back, false);
    const l = useLayoutStore.getState().layout;
    expect(l.mode).toBe("tabs");
    expect(activePane(l)!.session).toBe("sess-a");
  });
});
