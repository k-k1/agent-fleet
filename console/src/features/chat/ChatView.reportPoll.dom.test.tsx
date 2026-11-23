// The report poller of an af_write conversation (#4786): its reads are ticketed, so an answer
// for the conversation just left, or one older than a read that already landed, is dropped.
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { Conversation } from "../../types/chat.ts";

const conv = (id: string, updated: number, text: string, inProgress = false): Conversation => ({
  id,
  agent: "claude",
  active_agent: "claude",
  assistant_id: "a1",
  title: "t-" + id,
  model: "claude-opus-5",
  created_at: 1_700_000_000_000,
  updated_at: updated,
  tools: "af_write",
  in_progress: inProgress,
  messages: [{ role: "report", content: text, ts: updated, session: "sxxxxxx" }],
});

const chatGet = vi.fn();
vi.mock("../../core/api/client.ts", async (orig) => ({
  ...((await orig()) as Record<string, unknown>),
  api: () => Promise.resolve({}),
  apiJSON: () => Promise.resolve({}),
  chatGet: (...a: unknown[]) => chatGet(...a),
  chatList: () => Promise.resolve([]),
  assistantGet: () => Promise.resolve({ id: "a1", name: "x", agent: "claude", icon: "beaker", voice: "" }),
  chatSuggestReplies: () => Promise.resolve({ suggestions: [] }),
  raw: () => Promise.resolve(new Response("")),
  rel: (p: string) => p,
  errText: (e: { message?: string }) => e?.message || "",
  isTransientErr: () => false,
}));
vi.mock("../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));
vi.mock("../../ui/ConfirmProvider.tsx", () => ({ useConfirm: () => () => Promise.resolve(true) }));

import { ChatView } from "./ChatView.tsx";
import { useWorkspaceStore } from "../../core/store/workspace.ts";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function render(id: string) {
  if (!root) {
    host = document.createElement("div");
    document.body.appendChild(host);
    root = createRoot(host);
  }
  await act(async () => {
    root!.render(<ChatView conversationId={id} paneId="p0" active />);
  });
  await act(async () => {
    await Promise.resolve();
  });
}

const tick = async () => {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(5000);
  });
};

beforeEach(() => {
  chatGet.mockReset();
  useWorkspaceStore.setState({ state: "running" });
  vi.useFakeTimers({ shouldAdvanceTime: true });
});
afterEach(() => {
  vi.useRealTimers();
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

describe("ChatView report poller", () => {
  it("drops a slow read that answers after a newer one landed", async () => {
    let answerSlow: (v: unknown) => void = () => {};
    chatGet
      .mockResolvedValueOnce(conv("c1", 1000, "first"))
      .mockImplementationOnce(() => new Promise((r) => (answerSlow = r)))
      .mockResolvedValue(conv("c1", 3000, "newest"));
    await render("c1");
    expect(host!.textContent).toContain("first");
    await tick(); // this read hangs
    await tick(); // this one lands
    expect(host!.textContent).toContain("newest");
    await act(async () => {
      // Older than what is on screen, but it would still flip in_progress and be adopted.
      answerSlow(conv("c1", 2000, "stale", true));
      await Promise.resolve();
    });
    expect(host!.textContent).toContain("newest");
    expect(host!.textContent).not.toContain("stale");
  });

  it("drops a read for the conversation just left", async () => {
    let answerOld: (v: unknown) => void = () => {};
    chatGet
      .mockResolvedValueOnce(conv("c1", 1000, "one"))
      .mockImplementationOnce(() => new Promise((r) => (answerOld = r)))
      .mockResolvedValue(conv("c2", 1000, "two"));
    await render("c1");
    await tick(); // c1's poll read hangs
    await render("c2");
    expect(host!.textContent).toContain("two");
    await act(async () => {
      answerOld(conv("c1", 5000, "late-one"));
      await Promise.resolve();
    });
    expect(host!.textContent).toContain("two");
    expect(host!.textContent).not.toContain("late-one");
  });
});
