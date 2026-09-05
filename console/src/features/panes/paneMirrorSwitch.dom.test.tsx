// Regression guard for switching a tab from a file to a mirrored session in a tabbed grid: the
// raw TUI must not flash before the mirror appears.
//
// In the tabbed profile one cell reuses one Pane instance, so selecting another tab only swaps
// the pane props and never remounts (PaneHost keys on cell.id). Whether the mirror is shown is
// Pane's local state, so making it follow via an effect means "commit one frame with the stale
// state -> the browser paints -> the effect corrects it", and the bare TerminalView is visible
// for that frame.
//
// jsdom has no layout, so visibility cannot be measured. Instead the commit history is read with
// a MutationObserver: a record of the .view wrapping the terminal being inserted without hidden
// and only later given hidden is exactly that one bare frame.
import { describe, it, expect, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { Pane } from "./Pane.tsx";
import { useSessionsStore } from "../sessions/store.ts";
import type { Cell, PaneView } from "../../layout/types.ts";
import type { Session } from "../../types/session.ts";

// The contents of terminal / mirror / file are not this test's concern (the real ones open a
// PTY / SSE / fetch); knowing which of them mounted is enough.
vi.mock("../terminal/TerminalView.tsx", () => ({
  TerminalView: () => <div className="term-stub" />,
}));
vi.mock("../mirror/MirrorView.tsx", () => ({
  MirrorView: () => <div className="mirror-stub" />,
}));
vi.mock("../viewer/FileView.tsx", () => ({
  FileView: () => <div className="file-stub" />,
}));
// The session action menu requires the Confirm/Toast providers; it is for right-clicking a tab
// and is never opened here.
vi.mock("../sessions/useSessionActions.tsx", () => ({
  useSessionActions: () => ({}),
}));

const SESSION: Session = { name: "s1", kind: "claude", driver: "tui", alive: true, title: "作業" };

const fileView: PaneView = { id: "v-file", session: null, content: { kind: "file", filePath: "/w/a.ts" }, wrap: null };
const termView: PaneView = { id: "v-term", session: "s1", content: { kind: "terminal", chat: true }, wrap: null };
const cellWith = (selectedViewId: string): Cell => ({ id: "c1", selectedViewId, views: [fileView, termView] });

const noop = () => {};

describe("tabbed pane: file tab → mirrored session tab", () => {
  let root: Root | null = null;
  let host: HTMLElement | null = null;

  afterEach(async () => {
    if (root) await act(async () => root!.unmount());
    host?.remove();
    root = null;
    host = null;
  });

  it("never commits a visible terminal on the way to the mirror", async () => {
    useSessionsStore.setState({ sessions: [SESSION] });
    host = document.createElement("div");
    document.body.appendChild(host);

    await act(async () => {
      root = createRoot(host!);
      root.render(
        <Pane
          cell={cellWith("v-file")}
          pane={fileView}
          tabbed
          onActivate={noop}
          onClose={noop}
          onSwap={noop}
          onDropSplit={noop}
        />,
      );
    });
    expect(host.querySelector(".file-stub")).not.toBeNull();

    // The tab switch starts here; record everything that happens to the DOM from now on.
    const seen: MutationRecord[] = [];
    const obs = new MutationObserver((rs) => seen.push(...rs));
    obs.observe(host, { childList: true, subtree: true, attributes: true, attributeOldValue: true });

    await act(async () => {
      root!.render(
        <Pane
          cell={cellWith("v-term")}
          pane={termView}
          sessionMeta={SESSION}
          tabbed
          onActivate={noop}
          onClose={noop}
          onSwap={noop}
          onDropSplit={noop}
        />,
      );
    });
    seen.push(...obs.takeRecords());
    const records = seen;
    obs.disconnect();

    // It settles on the mirror. The terminal stays mounted, to keep the PTY and the scrollback,
    // but hidden.
    const view = host.querySelector(".view") as HTMLElement | null;
    expect(host.querySelector(".mirror-stub")).not.toBeNull();
    expect(view).not.toBeNull();
    expect(view!.querySelector(".term-stub")).not.toBeNull();
    expect(view!.hasAttribute("hidden")).toBe(true);

    // What happened in between: .view's hidden must have been present from insertion. A record
    // of it being added later means there was a commit in which the bare terminal was visible.
    const lateHide = records.filter(
      (r) =>
        r.type === "attributes" &&
        r.attributeName === "hidden" &&
        (r.target as HTMLElement).classList?.contains("view"),
    );
    expect(lateHide).toEqual([]);
  });
});
