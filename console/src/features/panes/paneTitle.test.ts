import { describe, it, expect } from "vitest";
import { paneTitle, chatLabel } from "./paneTitle.ts";
import type { Pane, PaneContent } from "../../layout/types.ts";

const pane = (content: PaneContent): Pane => ({ id: "v1", session: null, content, wrap: null });
const chat = (conversationId: string | null, draftAssistantId: string | null = null): PaneContent => ({
  kind: "chat",
  conversationId,
  draftAssistantId,
});

describe("paneTitle: chat", () => {
  it("uses the conversation's own title so several chat tabs read apart", () => {
    expect(paneTitle(pane(chat("c1")), null, { chatTitle: "ブランチ確認" })).toBe("ブランチ確認");
    expect(paneTitle(pane(chat("c2")), null, { chatTitle: "翻訳" })).toBe("翻訳");
  });

  it("falls back to the kind label for a draft, an unknown title, or a blank one", () => {
    expect(paneTitle(pane(chat(null, "asst1")), null, {})).toBe("チャット");
    expect(paneTitle(pane(chat("c1")), null, {})).toBe("チャット");
    expect(paneTitle(pane(chat("c1")), null, { chatTitle: "   " })).toBe("チャット");
  });

  it("chatLabel is the same rule, callable without a pane", () => {
    expect(chatLabel("メモ整理")).toBe("メモ整理");
    expect(chatLabel(undefined)).toBe("チャット");
  });
});

describe("paneTitle: other kinds still resolve from the content", () => {
  it("names files by basename and browsers by port", () => {
    expect(paneTitle(pane({ kind: "file", filePath: "a/b/c.ts" }), null)).toBe("c.ts");
    expect(paneTitle(pane({ kind: "browser", port: 5173, path: "/" }), null)).toBe("127.0.0.1:5173");
  });
});
