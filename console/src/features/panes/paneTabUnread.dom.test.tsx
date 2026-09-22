// The unread dot in the tab strip: a tab whose session has a notification nobody has opened.
//
// Two things are worth pinning. A background tab is the case the dot exists for — its session
// is off screen, so nothing else in the window says an answer came back. And the SELECTED tab
// must never wear one: it is on screen, the acknowledgement is already on its way, and a dot on
// the pane the user is reading would sit there for the whole round-trip (or forever, while the
// Control Plane is unreachable) telling them to look at what they are looking at.
import { afterEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { Pane } from "./Pane.tsx";
import { useSessionsStore } from "../sessions/store.ts";
import { useNotificationStore, type FleetNotification } from "../notifications/store.ts";
import type { Cell, PaneView } from "../../layout/types.ts";
import type { Session } from "../../types/session.ts";

vi.mock("../terminal/TerminalView.tsx", () => ({ TerminalView: () => <div className="term-stub" /> }));
vi.mock("../mirror/MirrorView.tsx", () => ({ MirrorView: () => <div className="mirror-stub" /> }));
vi.mock("../sessions/useSessionActions.tsx", () => ({ useSessionActions: () => ({}) }));

const SESSIONS: Session[] = [
  { name: "front", kind: "claude", driver: "tui", alive: true, title: "前" },
  { name: "behind", kind: "claude", driver: "tui", alive: true, title: "奥" },
];

const tabView = (id: string, session: string): PaneView => ({ id, session, content: { kind: "terminal", chat: true }, wrap: null });
const frontView = tabView("v-front", "front");
const behindView = tabView("v-behind", "behind");
const cellWith = (selectedViewId: string): Cell => ({ id: "c1", selectedViewId, views: [frontView, behindView] });

const event = (session: string): FleetNotification => ({
  seq: 1, id: "e-" + session, kind: "answer-ready", target: { type: "session", id: session },
  displayName: session, payload: {}, createdAt: "2026-09-22T00:00:00Z", seen: false,
});

const noop = () => {};

describe("tabbed pane: unread dot", () => {
  let root: Root | null = null;
  let host: HTMLElement | null = null;

  const show = async (items: FleetNotification[]) => {
    useSessionsStore.setState({ sessions: SESSIONS });
    useNotificationStore.setState({ items });
    host = document.createElement("div");
    document.body.appendChild(host);
    await act(async () => {
      root = createRoot(host!);
      root.render(
        <Pane cell={cellWith("v-front")} pane={frontView} sessionMeta={SESSIONS[0]} tabbed
          onActivate={noop} onClose={noop} onSwap={noop} onDropSplit={noop} />,
      );
    });
  };

  /** The dot of the tab at `index` in the strip, or null. */
  const dotAt = (index: number) =>
    host!.querySelectorAll<HTMLElement>(".pane-tab")[index]?.querySelector<HTMLElement>(".unread-dot") ?? null;

  afterEach(async () => {
    if (root) await act(async () => root!.unmount());
    host?.remove();
    root = null;
    host = null;
    useNotificationStore.setState({ items: [] });
  });

  it("marks the background tab and leaves the selected one alone", async () => {
    await show([event("front"), event("behind")]);
    // Both tabs are in the strip and only one of them is selected — otherwise the assertions
    // below would hold for the wrong reason.
    expect(host!.querySelectorAll(".pane-tab")).toHaveLength(2);
    expect(host!.querySelectorAll(".pane-tab.selected")).toHaveLength(1);
    expect(dotAt(0)).toBeNull(); // v-front is the selected tab
    expect(dotAt(1)).not.toBeNull();
  });

  it("shows nothing at all once the background tab's notification is seen", async () => {
    await show([{ ...event("behind"), seen: true }]);
    expect(host!.querySelector(".unread-dot")).toBeNull();
  });
});
