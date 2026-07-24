import { describe, expect, it } from "vitest";
import { isQuickReplyCandidate, recordQuickReply, rankQuickReplies, type QuickReplyMap } from "./quickReplies.ts";

describe("isQuickReplyCandidate", () => {
  it("accepts short single-line text", () => {
    expect(isQuickReplyCandidate("進めて", false)).toBe(true);
    expect(isQuickReplyCandidate("  ok  ", false)).toBe(true);
  });
  it("rejects empty, long (>20), multiline, slash, or attachment-bearing", () => {
    expect(isQuickReplyCandidate("", false)).toBe(false);
    expect(isQuickReplyCandidate("x".repeat(21), false)).toBe(false); // 20字超は質問文/プロンプト扱い
    expect(isQuickReplyCandidate("x".repeat(20), false)).toBe(true); // ちょうど20はOK
    expect(isQuickReplyCandidate("line1\nline2", false)).toBe(false);
    expect(isQuickReplyCandidate("/compact", false)).toBe(false);
    expect(isQuickReplyCandidate("ok", true)).toBe(false);
  });
});

describe("recordQuickReply", () => {
  it("counts case-insensitively and keeps the latest spelling", () => {
    let m: QuickReplyMap = {};
    m = recordQuickReply(m, "OK", 1);
    m = recordQuickReply(m, "ok", 2);
    expect(Object.keys(m)).toHaveLength(1);
    expect(m["ok"]).toEqual({ text: "ok", count: 2, at: 2 });
  });
  it("normalizes whitespace", () => {
    const m = recordQuickReply({}, "  commit   して ", 5);
    expect(m["commit して"].text).toBe("commit して");
  });
  it("prunes weakest entries past the cap", () => {
    let m: QuickReplyMap = {};
    for (let i = 0; i < 70; i++) m = recordQuickReply(m, "reply" + i, i);
    expect(Object.keys(m).length).toBeLessThanOrEqual(60);
    // the most recent survivor is kept; the oldest single-use ones are gone
    expect(m["reply69"]).toBeTruthy();
    expect(m["reply0"]).toBeFalsy();
  });
});

describe("rankQuickReplies", () => {
  const base = (): QuickReplyMap => ({
    "進めて": { text: "進めて", count: 5, at: 100 },
    commit: { text: "commit", count: 3, at: 90 },
    "やめて": { text: "やめて", count: 1, at: 80 },
  });

  it("ranks by frequency then recency", () => {
    const out = rankQuickReplies(base(), { draft: "", lastReply: "", locale: "ja", limit: 3 });
    expect(out[0]).toBe("進めて");
    expect(out[1]).toBe("commit");
  });

  it("hides already-stored entries longer than 20 chars", () => {
    const m: QuickReplyMap = {
      long: { text: "この環境、イメージにあるcliよりも古いのはなんでだろう", count: 9, at: 100 },
      ok: { text: "OK", count: 1, at: 50 },
    };
    const out = rankQuickReplies(m, { draft: "", lastReply: "", locale: "ja" });
    expect(out).not.toContain("この環境、イメージにあるcliよりも古いのはなんでだろう");
    expect(out).toContain("OK");
  });

  it("seeds defaults when empty so ok/進めて/commit appear", () => {
    const out = rankQuickReplies({}, { draft: "", lastReply: "", locale: "ja" });
    expect(out).toContain("OK");
    expect(out).toContain("進めて");
  });

  it("filters by draft prefix (autocomplete) and drops the exact draft", () => {
    const out = rankQuickReplies(base(), { draft: "co", lastReply: "", locale: "ja" });
    expect(out.length).toBeGreaterThan(0);
    expect(out.every((s) => s.toLowerCase().startsWith("co"))).toBe(true);
    expect(out).toContain("commit"); // learned "commit" (and the "commit して" seed) match "co"
    expect(rankQuickReplies(base(), { draft: "commit", lastReply: "", locale: "ja" })).not.toContain("commit");
  });

  it("boosts commit-topic replies from the last reply (B-1)", () => {
    const m: QuickReplyMap = {
      "進めて": { text: "進めて", count: 9, at: 100 },
      commit: { text: "commit", count: 1, at: 50 },
    };
    // Commit-topic but NOT a question — only the commit boost fires, lifting a rarely-used
    // "commit" above a more frequent unrelated reply.
    const out = rankQuickReplies(m, { draft: "", lastReply: "この変更をコミットしておきます", locale: "ja" });
    expect(out[0]).toBe("commit");
  });

  it("boosts short negative answers when the last reply is a question", () => {
    const m: QuickReplyMap = {
      "長めのプロンプト風": { text: "調べておいて", count: 9, at: 100 },
      "やめて": { text: "やめて", count: 1, at: 50 },
    };
    // "キャンセルしますか？" → a rarely-used "やめて" (NEGATE + question boost) beats the
    // frequent-but-unrelated reply.
    const out = rankQuickReplies(m, { draft: "", lastReply: "キャンセルしますか？", locale: "ja" });
    expect(out[0]).toBe("やめて");
  });
});
