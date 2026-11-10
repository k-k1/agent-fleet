import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const apiMock = vi.fn();
vi.mock("../../core/api/client.ts", async (orig) => ({
  ...(await orig<typeof import("../../core/api/client.ts")>()),
  api: (...a: unknown[]) => apiMock(...a),
}));

import { setTenant, setUser } from "../../core/api/client.ts";
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
  useSigninStartStore.setState({ auto: false, blocked: null, last: "" });
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

  it("asks for a workspace that reads \"none\" (stopped the normal way on docker and native)", async () => {
    render(<Probe />);
    setWs("none");
    input();
    await act(async () => {});
    expect(calls()).toBe(1);
  });

  it("asks after a touch, as a phone sends it", async () => {
    render(<Probe />);
    setWs("stopped");
    act(() => void window.dispatchEvent(new Event("touchstart")));
    await act(async () => {});
    expect(calls()).toBe(1);
  });

  it("does not let a refusal for policy silence the next page after the switch is turned on", async () => {
    apiMock.mockImplementationOnce(async () => ({ started: false, reason: "tenant_off" }));
    const { unmount } = render(<Probe />);
    setWs("stopped");
    input();
    await act(async () => {});
    expect(calls()).toBe(1);
    expect(useSigninStartStore.getState().last).toBe("tenant_off");
    expect(Object.keys(localStorage).filter((k) => k.startsWith("af.signinStart"))).toHaveLength(0);
    unmount();
    // The administrator turns the switch on; the next sign-in asks again and starts.
    useWorkspaceStore.setState({ state: "…" });
    render(<Probe />);
    setWs("stopped");
    input();
    await act(async () => {});
    expect(calls()).toBe(2);
    expect(useSigninStartStore.getState().last).toBe("started");
  });

  it("keeps the limit note while the workspace reads \"none\"", async () => {
    apiMock.mockImplementation(async () => ({ started: false, reason: "quota_workspaces" }));
    render(<Probe />);
    setWs("none");
    input();
    await act(async () => {});
    expect(useSigninStartStore.getState().blocked).not.toBeNull();
  });

  it("treats a page opened by navigation as the sign-in input", async () => {
    const spy = vi.spyOn(performance, "getEntriesByType").mockReturnValue([{ type: "navigate" } as unknown as PerformanceEntry]);
    try {
      render(<Probe />);
      setWs("stopped");
      await act(async () => {});
      expect(calls()).toBe(1);
    } finally {
      spy.mockRestore();
    }
  });

  it("does not treat a reload as input", async () => {
    const spy = vi.spyOn(performance, "getEntriesByType").mockReturnValue([{ type: "reload" } as unknown as PerformanceEntry]);
    try {
      render(<Probe />);
      setWs("stopped");
      await act(async () => {});
      expect(calls()).toBe(0);
    } finally {
      spy.mockRestore();
    }
  });

  it("does not ask from a hidden tab", async () => {
    const spy = vi.spyOn(document, "visibilityState", "get").mockReturnValue("hidden");
    try {
      render(<Probe />);
      setWs("stopped");
      input();
      await act(async () => {});
      expect(calls()).toBe(0);
    } finally {
      spy.mockRestore();
    }
  });

  it("does not start the cooldown when the request got no answer, and retries on a later load", async () => {
    apiMock.mockImplementationOnce(async () => {
      throw new Error("network");
    });
    const { unmount } = render(<Probe />);
    setWs("stopped");
    input();
    await act(async () => {});
    expect(calls()).toBe(1);
    expect(Object.keys(localStorage).filter((k) => k.startsWith("af.signinStart"))).toHaveLength(0);
    unmount();
    useWorkspaceStore.setState({ state: "…" });
    render(<Probe />);
    setWs("stopped");
    input();
    await act(async () => {});
    expect(calls()).toBe(2);
  });

  it("scopes the cooldown to tenant and member, so another account or tenant asks again", async () => {
    setTenant("acme");
    setUser("a@example.com");
    const { unmount } = render(<Probe />);
    setWs("stopped");
    input();
    await act(async () => {});
    expect(calls()).toBe(1);
    const keys = Object.keys(localStorage).filter((k) => k.startsWith("af.signinStart.at."));
    expect(keys).toEqual(["af.signinStart.at.acme.a@example.com"]);
    unmount();
    // Same tab, another account (no logout, so localStorage survives): not suppressed.
    setUser("b@example.com");
    useWorkspaceStore.setState({ state: "…" });
    const second = render(<Probe />);
    setWs("stopped");
    input();
    await act(async () => {});
    expect(calls()).toBe(2);
    second.unmount();
    // Same account, another tenant.
    setTenant("beta");
    useWorkspaceStore.setState({ state: "…" });
    render(<Probe />);
    setWs("stopped");
    input();
    await act(async () => {});
    expect(calls()).toBe(3);
    setTenant("");
    setUser("");
  });

  it("drops the limit note once the workspace leaves stopped", async () => {
    apiMock.mockImplementation(async () => ({ started: false, reason: "quota_workspaces", message: "m" }));
    render(<Probe />);
    setWs("stopped");
    input();
    await act(async () => {});
    expect(useSigninStartStore.getState().blocked).not.toBeNull();
    setWs("running");
    expect(useSigninStartStore.getState().blocked).toBeNull();
  });

  it("does nothing when disabled (not booted, or a pop-out tab)", async () => {
    render(<Probe enabled={false} />);
    setWs("stopped");
    input();
    await act(async () => {});
    expect(calls()).toBe(0);
  });
});
