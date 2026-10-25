// A create-time initial prompt that settles failed because the Claude login expired must tell
// the member why, once, on the list update that carries the change; and nothing else may.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";

const toastMock = vi.fn();
vi.mock("../../ui/toast.ts", () => ({
  toast: (...a: unknown[]) => toastMock(...a),
  dismissToast: (...a: unknown[]) => dismissMock(...a),
}));

let scopeUser = "u";
let scopeTenant = "a";
vi.mock("../../core/api/client.ts", () => ({
  getUser: () => scopeUser,
  getTenant: () => scopeTenant,
}));
const dismissMock = vi.fn();

const { noteInitialPromptFailures, resetInitialPromptFailuresForTest } = await import("./initialPromptFailure.ts");
import type { Session } from "../../types/session.ts";

const s = (state: Session["initialPromptState"], reason?: string, name = "child-1"): Session =>
  ({ name, kind: "claude", alive: true, initialPromptState: state, initialPromptReason: reason }) as Session;

let root: Root | null = null;
let host: HTMLDivElement;

beforeEach(() => {
  toastMock.mockClear();
  dismissMock.mockClear();
  scopeUser = "u";
  scopeTenant = "a";
  resetInitialPromptFailuresForTest();
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});
afterEach(async () => {
  await act(async () => root?.unmount());
  host.remove();
});

describe("noteInitialPromptFailures", () => {
  it("toasts the reason, sticky, when a pending prompt settles failed with auth_expired", async () => {
    noteInitialPromptFailures([s("pending")]);
    expect(toastMock).not.toHaveBeenCalled();
    noteInitialPromptFailures([s("failed", "auth_expired")]);
    expect(toastMock).toHaveBeenCalledTimes(1);
    const [node, opts] = toastMock.mock.calls[0] as [ReactNode, { duration?: number }];
    expect(opts.duration).toBe(0);
    await act(async () => root!.render(<>{node}</>));
    expect(host.textContent).toContain("child-1");
    expect(host.textContent).toContain("期限切れ");
    // The same failed list arriving again (the 4s poll) must not toast twice.
    noteInitialPromptFailures([s("failed", "auth_expired")]);
    expect(toastMock).toHaveBeenCalledTimes(1);
  });

  it("toasts a create whose FIRST list already says failed, with no pending in between", () => {
    noteInitialPromptFailures([{ name: "other", kind: "shell", alive: true } as Session]); // the opening snapshot
    noteInitialPromptFailures([{ name: "other", kind: "shell", alive: true } as Session, s("failed", "auth_expired")]);
    expect(toastMock).toHaveBeenCalledTimes(1);
  });

  it("stays quiet for failures in the opening snapshot, without the reason, and for a delivery", () => {
    noteInitialPromptFailures([s("failed", "auth_expired")]); // e.g. after a reload
    noteInitialPromptFailures([s("failed", "auth_expired")]);
    expect(toastMock).not.toHaveBeenCalled();
    noteInitialPromptFailures([s("pending", undefined, "b"), s("failed", "auth_expired")]);
    noteInitialPromptFailures([s("failed", undefined, "b"), s("failed", "auth_expired")]);
    noteInitialPromptFailures([s("delivered", undefined, "c"), s("failed", "auth_expired")]);
    expect(toastMock).not.toHaveBeenCalled();
  });

  it("does not carry observations across a tenant switch, and withdraws the old sticky toast", () => {
    noteInitialPromptFailures([s("pending")]);
    noteInitialPromptFailures([s("failed", "auth_expired")]);
    expect(toastMock).toHaveBeenCalledTimes(1);
    const keyA = (toastMock.mock.calls[0][1] as { key: string }).key;
    scopeTenant = "b";
    // Workspace B already holds a same-named failed session: the opening snapshot, so old news.
    noteInitialPromptFailures([s("failed", "auth_expired")]);
    expect(toastMock).toHaveBeenCalledTimes(1);
    expect(dismissMock).toHaveBeenCalledWith(keyA);
  });

  it("keys two sessions of one scope apart, and the same name in two scopes apart", () => {
    noteInitialPromptFailures([s("pending"), s("pending", undefined, "child-2")]);
    noteInitialPromptFailures([s("failed", "auth_expired"), s("failed", "auth_expired", "child-2")]);
    const keys = toastMock.mock.calls.map((c) => (c[1] as { key: string }).key);
    expect(new Set(keys).size).toBe(2);
    scopeTenant = "b";
    noteInitialPromptFailures([s("pending")]);
    noteInitialPromptFailures([s("failed", "auth_expired")]);
    const keyB = (toastMock.mock.calls[2][1] as { key: string }).key;
    expect(keys).not.toContain(keyB);
  });
});
