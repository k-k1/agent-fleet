import { describe, it, expect } from "vitest";
import type { Layout, Pane, PaneContent } from "./types.ts";
import { blankPane } from "./types.ts";
import { chatPanes, activeChatId } from "./badges.ts";

const chat = (conversationId: string | null, draftAssistantId: string | null = null): PaneContent => ({
  kind: "chat",
  conversationId,
  draftAssistantId,
});
const pane = (id: string, content: PaneContent): Pane => ({ ...blankPane(id), content });

// One column per pane, in reading order — enough for the derivations under test.
function layoutOf(panes: Pane[], activeId: string): Layout {
  return {
    version: 3,
    mode: "split",
    cols: panes.map((p, i) => ({ id: "c" + i, rowRatio: 0.5, cells: [{ id: "g" + i, selectedViewId: p.id, views: [p] }] })),
    colRatios: panes.map(() => 1 / panes.length),
    activeCellId: "g" + panes.findIndex((p) => p.id === activeId),
  };
}

describe("chatPanes", () => {
  it("maps each conversation to the panes showing it, in visual order", () => {
    const l = layoutOf([pane("a", chat("c1")), pane("b", blankPane("b").content), pane("c", chat("c1"))], "a");
    expect(chatPanes(l).get("c1")?.map((r) => r.ordinal)).toEqual([1, 3]);
    expect(chatPanes(l).get("c2")).toBeUndefined();
  });

  it("ignores drafts — they have no conversation yet", () => {
    const l = layoutOf([pane("a", chat(null, "asst1"))], "a");
    expect(chatPanes(l).size).toBe(0);
  });
});

describe("activeChatId", () => {
  it("reports the conversation the FOCUSED pane shows, not merely an open one", () => {
    const l = layoutOf([pane("a", chat("c1")), pane("b", chat("c2"))], "b");
    expect(activeChatId(l)).toBe("c2");
    expect(activeChatId({ ...l, activeCellId: "g0" })).toBe("c1");
  });

  it("is null when the active pane is a draft or isn't a chat at all", () => {
    expect(activeChatId(layoutOf([pane("a", chat(null, "asst1"))], "a"))).toBeNull();
    expect(activeChatId(layoutOf([blankPane("a")], "a"))).toBeNull();
  });

  it("is null when the active id points at no pane, or there is no layout", () => {
    expect(activeChatId(layoutOf([pane("a", chat("c1"))], "gone"))).toBeNull();
    expect(activeChatId(null)).toBeNull();
  });
});
