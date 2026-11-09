import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const apiMock = vi.fn();
vi.mock("../../core/api/client.ts", async (orig) => ({
  ...(await orig<typeof import("../../core/api/client.ts")>()),
  api: (...a: unknown[]) => apiMock(...a),
}));

import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { useSigninStart, useSigninStartStore } from "./signinStart.ts";

function Probe({ enabled = true }: { enabled?: boolean }) {
  useSigninStart(enabled);
  return null;
}

const setWs = (state: string) => act(() => useWorkspaceStore.setState({ state, bootPhase: "" }));
const input = () => act(() => void window.dispatchEvent(new Event("keydown")));
const calls = () => apiMock.mock.calls.filter((c) => c[0] === "api/workspace/signin-start").length;

beforeEach(() => {
  localStorage.clear();
  apiMock.mockReset();
  apiMock.mockImplementation(async (path: string) => (path === "api/workspace" ? { state: "starting" } : { started: true }));
  useSigninStartStore.setState({ auto: false, blocked: null });
  useWorkspaceStore.setState({ state: "…", bootPhase: "" });
});

let root: Root | null = null;
let host: HTMLElement | null = null;
const render = (el: React.ReactElement) => {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  act(() => root!.render(el));
  const r = root;
  return { unmount: () => act(() => r.unmount()) };
};
afterEach(() => {
  if (root) act(() => root!.unmount());
  root = null;
  host?.remove();
});

describe("useSigninStart", () => {
  it("does not ask for a tab that was opened and never used", async () => {
    render(<Probe />);
    setWs("stopped");
    await Promise.resolve();
    expect(calls()).toBe(0);
  });

  it("asks once after input on a stopped workspace, not again on more input or a reload", async () => {
    const { unmount } = render(<Probe />);
    setWs("stopped");
    input();
    input();
    await act(async () => {});
    expect(calls()).toBe(1);
    expect(useSigninStartStore.getState().auto).toBe(true);
    unmount();
    // A reload within the cooldown: a new page load, the stamp is shared through localStorage.
    useWorkspaceStore.setState({ state: "…" });
    render(<Probe />);
    setWs("stopped");
    input();
    await act(async () => {});
    expect(calls()).toBe(1);
  });

  it("never asks while the state is unknown (a Control Plane outage)", async () => {
    render(<Probe />);
    setWs("unknown");
    input();
    await act(async () => {});
    expect(calls()).toBe(0);
    setWs("stopped");
    input();
    await act(async () => {});
    // The first definite state is stopped, so this is the page's cold start: it asks.
    expect(calls()).toBe(1);
  });

  it("does not restart a workspace that stopped while the tab was open", async () => {
    render(<Probe />);
    setWs("running");
    setWs("stopped");
    input();
    await act(async () => {});
    expect(calls()).toBe(0);
  });

  it("keeps the reason when the workspace limit is reached", async () => {
    apiMock.mockImplementation(async () => ({ started: false, reason: "quota_workspaces", message: "tenant workspace limit reached (2)" }));
    render(<Probe />);
    setWs("stopped");
    input();
    await act(async () => {});
    expect(useSigninStartStore.getState().blocked?.reason).toBe("quota_workspaces");
    expect(useSigninStartStore.getState().auto).toBe(false);
  });

  it("does nothing when disabled (not booted, or a pop-out tab)", async () => {
    render(<Probe enabled={false} />);
    setWs("stopped");
    input();
    await act(async () => {});
    expect(calls()).toBe(0);
  });
});
