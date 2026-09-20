// Clicking a pasted image opens the shared ImageLightbox (features/viewer) instead of a new
// tab (already-sent turns) or nothing at all (the composer's pre-send chips) — the gap ADR
// 0080 decision 8 named and left for "its own entry if one is ever wanted."
//
// Two surfaces, two levels: the composer chip is exercised directly on ChatAttachStrip (no
// conversation, upload or paste plumbing needed to prove the click reaches `onOpen`); the
// already-sent thumbnail is exercised through the real ChatView so the click is shown to
// actually mount features/viewer's ImageLightbox, not just call a prop.
import { describe, it, expect, vi } from "vitest";
import { act } from "react";
import { createRoot } from "react-dom/client";
import type { Conversation } from "../../types/chat.ts";
import { FILE_PROMPT } from "../../lib/pastedImages.ts";

const click = async (el: Element | null | undefined) => {
  if (!el) throw new Error("not in the DOM");
  await act(async () => {
    el.dispatchEvent(new MouseEvent("click", { bubbles: true }));
  });
};

describe("ChatAttachStrip lightbox wiring", () => {
  it("clicking a chip's thumbnail opens it, clicking the remove button does not", async () => {
    const { ChatAttachStrip } = await import("./parts/ChatAttachStrip.tsx");
    const opened: string[] = [];
    const removed: number[] = [];
    const host = document.createElement("div");
    document.body.appendChild(host);
    const root = createRoot(host);
    await act(async () => {
      root.render(
        <ChatAttachStrip
          attachments={[{ path: "/tmp/a.png", name: "a.png", url: "blob:a" }]}
          pasting={false}
          onRemove={(i) => removed.push(i)}
          onOpen={(u) => opened.push(u)}
        />,
      );
    });

    await click(host.querySelector(".ca-thumb-btn"));
    expect(opened).toEqual(["blob:a"]);
    expect(removed).toEqual([]);

    await click(host.querySelector(".ca-del"));
    expect(removed).toEqual([0]);
    expect(opened).toEqual(["blob:a"]); // the delete click didn't also open it

    await act(async () => root.unmount());
    host.remove();
  });
});

const CONV: Conversation = {
  id: "c1",
  agent: "claude",
  active_agent: "claude",
  assistant_id: "a1",
  title: "テスト会話",
  model: "claude-opus-5",
  created_at: 1_700_000_000_000,
  updated_at: 1_700_000_100_000,
  messages: [
    {
      role: "user",
      content: "見て\n\n" + FILE_PROMPT + " /home/u/.cache/agent-fleet/pasted/abc/paste-1.png",
      ts: 1_700_000_001_000,
    },
  ],
};

vi.mock("../../core/api/client.ts", async (orig) => ({
  ...((await orig()) as Record<string, unknown>),
  api: () => Promise.resolve({}),
  apiJSON: () => Promise.resolve({}),
  chatGet: () => Promise.resolve(CONV),
  chatList: () => Promise.resolve([]),
  assistantGet: () => Promise.resolve({ id: "a1", name: "アシスタント", agent: "claude", icon: "beaker", voice: "" }),
  chatSuggestReplies: () => Promise.resolve({ suggestions: [] }),
  raw: () => Promise.resolve(new Response("")), // .blob() resolves to a (non-null) empty Blob
  rel: (p: string) => p,
  errText: (e: { message?: string }) => e?.message || "",
  isTransientErr: () => false,
}));
vi.mock("../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));
vi.mock("../../ui/ConfirmProvider.tsx", () => ({ useConfirm: () => () => Promise.resolve(true) }));

describe("already-sent pasted image lightbox", () => {
  it("clicking the thumbnail mounts ImageLightbox on a blob URL, closing unmounts it", async () => {
    const { ChatView } = await import("./ChatView.tsx");
    const { useWorkspaceStore } = await import("../../core/store/workspace.ts");
    useWorkspaceStore.setState({ state: "running" });

    const host = document.createElement("div");
    document.body.appendChild(host);
    const root = createRoot(host);
    await act(async () => {
      root.render(<ChatView conversationId="c1" paneId="p0" active />);
    });
    await act(async () => {
      await new Promise((r) => setTimeout(r, 120));
    });

    expect(document.querySelector(".mirror-lightbox")).toBeNull();
    await click(host.querySelector(".chat-img"));

    const lightboxImg = document.querySelector(".mirror-lightbox .imgview-img") as HTMLImageElement | null;
    expect(lightboxImg).not.toBeNull();
    expect(lightboxImg!.src.startsWith("blob:")).toBe(true);

    await act(async () => {
      document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    });
    expect(document.querySelector(".mirror-lightbox")).toBeNull();

    await act(async () => root.unmount());
    host.remove();
  });
});
