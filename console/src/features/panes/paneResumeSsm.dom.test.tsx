// Regression guard for #1025: the pane's resume button on a stopped SSM session must go through
// the SSO login modal (useSessionUI.openSsmResume), the same route as the rail menu's resume,
// instead of POSTing /start directly and leaving the device code only inside the terminal.
// A stopped shell session keeps resuming directly, and while the modal is open the pane holds
// its attach (attaching resizes the tmux window the modal scrapes the device URL from).
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
  TerminalView: ({ onResume, attached }: { onResume?: () => void; attached?: boolean }) => (
    <button type="button" className="resume-stub" data-attached={String(!!attached)} onClick={() => onResume?.()} />
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

const realStart = useSessionsStore.getState().start;

describe("pane resume button on a stopped session", () => {
  let root: Root | null = null;
  let host: HTMLElement | null = null;

  afterEach(async () => {
    if (root) await act(async () => root!.unmount());
    host?.remove();
    root = null;
    host = null;
    useSessionUI.getState().close();
    useSessionsStore.setState({ sessions: [], start: realStart });
  });

  const render = async (meta: Session) => {
    useSessionsStore.setState({ sessions: [meta] });
    await act(async () => {
      root!.render(
        <Pane cell={cell} pane={view} sessionMeta={meta} onActivate={noop} onClose={noop} onSwap={noop} onDropSplit={noop} />,
      );
    });
  };
  const stub = () => host!.querySelector(".resume-stub") as HTMLButtonElement;

  const pressResume = async (meta: Session) => {
    // Replace the store method outright (a spy on the state object would be copied into the
    // next state by setState and outlive restoreAllMocks).
    const start = vi.fn(async () => true);
    useSessionsStore.setState({ start });
    host = document.createElement("div");
    document.body.appendChild(host);
    root = createRoot(host);
    await render(meta);
    expect(stub()).not.toBeNull();
    await act(async () => stub().click());
    return start;
  };

  it("opens the SSO login modal for ssm and does not POST /start itself", async () => {
    const start = await pressResume({ name: "s1", kind: "ssm", alive: false, title: "ssm" });
    expect(useSessionUI.getState().ssmResume).toEqual({ name: "s1", force: false });
    expect(start).not.toHaveBeenCalled();
    expect(stub().dataset.attached).toBe("false");
  });

  it("holds the attach while the modal is open and attaches once it closes", async () => {
    await pressResume({ name: "s1", kind: "ssm", alive: false, title: "ssm" });
    // The login runs inside the session's pane, so the list reports it alive mid-login.
    await render({ name: "s1", kind: "ssm", alive: true, title: "ssm" });
    expect(stub().dataset.attached).toBe("false");
    await act(async () => useSessionUI.getState().close());
    expect(stub().dataset.attached).toBe("true");
  });

  it("starts a shell session directly without the modal", async () => {
    const start = await pressResume({ name: "s1", kind: "shell", alive: false, title: "shell" });
    expect(start).toHaveBeenCalledWith("s1");
    expect(useSessionUI.getState().ssmResume).toBeNull();
    expect(stub().dataset.attached).toBe("true");
  });
});
