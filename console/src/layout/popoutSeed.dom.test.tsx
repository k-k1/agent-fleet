// The pop-out tab seeds its single pane, and App's mode-sync effect then loads the paneLayout
// profile whenever the layout's mode differs. A seed built in the other mode was replaced by
// that profile at once, so the popped session or file vanished from the new tab — for every
// user once Tabbed grid became the default.
import { beforeEach, describe, expect, it } from "vitest";
import { getTenant } from "../core/api/client.ts";
import { activePane } from "./ops.ts";
import { useLayoutStore } from "./store.ts";

describe("pop-out seed", () => {
  beforeEach(() => {
    sessionStorage.clear();
    localStorage.clear();
  });

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
});
