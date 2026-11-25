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
  // The send goes through api() with a deadline (boundedRequest); route a JSON write to the
  // apiJSON spy so the body assertions below read the same either way.
  api: (path: string, opts?: RequestInit) =>
    opts?.method && opts.method !== "GET" ? apiJSON(path, opts.method, JSON.parse(String(opts.body))) : api(path),
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

  // "Push, then send" (#2072). The opt-in exists only for the two reasons a push clears, says
  // what it will publish, and sends pushFirst so the CP pushes and re-asks the Agent; the
  // verdict after the push is never decided here.
  describe("push, then send", () => {
    const pushBox = () =>
      [...document.querySelectorAll("label.handoff-push input")][0] as HTMLInputElement | undefined;

    it.each([
      ["no_upstream", { noUpstream: true, ahead: 0 }],
      ["unpushed_commits", { ahead: 2 }],
    ])("offers the push for %s, with branch, remote and commit count", async (reason, extra) => {
      await render({
        members: MEMBERS,
        context: {
          vcs: "git",
          branch: "temp/x",
          headSha: "abc123",
          blocked: reason,
          pushRemote: "https://x/y.git",
          pushCommits: 2,
          ...extra,
        },
      });
      expect(sendButton()?.disabled).toBe(true);
      const box = pushBox();
      expect(box).toBeTruthy();
      const summary = find(".handoff-push-summary")?.textContent || "";
      expect(summary).toContain("temp/x");
      expect(summary).toContain("https://x/y.git");
      expect(summary).toContain("2");
      await act(async () => {
        box!.click();
      });
      expect(sendButton()?.disabled).toBe(false);
      apiJSON.mockResolvedValueOnce({ id: "o1" });
      await act(async () => {
        sendButton()!.closest("form")!.requestSubmit();
      });
      expect(apiJSON).toHaveBeenCalledWith(
        "api/session-handoff-offers",
        "POST",
        expect.objectContaining({
          pushFirst: true,
          pushExpect: { branch: "temp/x", headSha: "abc123", pushRemote: "https://x/y.git" },
        }),
      );
    });

    it("does not offer a push for a detached HEAD or an unknown state", async () => {
      for (const blocked of ["detached_head", "something_new"]) {
        await render({ members: MEMBERS, context: { vcs: "git", branch: "x", blocked, pushRemote: "origin" } });
        expect(pushBox()).toBeUndefined();
        expect(sendButton()?.disabled).toBe(true);
        act(() => root?.unmount());
        host?.remove();
      }
    });

    it("does not offer a push when there is nowhere to push to", async () => {
      await render({ members: MEMBERS, context: { vcs: "git", branch: "x", blocked: "no_upstream" } });
      expect(pushBox()).toBeUndefined();
    });

    it("still asks for the uncommitted-changes acknowledgement when pushing", async () => {
      await render({
        members: MEMBERS,
        context: { vcs: "git", branch: "x", blocked: "unpushed_commits", pushRemote: "origin", pushCommits: 1, dirty: true, warning: "uncommitted_changes" },
      });
      await act(async () => {
        pushBox()!.click();
      });
      expect(sendButton()?.disabled).toBe(true);
      const boxes = [...document.querySelectorAll('input[type="checkbox"]')] as HTMLInputElement[];
      expect(boxes).toHaveLength(2);
      await act(async () => {
        boxes[1].click();
      });
      expect(sendButton()?.disabled).toBe(false);
    });

    it("shows git's refusal in the modal and does not close", async () => {
      const onClose = vi.fn();
      api.mockReset();
      api.mockImplementation(async () => ({
        members: MEMBERS,
        context: { vcs: "git", branch: "x", blocked: "unpushed_commits", pushRemote: "origin", pushCommits: 1 },
      }));
      host = document.createElement("div");
      document.body.appendChild(host);
      await act(async () => {
        root = createRoot(host!);
        root.render(
          <ToastProvider>
            <HandoffOfferModal session="s1" initialTitle="t" initialPrompt="p" onClose={onClose} />
          </ToastProvider>,
        );
      });
      await act(async () => {
        pushBox()!.click();
      });
      const gitSaid = " ! [rejected]        x -> x (non-fast-forward)";
      apiJSON.mockResolvedValueOnce({ error: { code: "handoff_push_failed", message: gitSaid, reason: "push_rejected" } });
      await act(async () => {
        sendButton()!.closest("form")!.requestSubmit();
      });
      expect(find(".handoff-push-error pre")?.textContent).toBe(gitSaid);
      expect(onClose).not.toHaveBeenCalled();
      // The verdict was fetched again; the push is not re-armed on the new snapshot.
      expect(pushBox()?.checked).toBe(false);
      expect(sendButton()?.disabled).toBe(true);
    });
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
