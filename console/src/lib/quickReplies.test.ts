import { describe, expect, it } from "vitest";
import {
  isQuickReplyCandidate,
  recordQuickReply,
  rankQuickReplies,
  forgetQuickReply,
  hideQuickReply,
  unhideQuickReply,
  type QuickReplyMap,
} from "./quickReplies.ts";

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

  it("takes the strongest boost, not the sum (a multi-keyword entry can't stack)", () => {
    // 「コミット」＋「進め」を1文に詰め込んだ欲張りエントリ。合算加点だと +180 を得て、単語
    // ひとつの素直な候補（同じ使用回数）を構造的に永久に上回り、どの文脈でも先頭に貼り付いた。
    const m: QuickReplyMap = {
      greedy: { text: "OK,順に進めよう。都度コミットしてね", count: 3, at: 100 },
      "コミット": { text: "コミット", count: 3, at: 100 },
    };
    const out = rankQuickReplies(m, { draft: "", lastReply: "続けてコミットしておきます", locale: "ja" });
    // 両者とも +100 で並び、同点は短い方が先（欲張りが加点だけで勝つことはない）。
    expect(out.indexOf("コミット")).toBeLessThan(out.indexOf("OK,順に進めよう。都度コミットしてね"));
  });

  it("drops hidden keys, seeds included", () => {
    const m: QuickReplyMap = { "進めて": { text: "進めて", count: 5, at: 100 } };
    const out = rankQuickReplies(m, { draft: "", lastReply: "", locale: "ja", hidden: ["進めて", "ok"] });
    expect(out).not.toContain("進めて"); // 学習済みでも消える
    expect(out).not.toContain("OK"); // シードでも復活しない
    expect(out).toContain("続けて"); // 他のシードはそのまま
  });
});

describe("forget / hide / unhide", () => {
  it("forgets one learned entry (case-insensitive) and leaves the rest", () => {
    const m: QuickReplyMap = {
      ok: { text: "OK", count: 2, at: 10 },
      "進めて": { text: "進めて", count: 1, at: 20 },
    };
    const next = forgetQuickReply(m, "ok");
    expect(next.ok).toBeUndefined();
    expect(next["進めて"]).toBeTruthy();
    expect(forgetQuickReply(m, "未学習")).toBe(m); // 無変化なら同じ参照
  });

  it("hides by normalized key, ignores duplicates, caps the list", () => {
    expect(hideQuickReply([], "  OK  ")).toEqual(["ok"]);
    const once = hideQuickReply([], "OK");
    expect(hideQuickReply(once, "ok")).toBe(once); // 二重登録しない（同じ参照）
    let hidden: string[] = [];
    for (let i = 0; i < 70; i++) hidden = hideQuickReply(hidden, "reply" + i);
    expect(hidden).toHaveLength(60);
    expect(hidden).toContain("reply69");
    expect(hidden).not.toContain("reply0"); // 古いものから落ちる
  });

  it("unhides when the user sends the same text again", () => {
    const hidden = ["ok", "進めて"];
    expect(unhideQuickReply(hidden, "OK")).toEqual(["進めて"]);
    expect(unhideQuickReply(hidden, "未登録")).toBe(hidden); // 無変化なら同じ参照
  });
});
