// An image-studio tab in a tabbed cell: named after its studio, not "image generation", and
// right-clicking it opens the menu of the session bound to the studio. The tab strip never
// mounts the studio pane, so both come from the window's studio cache and the session list.
import { afterEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { Pane } from "./Pane.tsx";
import { useSessionsStore } from "../sessions/store.ts";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { useStudioCache } from "../imagegen/studioCache.ts";
import type { Cell, PaneView } from "../../layout/types.ts";
import type { Session } from "../../types/session.ts";

vi.mock("../terminal/TerminalView.tsx", () => ({ TerminalView: () => <div className="term-stub" /> }));
vi.mock("../mirror/MirrorView.tsx", () => ({ MirrorView: () => <div className="mirror-stub" /> }));
vi.mock("../imagegen/ImagegenView.tsx", () => ({ ImagegenView: () => <div className="igen-stub" /> }));
vi.mock("../sessions/useSessionActions.tsx", () => ({ useSessionActions: () => ({}) }));
vi.mock("../sessions/SessionMenu.tsx", () => ({
  SessionMenu: ({ s, open }: { s: Session; open: boolean }) => (open ? <div className="menu-stub">{s.name}</div> : null),
}));
// The tab strip asks for the list itself when no studio pane has read it yet.
const listed: { studios: { id: string; title: string; updated_at: string }[] } = { studios: [] };
vi.mock("../imagegen/api.ts", () => ({ listStudios: async () => listed }));

const SESSIONS: Session[] = [
  { name: "front", kind: "claude", driver: "tui", alive: true, title: "前" },
  { name: "painter", kind: "claude", driver: "tui", alive: true, title: "絵描き", studio: "st-1" },
];

const frontView: PaneView = { id: "v-front", session: "front", content: { kind: "terminal", chat: true }, wrap: null };
const studioView: PaneView = { id: "v-studio", session: null, content: { kind: "imagegen", studioId: "st-1" }, wrap: null };
const cell: Cell = { id: "c1", selectedViewId: "v-front", views: [frontView, studioView] };
const noop = () => {};

describe("tabbed pane: image studio tab", () => {
  let root: Root | null = null;
  let host: HTMLElement | null = null;

  const show = async () => {
    useWorkspaceStore.setState({ state: "running" });
    useSessionsStore.setState({ sessions: SESSIONS });
    host = document.createElement("div");
    document.body.appendChild(host);
    await act(async () => {
      root = createRoot(host!);
      root.render(
        <Pane cell={cell} pane={frontView} sessionMeta={SESSIONS[0]} tabbed
          onActivate={noop} onClose={noop} onSwap={noop} onDropSplit={noop} />,
      );
    });
  };
  const studioTab = () => host!.querySelectorAll<HTMLElement>(".pane-tab")[1];

  afterEach(async () => {
    if (root) await act(async () => root!.unmount());
    host?.remove();
    root = null;
    host = null;
    useStudioCache.setState({ list: [], byId: {}, status: null });
  });

  it("is named after its studio, from the list the strip reads itself", async () => {
    listed.studios = [{ id: "st-1", title: "港の夕暮れ", updated_at: "2026-09-27T10:00:00Z" }];
    await show();
    await act(async () => {});
    expect(studioTab().querySelector(".pane-tab-title")?.textContent).toBe("港の夕暮れ");
  });

  it("falls back to the generic name while nothing is known about the studio", async () => {
    useStudioCache.setState({ list: [], byId: {}, status: null });
    await show();
    expect(studioTab().querySelector(".pane-tab-title")?.textContent).toMatch(/画像生成|Image/);
  });

  it("opens the bound session's menu on right-click", async () => {
    await show();
    const tab = studioTab();
    await act(async () => {
      tab.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 10, clientY: 10 }));
    });
    expect(host!.querySelector(".menu-stub")?.textContent).toBe("painter");
  });
});
