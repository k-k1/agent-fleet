// A handoff has to be readable in a shared session too.
//
// propose_session_handoff leaves only a tool line and a boilerplate completion sentence in
// the transcript; the prompt handed to the next session lives in a separate store on the
// owner's side. The mirror injects it into the conversation as a card, so a shared view
// that fetches the transcript alone shows the recipient nothing but "a handoff apparently
// happened".
//
// It also holds the promise of docs/log/59 §3 — never render a control for a capability the
// viewer does not have: a recipient can neither edit, discard nor launch, so those buttons
// must not appear.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

// MarkdownView drags in all of remark/rehype; the handoff card is what matters here, so it
// is passed through.
vi.mock("../viewer/MarkdownView.tsx", () => ({
  MarkdownView: ({ source }: { source?: string }) => <div className="markdown">{source}</div>,
}));

const api = vi.fn();
vi.mock("../../core/api/client.ts", () => ({
  api: (path: string) => api(path),
  apiJSON: vi.fn(async () => ({})),
  errText: (e: unknown) => String((e as { message?: string })?.message ?? e),
  // Marks (docs/log/69) look up the reader's login id through the tenant store, which needs
  // this to initialise.
  getTenant: () => "",
}));

import { SharedSessionView } from "./SharedSessionView.tsx";
import { useHandoffStore, type HandoffOffer } from "./handoffStore.ts";
import { ToastProvider } from "../../ui/ToastProvider.tsx";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

const MESSAGES = {
  cursor: 10,
  firstLine: 0,
  hasMore: false,
  status: "idle",
  messages: [
    { role: "user", text: "続きを頼む", idx: 1, anchorId: "u1", ts: "2026-08-18T10:00:00Z" },
    { role: "assistant", idx: 2, ts: "2026-08-18T10:01:00Z", parts: [{ kind: "text", text: "引き継ぎを用意しました" }] },
  ],
};
const PROPOSALS = {
  proposals: [{ id: "hp_1", title: "残作業の続き", prompt: "未完了: 表示の実装。次はここから。", created_at: Date.parse("2026-08-18T10:02:00Z") }],
};

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  api.mockReset();
  api.mockImplementation(async (path: string) => {
    if (path.includes("/handoff-proposals")) return PROPOSALS;
    if (path.includes("/messages")) return MESSAGES;
    return { sessions: [] };
  });
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  delete (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT;
});

async function render() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<SharedSessionView sharedSessionId="catalog-1" />);
  });
  // The transcript and the proposals arrive on separate polls.
  await act(async () => {
    await Promise.resolve();
  });
  return host;
}

describe("SharedSessionView handoff proposals", () => {
  it("shows the proposal title and prompt", async () => {
    const el = await render();
    const card = el.querySelector(".mirror-handoff");
    expect(card).toBeTruthy();
    expect(card!.textContent).toContain("残作業の続き");
    expect(card!.querySelector(".mirror-handoff-prompt")?.textContent).toBe("未完了: 表示の実装。次はここから。");
  });

  it("shows no edit, discard or launch control (the recipient has no such capability)", async () => {
    const el = await render();
    expect(el.querySelector(".mirror-handoff-actions")).toBeNull();
    expect(el.querySelector(".mirror-handoff")!.querySelectorAll("button, textarea, input").length).toBe(0);
  });

  it("places the card after the conversation up to that point, not pinned to the end where later turns would be hidden", async () => {
    const el = await render();
    const nodes = [...el.querySelectorAll(".mirror-turn, .mirror-handoff")];
    expect(nodes.at(-1)?.classList.contains("mirror-handoff")).toBe(true);
    expect(nodes.length).toBeGreaterThan(1);
  });
});

// A handoff received from another member (docs/log/77). The "a handoff arrived"
// notification leads to this surface, so without a way to accept here whoever pressed it
// reaches a dead end — the only entry point used to be the rail heading icon.
describe("SharedSessionView received handoffs", () => {
  const OFFER: HandoffOffer = {
    id: "ho_1",
    sessionId: "catalog-1", // shared-catalog id = this surface's sharedSessionId
    sessionName: "sess-a",
    recipientUserKey: "b@example.com",
    ownerUserKey: "a@example.com",
    title: "残作業の続き",
    status: "pending",
    branch: "feature/x",
    repoRemote: "https://github.com/k-k1/agent-fleet.git",
    headSha: "0123456789ab",
    prompt: "未完了: 表示の実装。次はここから。",
  };
  const withOffer = (offer: HandoffOffer | null) =>
    api.mockImplementation(async (path: string) => {
      if (path.includes("session-handoff-offers/received")) return { offers: offer ? [offer] : [] };
      if (path.includes("session-handoff-offers")) return { offers: [] };
      if (path.includes("/handoff-proposals")) return { proposals: [] };
      if (path.includes("/messages")) return MESSAGES;
      return { sessions: [] };
    });

  // This surface fetches the offers itself, so the banner appears even in a detached tab
  // with no rail; reset the store between tests.
  afterEach(() => useHandoffStore.setState({ owned: [], received: [] }));

  async function renderWithToast() {
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    await act(async () => {
      // Accepting rides on the launch path, so the row requires a toast — rendered in the
      // same context as the inbox.
      root!.render(
        <ToastProvider>
          <SharedSessionView sharedSessionId="catalog-1" />
        </ToastProvider>,
      );
    });
    // Transcript, proposals and inbox all arrive on separate polls (the inbox does two in a
    // Promise.all).
    await act(async () => {
      for (let i = 0; i < 4; i++) await Promise.resolve();
    });
    return host;
  }

  it("shows the banner and a way to accept when an unprocessed offer is addressed to me", async () => {
    withOffer(OFFER);
    const el = await renderWithToast();
    const banner = el.querySelector(".shared-view-handoff");
    expect(banner).toBeTruthy();
    expect(banner!.textContent).toContain("a@example.com");
    // The banner button has to reach the inbox (accept / decline). Modal portals directly
    // under body.
    await act(async () => {
      banner!.querySelector("button")!.click();
    });
    const modal = document.body.querySelector(".ui-modal");
    expect(modal).toBeTruthy();
    expect(modal!.textContent).toContain("未完了: 表示の実装。次はここから。");
    // Accept and decline, two of them; the wording is locale-dependent, so assert structure.
    expect(modal!.querySelectorAll(".handoff-inbox-actions button").length).toBe(2);
  });

  it("shows nothing for a handoff addressed to another session", async () => {
    withOffer({ ...OFFER, sessionId: "catalog-2" });
    const el = await renderWithToast();
    expect(el.querySelector(".shared-view-handoff")).toBeNull();
  });
});

// A pending AskUserQuestion. While the modal is open the owner's Agent keeps the question
// out of the transcript (messages), stops the cursor before it and returns it separately as
// pendingQuestions. Without rendering that, the recipient sees nothing for exactly as long
// as the question is up.
describe("SharedSessionView pending question", () => {
  const PENDING = {
    ...MESSAGES,
    // The real shape: a pending question is absent from messages.
    pendingText: "方式が2つあります",
    pendingQuestions: [
      {
        header: "方式",
        question: "どちらにしますか",
        options: [
          { label: "A 案", description: "既存を拡張する" },
          { label: "B 案", description: "作り直す", preview: "+--+\n|  |\n+--+" },
        ],
      },
    ],
  };
  const withPending = () =>
    api.mockImplementation(async (path: string) => {
      if (path.includes("/handoff-proposals")) return { proposals: [] };
      if (path.includes("/messages")) return PENDING;
      return { sessions: [] };
    });

  it("shows the question and its options, preview included", async () => {
    withPending();
    const el = await render();
    const card = el.querySelector(".mt-question");
    expect(card).toBeTruthy();
    expect(card!.textContent).toContain("どちらにしますか");
    expect([...card!.querySelectorAll(".mq-opt")].map((o) => o.textContent)).toEqual([
      expect.stringContaining("A 案"),
      expect.stringContaining("B 案"),
    ]);
    expect(card!.querySelector(".mq-opt-preview")?.textContent).toContain("+--+");
    // The prose that ran just before the question comes along too; the card alone gives no
    // idea what it is about.
    expect(el.textContent).toContain("方式が2つあります");
  });

  it("offers no way to answer (the owner answers)", async () => {
    withPending();
    const el = await render();
    const card = el.querySelector(".mt-question")!;
    expect(card.querySelector(".mq-submit")).toBeNull();
    expect(card.querySelector(".mq-freetext")).toBeNull();
    expect([...card.querySelectorAll("button")].every((b) => (b as HTMLButtonElement).disabled)).toBe(true);
    // Unresolved, so there is no "answered" badge either.
    expect(card.querySelector(".mq-done")).toBeNull();
  });

  it("does not collapse to \"no history\" when the transcript is empty", async () => {
    api.mockImplementation(async (path: string) => {
      if (path.includes("/handoff-proposals")) return { proposals: [] };
      if (path.includes("/messages")) return { ...PENDING, messages: [] };
      return { sessions: [] };
    });
    const el = await render();
    expect(el.querySelector(".mt-question")).toBeTruthy();
    expect(el.querySelector(".mirror-empty")).toBeNull();
  });

  it("drops the card once the question is resolved", async () => {
    withPending();
    vi.useFakeTimers();
    try {
      const el = await render();
      expect(el.querySelector(".mt-question")).toBeTruthy();
      // No pendingQuestions in the next poll means the modal is gone. Keeping the previous
      // value would leave the card sitting in the recipient's view alone, after the owner
      // already answered.
      api.mockImplementation(async (path: string) => {
        if (path.includes("/handoff-proposals")) return { proposals: [] };
        if (path.includes("/messages")) return MESSAGES;
        return { sessions: [] };
      });
      await act(async () => {
        await vi.advanceTimersByTimeAsync(3100); // POLL_IDLE
      });
      expect(el.querySelector(".mt-question")).toBeNull();
    } finally {
      vi.useRealTimers();
    }
  });
});
