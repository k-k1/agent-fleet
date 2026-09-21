// The managed approval card. What makes it worth a render test rather than a props test is
// the one mistake it exists to prevent: the TUI permission card next to it answers by sending
// keystrokes into a tmux pane, which a managed session does not have. The two must never
// render together, and this one's buttons must produce allow/deny, not keys.
import { describe, it, expect, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

import { ApprovalCard } from "./pendingCards.tsx";
import { isPendingApproval, type PendingApproval } from "../transcript/types.ts";

const APPROVAL: PendingApproval = {
  id: "approval-ap-1",
  request: {
    summary: "rm -rf build",
    tool: "shell",
    command: "rm -rf build | tee /etc/passwd",
    stages: [
      ["rm", "-rf", "build"],
      ["tee", "/etc/passwd"],
    ],
    protectedWrite: true,
    judgeEscalated: true,
  },
};

let root: Root | null = null;
let host: HTMLDivElement | null = null;

function mount(approval: PendingApproval, onAllow = vi.fn(), onDeny = vi.fn(), sending = false) {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  act(() => {
    root!.render(
      <ApprovalCard
        agentName="Muse"
        approval={approval}
        sending={sending}
        onAllow={onAllow}
        onDeny={onDeny}
      />,
    );
  });
  return { onAllow, onDeny };
}

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

function buttons(): HTMLButtonElement[] {
  return Array.from(host!.querySelectorAll("button"));
}

describe("ApprovalCard", () => {
  it("shows the command, not just the summary", () => {
    mount(APPROVAL);
    expect(host!.textContent).toContain("rm -rf build | tee /etc/passwd");
  });

  it("lists every stage of a pipeline", () => {
    // A member approving `a | b` is approving both, and the second one is invisible unless the
    // parsed argv is rendered.
    mount(APPROVAL);
    const stages = Array.from(host!.querySelectorAll(".mt-approval-stages li")).map((li) => li.textContent);
    expect(stages).toEqual(["rm -rf build", "tee /etc/passwd"]);
  });

  it("does not spend a row on the stages of a single command", () => {
    mount({ id: "a", request: { summary: "ls", command: "ls", stages: [["ls"]] } });
    expect(host!.querySelector(".mt-approval-stages")).toBeNull();
  });

  it("shows the runtime's own markings", () => {
    mount(APPROVAL);
    expect(host!.querySelector(".mt-approval-flag.warn")).not.toBeNull();
    expect(host!.querySelectorAll(".mt-approval-flag").length).toBe(2);
  });

  it("offers exactly allow and deny", () => {
    // onRequest mode presents allow_once and abort and nothing else, so there is no scope
    // selector and no "always allow" — offering one would promise a persistence the runtime
    // never agreed to.
    mount(APPROVAL);
    expect(buttons().length).toBe(2);
  });

  it("answers allow and deny through the callbacks, never by keystroke", () => {
    const { onAllow, onDeny } = mount(APPROVAL);
    act(() => buttons()[0].click());
    expect(onAllow).toHaveBeenCalledTimes(1);
    expect(onDeny).not.toHaveBeenCalled();
    act(() => buttons()[1].click());
    expect(onDeny).toHaveBeenCalledTimes(1);
  });

  it("disables both buttons while a reply is in flight", () => {
    // Without this a double click sends two decisions for one approval; the second is refused
    // as already-settled, which is harmless, but the card would flicker back.
    mount(APPROVAL, vi.fn(), vi.fn(), true);
    expect(buttons().every((b) => b.disabled)).toBe(true);
  });

  it("falls back to the summary when there is no command", () => {
    mount({ id: "a", request: { summary: "web_fetch", tool: "web_fetch" } });
    expect(host!.textContent).toContain("web_fetch");
  });
});

describe("isPendingApproval", () => {
  it("accepts a real payload", () => {
    expect(isPendingApproval(APPROVAL)).toBe(true);
  });

  it("refuses a payload the card could not render", () => {
    // The card leads with the summary, so a payload without one would ask the member to allow
    // a blank line.
    expect(isPendingApproval(null)).toBe(false);
    expect(isPendingApproval({})).toBe(false);
    expect(isPendingApproval({ id: "a" })).toBe(false);
    expect(isPendingApproval({ id: "a", request: {} })).toBe(false);
    expect(isPendingApproval({ id: "", request: { summary: "x" } })).toBe(false);
    expect(isPendingApproval({ id: "a", request: { summary: "" } })).toBe(false);
  });
});
