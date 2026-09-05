// The offering side's gate (docs/log/77 §77.5 / ADR 0057 decision 5).
//
// What this holds is that an unpushed handoff can never be sent: the owner's commits are
// not on the recipient's disk, so however well written the prompt is, the handoff is a lie.
// A branch that was never pushed has ahead = 0, so the naive implementation lets exactly
// that case through.
//
// The verdict is built by the Agent and relayed unchanged by the CP. This surface only
// displays it and blocks sending; that it does NOT recompute the condition client-side is
// itself what is under test.
import { describe, it, expect, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn(async (..._args: unknown[]) => ({}) as Record<string, unknown>);
vi.mock("../../core/api/client.ts", () => ({
  api: (path: string) => api(path),
  apiJSON: (path: string, method: string, body: unknown) => apiJSON(path, method, body),
  errText: (e: unknown) => String((e as { message?: string })?.message ?? e),
  getTenant: () => "",
}));
// The share-creation modal is a separate feature; all that matters here is that a way out
// exists rather than a dead end.
vi.mock("./ShareCreateModal.tsx", () => ({ ShareCreateModal: () => <div data-share-modal /> }));

import { HandoffOfferModal } from "./HandoffOfferModal.tsx";
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function render(payload: unknown) {
  api.mockReset();
  api.mockImplementation(async () => payload);
  host = document.createElement("div");
  document.body.appendChild(host);
  await act(async () => {
    root = createRoot(host!);
    root.render(
      <ToastProvider>
        <HandoffOfferModal session="s1" initialTitle="続き" initialPrompt="ここから続けて" onClose={() => {}} />
      </ToastProvider>,
    );
  });
  await act(async () => {
    await Promise.resolve();
  });
}

// Modal renders through a portal directly under document.body: looking inside host always
// finds nothing, and the test then compares undefined instead of reporting a missing
// button.
function sendButton(): HTMLButtonElement | undefined {
  return [...document.querySelectorAll("button")].find((b) => b.getAttribute("type") === "submit") as
    | HTMLButtonElement
    | undefined;
}
function find(sel: string): Element | null {
  return document.querySelector(sel);
}

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

const MEMBERS = [{ userKey: "b-example-com", email: "b@example.com" }];

describe("HandoffOfferModal", () => {
  it("allows sending when the branch is pushed and clean", async () => {
    await render({ members: MEMBERS, context: { vcs: "git", branch: "main", headSha: "abcdef1234", remote: "https://x/y.git", ahead: 0 } });
    expect(sendButton()?.disabled).toBe(false);
  });

  it("blocks a branch that was never pushed, even though ahead is 0", async () => {
    await render({
      members: MEMBERS,
      context: { vcs: "git", branch: "temp/x", ahead: 0, noUpstream: true, blocked: "no_upstream" },
    });
    expect(sendButton()?.disabled).toBe(true);
    expect(find(".handoff-blocked")).toBeTruthy();
  });

  it("blocks sending while there are unpushed commits", async () => {
    await render({ members: MEMBERS, context: { vcs: "git", branch: "main", ahead: 2, blocked: "unpushed_commits" } });
    expect(sendButton()?.disabled).toBe(true);
  });

  it("warns rather than blocks on uncommitted changes, and unblocks on acknowledgement", async () => {
    await render({ members: MEMBERS, context: { vcs: "git", branch: "main", ahead: 0, dirty: true, warning: "uncommitted_changes" } });
    expect(sendButton()?.disabled).toBe(true);
    const ack = find('input[type="checkbox"]') as HTMLInputElement;
    expect(ack).toBeTruthy();
    await act(async () => {
      ack.click();
    });
    expect(sendButton()?.disabled).toBe(false);
  });

  // Not having shared the session yet is a normal state, so the CP answers 200 with an
  // empty candidate list. The screen has to say "share it first" and offer the way to do
  // so on the spot: a fetch-failed message, or a stuck loading state, leaves the member
  // with no idea what to do next.
  it("explains that an unshared session must be shared first, and offers the way there", async () => {
    await render({ members: [], context: { vcs: "git", branch: "main", ahead: 0 } });
    expect(sendButton()?.disabled).toBe(true);
    const notShared = find(".handoff-blocked");
    expect(notShared?.textContent || "").toContain("共有");
    const labels = [...document.querySelectorAll("button")].map((b) => b.textContent || "");
    expect(labels.some((t) => t.includes("共有"))).toBe(true);
    // No leftover loading indicator: it would read as being stuck.
    expect(document.body.textContent || "").not.toContain("読み込み中");
  });
});
