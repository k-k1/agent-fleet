// A session handoff opens a chat seeded with a prompt whose turn reads another session's output
// and may lead to create_session. This drives the real handoff action (useSessionActions) into
// the real ChatView: the prompt must only prefill the composer, and the turn is streamed once the
// member presses Send, so consent is bound to a UI action and not to prompt text.
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
  chatCreate: () => Promise.resolve({ id: "c9" }),
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
const toasts: string[] = [];
vi.mock("../../ui/ToastProvider.tsx", () => ({ useToast: () => (m: string) => void toasts.push(m) }));
vi.mock("../../lib/workingSetsStore.ts", async (orig) => ({
  ...((await orig()) as Record<string, unknown>),
  autoAddToActiveWorkingSet: () => {},
}));
vi.mock("../../ui/ConfirmProvider.tsx", () => ({ useConfirm: () => () => Promise.resolve(true) }));

import { ChatView } from "./ChatView.tsx";
import { useSessionActions } from "../sessions/useSessionActions.tsx";
import { useWorkspaceStore } from "../../core/store/workspace.ts";

function StartHandoff() {
  const { handoff } = useSessionActions();
  return (
    <button data-testid="start" onClick={() => void handoff("src-session", "claude")}>
      start
    </button>
  );
}

describe("handoff seed", () => {
  it("prefills the composer, sends nothing, and streams only after Send", async () => {
    useWorkspaceStore.setState({ state: "running" });
    const host = document.createElement("div");
    document.body.appendChild(host);
    const root = createRoot(host);
    await act(async () => {
      root.render(<StartHandoff />);
    });
    await act(async () => {
      (host.querySelector("[data-testid=start]") as HTMLButtonElement).click();
      await new Promise((r) => setTimeout(r, 50));
    });
    // The handoff action has run; now the pane it opened shows the conversation.
    await act(async () => {
      root.render(<ChatView conversationId="c9" paneId="p0" active />);
    });
    await act(async () => {
      await new Promise((r) => setTimeout(r, 300));
    });
    const ta = host.querySelector("textarea") as HTMLTextAreaElement | null;
    expect(ta?.value).toContain("src-session"); // positive control: the handoff prompt arrived
    expect(streams.length).toBe(0);
    expect(toasts.length).toBe(1);

    await act(async () => {
      (host.querySelector("button.chat-send") as HTMLButtonElement).click();
      await new Promise((r) => setTimeout(r, 50));
    });
    expect(streams.length).toBe(1);
    expect(String(streams[0][1])).toContain("src-session");
    await act(async () => root.unmount());
    host.remove();
  });
});
