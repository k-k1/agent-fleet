// Regression guard for #1025: the pane's resume button on a stopped SSM session must go through
// the SSO login modal (useSessionUI.openSsmResume), the same route as the rail menu's resume,
// instead of POSTing /start directly and leaving the device code only inside the terminal.
// A stopped shell session keeps resuming directly.
import { describe, it, expect, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { Pane } from "./Pane.tsx";
import { useSessionsStore } from "../sessions/store.ts";
import { useSessionUI } from "../sessions/ui.ts";
import type { Cell, PaneView } from "../../layout/types.ts";
import type { Session } from "../../types/session.ts";

// The real TerminalView opens a PTY; a stub that exposes the resume callback is enough.
vi.mock("../terminal/TerminalView.tsx", () => ({
  TerminalView: ({ onResume }: { onResume?: () => void }) => (
    <button type="button" className="resume-stub" onClick={() => onResume?.()} />
  ),
}));
vi.mock("../mirror/MirrorView.tsx", () => ({
  MirrorView: () => <div className="mirror-stub" />,
}));
// The session action menu requires the Confirm/Toast providers; it is never opened here.
vi.mock("../sessions/useSessionActions.tsx", () => ({
  useSessionActions: () => ({}),
}));

const view: PaneView = { id: "v1", session: "s1", content: { kind: "terminal", chat: false }, wrap: null };
const cell: Cell = { id: "c1", selectedViewId: "v1", views: [view] };
const noop = () => {};

describe("pane resume button on a stopped session", () => {
  let root: Root | null = null;
  let host: HTMLElement | null = null;

  afterEach(async () => {
    if (root) await act(async () => root!.unmount());
    host?.remove();
    root = null;
    host = null;
    useSessionUI.getState().close();
    vi.restoreAllMocks();
  });

  const pressResume = async (meta: Session) => {
    useSessionsStore.setState({ sessions: [meta] });
    const start = vi.spyOn(useSessionsStore.getState(), "start").mockResolvedValue(true);
    // The spy replaces the method on the state object; put that object back so the hook reads it.
    useSessionsStore.setState({ start: useSessionsStore.getState().start });
    host = document.createElement("div");
    document.body.appendChild(host);
    await act(async () => {
      root = createRoot(host!);
      root.render(
        <Pane cell={cell} pane={view} sessionMeta={meta} onActivate={noop} onClose={noop} onSwap={noop} onDropSplit={noop} />,
      );
    });
    const btn = host.querySelector(".resume-stub") as HTMLButtonElement | null;
    expect(btn).not.toBeNull();
    await act(async () => btn!.click());
    return start;
  };

  it("opens the SSO login modal for ssm and does not POST /start itself", async () => {
    const start = await pressResume({ name: "s1", kind: "ssm", alive: false, title: "ssm" });
    expect(useSessionUI.getState().ssmResume).toEqual({ name: "s1", force: false });
    expect(start).not.toHaveBeenCalled();
  });

  it("starts a shell session directly without the modal", async () => {
    const start = await pressResume({ name: "s1", kind: "shell", alive: false, title: "shell" });
    expect(start).toHaveBeenCalledWith("s1");
    expect(useSessionUI.getState().ssmResume).toBeNull();
  });
});
