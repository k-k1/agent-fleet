// usePaneLayoutSync decides between converting the pop-out's one pane in place and switching
// profiles. Converting on a preference change overwrote the other profile; switching on Expand
// or Back replaced the pane the user popped out.
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { getTenant } from "../core/api/client.ts";
import type { PopoutMode } from "../lib/popoutMode.ts";
import { activePane } from "../layout/ops.ts";
import { useLayoutStore } from "../layout/store.ts";
import { usePaneLayoutSync } from "./usePaneLayoutSync.ts";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let root: Root | null = null;
let host: HTMLElement | null = null;

function Probe({ paneLayout, popout }: { paneLayout: "split" | "tabs"; popout: PopoutMode }) {
  usePaneLayoutSync(true, getTenant(), paneLayout, popout);
  return null;
}

/** Renders (or re-renders) the hook and lets the dirty-navigation promise settle. */
async function sync(paneLayout: "split" | "tabs", popout: PopoutMode): Promise<void> {
  if (!root) {
    host = document.createElement("div");
    document.body.appendChild(host);
    root = createRoot(host);
  }
  await act(async () => {
    root!.render(<Probe paneLayout={paneLayout} popout={popout} />);
  });
  await act(async () => {});
}

const session = (): string | null | undefined => activePane(useLayoutStore.getState().layout)?.session;

beforeEach(() => {
  sessionStorage.clear();
  localStorage.clear();
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

describe("usePaneLayoutSync in a pop-out tab", () => {
  it("keeps the popped pane when Expand moves it to the tabbed preference", async () => {
    useLayoutStore.getState().initSinglePane({ kind: "terminal", chat: true }, "sess-a", null, "split");
    await sync("tabs", "popout");
    expect(useLayoutStore.getState().layout.mode).toBe("split");

    await sync("tabs", "full");
    expect(useLayoutStore.getState().layout.mode).toBe("tabs");
    expect(session()).toBe("sess-a");
  });

  it("brings the session back when Back restores a pre-expand entry", async () => {
    useLayoutStore.getState().initSinglePane({ kind: "terminal", chat: true }, "sess-a", null, "split");
    const beforeLink = useLayoutStore.getState().layout;
    await sync("tabs", "popout");
    useLayoutStore.getState().openTargetInNew({ content: { kind: "file", filePath: "/repo/a.ts" } });
    await sync("tabs", "full");

    await act(async () => useLayoutStore.getState().setFromHistory(beforeLink));
    await act(async () => {});
    expect(useLayoutStore.getState().layout.mode).toBe("tabs");
    expect(session()).toBe("sess-a");
  });

  it("switches profiles, not panes, when the preference changes", async () => {
    // Split profile holds B; the full pop-out shows A in tabs.
    useLayoutStore.getState().initSinglePane({ kind: "terminal", chat: true }, "sess-b", null, "split");
    useLayoutStore.getState().initSinglePane({ kind: "terminal", chat: true }, "sess-a", null, "tabs");
    await sync("tabs", "full");

    await sync("split", "full");
    expect(useLayoutStore.getState().layout.mode).toBe("split");
    expect(session()).toBe("sess-b");

    await sync("tabs", "full");
    expect(useLayoutStore.getState().layout.mode).toBe("tabs");
    expect(session()).toBe("sess-a");
  });
});
