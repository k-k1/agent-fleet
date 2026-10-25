// A create-time initial prompt that settles failed because the Claude login expired must tell
// the member why, once, on the list update that carries the change; and nothing else may.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";

const toastMock = vi.fn();
vi.mock("../../ui/toast.ts", () => ({
  toast: (...a: unknown[]) => toastMock(...a),
  dismissToast: () => {},
}));

const { noteInitialPromptFailures, resetInitialPromptFailuresForTest } = await import("./initialPromptFailure.ts");
import type { Session } from "../../types/session.ts";

const s = (state: Session["initialPromptState"], reason?: string): Session =>
  ({ name: "child-1", kind: "claude", alive: true, initialPromptState: state, initialPromptReason: reason }) as Session;

let root: Root | null = null;
let host: HTMLDivElement;

beforeEach(() => {
  toastMock.mockClear();
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

  it("stays quiet for a failure first seen as failed, a failure without the reason, and a delivery", () => {
    noteInitialPromptFailures([s("failed", "auth_expired")]); // e.g. after a reload
    noteInitialPromptFailures([s("pending")]);
    noteInitialPromptFailures([s("failed")]);
    noteInitialPromptFailures([s("pending")]);
    noteInitialPromptFailures([s("delivered")]);
    expect(toastMock).not.toHaveBeenCalled();
  });
});
