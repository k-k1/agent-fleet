// A browser pane (or a Chromium attachment pane) on a workspace whose runtime offers no
// browser features: Pane swaps in the BrowserUnavailable notice instead of mounting the
// browser view, so no controller asks the Agent for a Chromium that cannot start and a
// pane restored from a saved layout explains itself. Elsewhere the browser view mounts.
import { afterEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { Pane } from "./Pane.tsx";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { t } from "../../lib/i18n/index.ts";
import type { Cell, PaneView } from "../../layout/types.ts";

vi.mock("../browser/BrowserPane.tsx", () => ({ BrowserPane: () => <div className="browser-stub" /> }));
vi.mock("../sessions/useSessionActions.tsx", () => ({ useSessionActions: () => ({}) }));
vi.mock("../browser/BrowserAttachPane.tsx", () => ({ BrowserAttachPane: () => <div className="attach-stub" /> }));

const browserView: PaneView = { id: "v-b", session: null, content: { kind: "browser", port: 3000, path: "/app" }, wrap: null };
const attachView: PaneView = { id: "v-a", session: null, content: { kind: "browserAttach", attachmentId: "a1" }, wrap: null };
const noop = () => {};

describe("browser panes and browser availability", () => {
  let root: Root | null = null;
  let host: HTMLElement | null = null;

  const show = async (view: PaneView, browserUnavailable: string) => {
    useWorkspaceStore.setState({ state: "running", browserUnavailable });
    const cell: Cell = { id: "c1", selectedViewId: view.id, views: [view] };
    host = document.createElement("div");
    document.body.appendChild(host);
    await act(async () => {
      root = createRoot(host!);
      root.render(<Pane cell={cell} pane={view} onActivate={noop} onClose={noop} onSwap={noop} onDropSplit={noop} />);
    });
  };

  afterEach(async () => {
    if (root) await act(async () => root!.unmount());
    host?.remove();
    root = null;
    host = null;
    useWorkspaceStore.setState({ browserUnavailable: "" });
  });

  it("replaces a browser pane with the reason and a lightweight-preview link", async () => {
    await show(browserView, "kubernetes");
    expect(host!.querySelector(".browser-stub")).toBeNull();
    const notice = host!.querySelector(".browser-unavailable")!;
    expect(notice.textContent).toContain(t("browser.unavailable.title"));
    expect(notice.textContent).toContain(t("browser.unavailable.body", { runtime: "kubernetes" }));
    const open = vi.spyOn(window, "open").mockReturnValue(null);
    const btn = [...notice.querySelectorAll("button")].find((b) => b.textContent === t("browser.unavailable.open_light", { port: 3000 }))!;
    await act(async () => btn.click());
    expect(open).toHaveBeenCalledWith(expect.stringMatching(/3000\/app$/), "_blank", "noopener");
    open.mockRestore();
  });

  it("replaces an attachment pane with the reason", async () => {
    await show(attachView, "kubernetes");
    expect(host!.querySelector(".attach-stub")).toBeNull();
    expect(host!.querySelector(".browser-unavailable")?.textContent).toContain(t("browser.unavailable.title"));
    expect(host!.querySelector(".browser-unavailable button")).toBeNull();
  });

  it("mounts the browser views on a runtime with browser features", async () => {
    await show(browserView, "");
    expect(host!.querySelector(".browser-stub")).not.toBeNull();
    expect(host!.querySelector(".browser-unavailable")).toBeNull();
    if (root) await act(async () => root!.unmount());
    host?.remove();
    root = null;
    await show(attachView, "");
    expect(host!.querySelector(".attach-stub")).not.toBeNull();
  });
});
