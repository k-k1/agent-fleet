import { describe, it, expect } from "vitest";
import { mergeChatTitles, useChatStore } from "./store.ts";
import type { ConversationMeta } from "../../types/chat.ts";

const meta = (id: string, title: string): ConversationMeta => ({
  id,
  agent: "claude",
  title,
  created_at: 0,
  updated_at: 0,
  message_count: 1,
});

describe("mergeChatTitles", () => {
  it("prefers the rail's list, which a rename refreshes immediately", () => {
    const m = mergeChatTitles([meta("c1", "改名後")], { c1: "改名前" });
    expect(m.get("c1")).toBe("改名後");
  });

  it("falls back to a published title for a conversation the list hasn't got yet", () => {
    const m = mergeChatTitles([meta("c1", "既存")], { c2: "作りたて" });
    expect(m.get("c2")).toBe("作りたて");
  });

  it("treats a never-loaded list and an empty title as no title at all", () => {
    expect(mergeChatTitles(null, {}).size).toBe(0);
    expect(mergeChatTitles([meta("c1", "")], { c1: "" }).has("c1")).toBe(false);
  });
});

describe("setConvTitle", () => {
  it("keeps the same object when nothing changed, so subscribers don't re-render", () => {
    useChatStore.setState({ titles: {} });
    useChatStore.getState().setConvTitle("c1", "題");
    const first = useChatStore.getState().titles;
    useChatStore.getState().setConvTitle("c1", "題");
    expect(useChatStore.getState().titles).toBe(first);
    useChatStore.getState().setConvTitle("c1", "別の題");
    expect(useChatStore.getState().titles.c1).toBe("別の題");
  });
});
