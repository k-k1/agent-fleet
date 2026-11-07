// A session handoff opens a chat seeded with a prompt whose turn reads another session's output
// and may lead to create_session. The seed must only prefill the composer: no turn is sent until
// the member acts, so consent is bound to a UI action and not to prompt text.
import { describe, it, expect, vi } from "vitest";
import { act } from "react";
import { createRoot } from "react-dom/client";
import type { Conversation } from "../../types/chat.ts";

const CONV: Conversation = {
  id: "c9",
  agent: "claude",
  active_agent: "claude",
  assistant_id: "a1",
  title: "handoff",
  model: "claude-opus-5",
  created_at: 1_700_000_000_000,
  updated_at: 1_700_000_000_000,
  messages: [],
} as Conversation;

const streams: unknown[][] = [];
vi.mock("./api.ts", async (orig) => ({
  ...((await orig()) as Record<string, unknown>),
  chatGet: () => Promise.resolve(CONV),
  chatStream: (...a: unknown[]) => {
    streams.push(a);
    return Promise.resolve();
  },
  assistantGet: () => Promise.resolve({ id: "a1", name: "A", agent: "claude", icon: "beaker", voice: "" }),
}));
vi.mock("../../core/api/client.ts", async (orig) => ({
  ...((await orig()) as Record<string, unknown>),
  api: () => Promise.resolve({}),
  apiJSON: () => Promise.resolve({}),
  chatList: () => Promise.resolve([]),
  chatSuggestReplies: () => Promise.resolve({ suggestions: [] }),
  raw: () => Promise.resolve(new Response("")),
  rel: (p: string) => p,
  errText: (e: { message?: string }) => e?.message || "",
  isTransientErr: () => false,
}));
vi.mock("../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));
vi.mock("../../ui/ConfirmProvider.tsx", () => ({ useConfirm: () => () => Promise.resolve(true) }));

import { ChatView } from "./ChatView.tsx";
import { setChatSeed } from "../../lib/chatSeed.ts";
import { useWorkspaceStore } from "../../core/store/workspace.ts";

describe("ChatView handoff seed", () => {
  it("prefills the composer and sends nothing until the member acts", async () => {
    useWorkspaceStore.setState({ state: "running" });
    const SEED = "HANDOFF-SEED-PROMPT";
    setChatSeed("c9", SEED);
    const host = document.createElement("div");
    document.body.appendChild(host);
    const root = createRoot(host);
    await act(async () => {
      root.render(<ChatView conversationId="c9" paneId="p0" active />);
    });
    await act(async () => {
      await new Promise((r) => setTimeout(r, 300));
    });
    const ta = host.querySelector("textarea") as HTMLTextAreaElement | null;
    expect(ta?.value).toBe(SEED); // positive control: the seed did arrive
    expect(streams.length).toBe(0);
    await act(async () => root.unmount());
    host.remove();
  });
});
