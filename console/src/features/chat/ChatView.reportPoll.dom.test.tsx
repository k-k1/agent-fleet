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
// A turn that never finishes: the pane stays in its sending state.
const chatStream = vi.fn(() => new Promise(() => {}));
vi.mock("../../core/api/client.ts", async (orig) => ({
  ...((await orig()) as Record<string, unknown>),
  api: () => Promise.resolve({}),
  apiJSON: () => Promise.resolve({}),
  chatGet: (...a: unknown[]) => chatGet(...a),
  chatStream: () => chatStream(),
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
  chatStream.mockClear();
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

  it("drops a read in flight when this pane starts sending", async () => {
    let answerPoll: (v: unknown) => void = () => {};
    chatGet
      .mockResolvedValueOnce(conv("c1", 1000, "one"))
      .mockImplementationOnce(() => new Promise((r) => (answerPoll = r)));
    await render("c1");
    await tick(); // the report read hangs
    const ta = host!.querySelector("textarea") as HTMLTextAreaElement;
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(ta, "hello there");
      ta.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => {
      (host!.querySelector(".chat-composer-row button.primary, .chat-composer-row .chat-send") as HTMLElement).click();
    });
    expect(chatStream).toHaveBeenCalledTimes(1);
    expect(host!.textContent).toContain("hello there");
    await act(async () => {
      // Newer than the conversation on screen, but it predates the send.
      answerPoll(conv("c1", 5000, "late-report"));
      await Promise.resolve();
    });
    expect(host!.textContent).not.toContain("late-report");
    expect(host!.textContent).toContain("hello there");
  });
  // The reattach poller (1.4 s) and the report poller (5 s) read the same conversation; they
  // share one ReadGuard, so the order of their reads holds across the two (#4902).
  describe("with the reattach poller", () => {
    const advance = async (ms: number) => {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(ms);
      });
    };

    it("drops a report read that answers in_progress after a reattach read saw the turn end", async () => {
      let answerReport: ((v: unknown) => void) | null = null;
      chatGet.mockResolvedValue(conv("c1", 1000, "running", true));
      await render("c1");
      await advance(4300); // reattach reads at 1.4 s, 2.8 s and 4.2 s; the report poller has not ticked
      chatGet.mockImplementationOnce(() => new Promise((r) => (answerReport = r)));
      await advance(800); // the report read begins at 5 s and hangs
      expect(answerReport).not.toBeNull();
      chatGet.mockReset();
      chatGet.mockResolvedValue(conv("c1", 2000, "finished", false));
      await advance(1500); // the next reattach read sees the turn end and stops polling
      expect(host!.textContent).toContain("finished");
      const calls = chatGet.mock.calls.length;
      await act(async () => {
        // Older than what is on screen, but its in_progress differs and would be adopted.
        answerReport!(conv("c1", 1500, "stale-copy", true));
        await Promise.resolve();
      });
      expect(host!.textContent).toContain("finished");
      expect(host!.textContent).not.toContain("stale-copy");
      await advance(3000); // a restarted reattach poller would read again within 1.4 s; the next report tick is at 10 s
      expect(chatGet.mock.calls.length - calls).toBe(0);
    });

    it("drops a reattach read that answers after a newer report read landed", async () => {
      let answerReattach: (v: unknown) => void = () => {};
      chatGet.mockResolvedValueOnce(conv("c1", 1000, "running", true));
      chatGet.mockImplementationOnce(() => new Promise((r) => (answerReattach = r)));
      chatGet.mockResolvedValue(conv("c1", 3000, "newest", true));
      await render("c1");
      await advance(1500); // the reattach read begins at 1.4 s and hangs
      await advance(4000); // the report read at 5 s lands the newest copy
      expect(host!.textContent).toContain("newest");
      await act(async () => {
        answerReattach(conv("c1", 2000, "older-reattach", true));
        await Promise.resolve();
      });
      expect(host!.textContent).toContain("newest");
      expect(host!.textContent).not.toContain("older-reattach");
    });

    it("keeps polling after it drops a stale reattach answer, even one that says the turn ended", async () => {
      let answerReattach: (v: unknown) => void = () => {};
      chatGet.mockResolvedValueOnce(conv("c1", 1000, "running", true));
      chatGet.mockImplementationOnce(() => new Promise((r) => (answerReattach = r)));
      chatGet.mockResolvedValue(conv("c1", 3000, "newest", true));
      await render("c1");
      await advance(1500); // the reattach read begins at 1.4 s and hangs
      await advance(4000); // the report read at 5 s lands the newest copy
      expect(host!.textContent).toContain("newest");
      chatGet.mockReset();
      chatGet.mockResolvedValue(conv("c1", 4000, "finished", false));
      await act(async () => {
        // Older than what is on screen: dropped, so it must not stop the poller either.
        answerReattach(conv("c1", 2000, "older-done", false));
        await Promise.resolve();
      });
      expect(host!.textContent).toContain("newest");
      expect(host!.textContent).not.toContain("older-done");
      await advance(1500); // the next reattach read, before the report tick at 10 s
      expect(chatGet).toHaveBeenCalledTimes(1);
      expect(host!.textContent).toContain("finished");
    });
  });
});
