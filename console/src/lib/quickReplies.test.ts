import { describe, expect, it } from "vitest";
import {
  isQuickReplyCandidate,
  recordQuickReply,
  rankQuickReplies,
  forgetQuickReply,
  hideQuickReply,
  unhideQuickReply,
  pinQuickReply,
  unpinQuickReply,
  isQuickReplyPinned,
  quickReplyKey,
  oneTimeQuickReplies,
  type QuickReplyMap,
} from "./quickReplies.ts";

describe("isQuickReplyCandidate", () => {
  it("accepts short single-line text", () => {
    expect(isQuickReplyCandidate("進めて", false)).toBe(true);
    expect(isQuickReplyCandidate("  ok  ", false)).toBe(true);
  });
  it("rejects empty, long (>20), multiline, slash, or attachment-bearing", () => {
    expect(isQuickReplyCandidate("", false)).toBe(false);
    expect(isQuickReplyCandidate("x".repeat(21), false)).toBe(false); // over 20 chars counts as a question or prompt
    expect(isQuickReplyCandidate("x".repeat(20), false)).toBe(true); // exactly 20 is fine
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
  it("folds full-width to half-width, so toggling the IME does not create a second entry", () => {
    let m: QuickReplyMap = {};
    m = recordQuickReply(m, "１", 1);
    m = recordQuickReply(m, "1", 2);
    expect(Object.keys(m)).toEqual(["1"]);
    expect(m["1"]).toEqual({ text: "1", count: 2, at: 2 }); // the spelling is the one most recently sent
    let n: QuickReplyMap = {};
    n = recordQuickReply(n, "ｂ", 1);
    n = recordQuickReply(n, "b", 2);
    expect(n["b"].count).toBe(2);
    // Halfwidth kana, composed voiced marks included, fold into the same candidate.
    let k: QuickReplyMap = {};
    k = recordQuickReply(k, "ｺﾐｯﾄして", 1);
    k = recordQuickReply(k, "コミットして", 2);
    expect(Object.keys(k)).toHaveLength(1);
    expect(k["コミットして"].count).toBe(2);
  });

  it("merges legacy entries that were learned under a full-width key", () => {
    // An entry accumulated before the key normalization changed, fullwidth in both key and
    // spelling. The next halfwidth send must fold it into one.
    const m: QuickReplyMap = { "ＯＫ": { text: "ＯＫ", count: 4, at: 10 } };
    const next = recordQuickReply(m, "OK", 20);
    expect(Object.keys(next)).toEqual(["ok"]);
    expect(next["ok"]).toEqual({ text: "OK", count: 5, at: 20 }); // the count is carried over
  });

  it("normalizes whitespace", () => {
    const m = recordQuickReply({}, "  commit   して ", 5);
    expect(m["commit して"].text).toBe("commit して");
  });
  it("prunes weakest entries past the cap", () => {
    let m: QuickReplyMap = {};
    for (let i = 0; i < 110; i++) m = recordQuickReply(m, "reply" + i, i);
    expect(Object.keys(m).length).toBeLessThanOrEqual(100);
    // the most recent survivor is kept; the oldest single-use ones are gone
    expect(m["reply109"]).toBeTruthy();
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

  it("seeds defaults when empty so the everyday replies appear", () => {
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
    // A greedy entry that packs both the commit and the proceed keyword into one sentence. With a
    // summed boost it collects +180 and structurally beats a plain single-word candidate of the
    // same use count forever, sticking to the front in every context.
    const m: QuickReplyMap = {
      greedy: { text: "OK,順に進めよう。都度コミットしてね", count: 3, at: 100 },
      "コミット": { text: "コミット", count: 3, at: 100 },
    };
    const out = rankQuickReplies(m, { draft: "", lastReply: "続けてコミットしておきます", locale: "ja" });
    // Both get +100 and the tie goes to the shorter one, so the greedy entry cannot win on boost alone.
    expect(out.indexOf("コミット")).toBeLessThan(out.indexOf("OK,順に進めよう。都度コミットしてね"));
  });

  it("puts pinned entries first, in pin order, whatever the ranking says", () => {
    const m: QuickReplyMap = { "進めて": { text: "進めて", count: 99, at: 999 } };
    const out = rankQuickReplies(m, {
      draft: "",
      lastReply: "続けてコミットしますか？",
      locale: "ja",
      pinned: ["親レポにマージしてプッシュ", "デプロイして"],
    });
    expect(out[0]).toBe("親レポにマージしてプッシュ");
    expect(out[1]).toBe("デプロイして");
    expect(out.filter((s) => s === "親レポにマージしてプッシュ")).toHaveLength(1); // not listed twice alongside the learned entry
  });

  it("keeps pinned entries on top of the ranked limit, not squeezed out of it", () => {
    // limit=2 caps the learned side only. Pins sit outside it, so the total reaches pins + limit.
    const m: QuickReplyMap = {
      a: { text: "aaa", count: 9, at: 100 },
      b: { text: "bbb", count: 8, at: 90 },
      c: { text: "ccc", count: 7, at: 80 },
    };
    const out = rankQuickReplies(m, { draft: "", lastReply: "", locale: "ja", pinned: ["固定"], limit: 2 });
    expect(out[0]).toBe("固定");
    expect(out).toHaveLength(3); // 1 pin + the top 2 learned; the pin does not eat into limit
  });

  it("shows a pinned entry that was hidden or never learned, and still honors the draft prefix", () => {
    const out = rankQuickReplies({}, { draft: "", lastReply: "", locale: "ja", hidden: ["固定"], pinned: ["固定"] });
    expect(out[0]).toBe("固定"); // a pin beats the hidden list
    const typing = rankQuickReplies({}, { draft: "こ", lastReply: "", locale: "ja", pinned: ["固定", "commit"] });
    expect(typing).not.toContain("commit"); // the prefix match while typing is the one rule pins obey
  });

  it("merges full-width variants into one chip and matches a full-width draft", () => {
    // A fullwidth entry still under its old key plus a halfwidth one: one chip, counts summed.
    const m: QuickReplyMap = {
      "ｃｏｍｍｉｔ": { text: "ｃｏｍｍｉｔ", count: 2, at: 50 },
      commit: { text: "commit", count: 1, at: 90 },
    };
    const out = rankQuickReplies(m, { draft: "", lastReply: "", locale: "ja" });
    expect(out.filter((s) => quickReplyKey(s) === "commit")).toEqual(["commit"]); // one entry, in the newer spelling
    // count is 2+1=3, comfortably above the seed at count 0.
    expect(out[0]).toBe("commit");
    // A fullwidth prefix matches too, so the list narrows without switching the IME.
    expect(rankQuickReplies(m, { draft: "ｃｏ", lastReply: "", locale: "ja" })).toContain("commit");
    expect(rankQuickReplies({ ...m, commit: m.commit }, { draft: "co", lastReply: "", locale: "ja" })).toContain(
      "commit",
    );
  });

  it("hides / pins / boosts across the width difference", () => {
    const m: QuickReplyMap = { "ｏｋ": { text: "ＯＫ", count: 5, at: 100 } };
    expect(rankQuickReplies(m, { draft: "", lastReply: "", locale: "ja", hidden: ["ok"] })).not.toContain("ＯＫ");
    expect(rankQuickReplies(m, { draft: "", lastReply: "", locale: "ja", pinned: ["OK"] })[0]).toBe("OK");
    // An answer written in fullwidth still matches the B-1 topic test.
    const c: QuickReplyMap = {
      "進めて": { text: "進めて", count: 9, at: 100 },
      commit: { text: "commit", count: 1, at: 50 },
    };
    expect(rankQuickReplies(c, { draft: "", lastReply: "ここで ｃｏｍｍｉｔ します", locale: "ja" })[0]).toBe("commit");
  });

  it("drops hidden keys, seeds included", () => {
    const m: QuickReplyMap = { "進めて": { text: "進めて", count: 5, at: 100 } };
    const out = rankQuickReplies(m, { draft: "", lastReply: "", locale: "ja", hidden: ["進めて", "ok"] });
    expect(out).not.toContain("進めて"); // gone even though it was learned
    expect(out).not.toContain("OK"); // a seed does not come back either
    expect(out).toContain("続けて"); // the other seeds are untouched
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
    expect(forgetQuickReply(m, "未学習")).toBe(m); // unchanged, so the same reference
  });

  it("hides by normalized key, ignores duplicates, caps the list", () => {
    expect(hideQuickReply([], "  OK  ")).toEqual(["ok"]);
    const once = hideQuickReply([], "OK");
    expect(hideQuickReply(once, "ok")).toBe(once); // no duplicate entry, so the same reference
    let hidden: string[] = [];
    for (let i = 0; i < 110; i++) hidden = hideQuickReply(hidden, "reply" + i);
    expect(hidden).toHaveLength(100);
    expect(hidden).toContain("reply109");
    expect(hidden).not.toContain("reply0"); // the oldest are dropped first
  });

  it("unhides when the user sends the same text again", () => {
    const hidden = ["ok", "進めて"];
    expect(unhideQuickReply(hidden, "OK")).toEqual(["進めて"]);
    expect(unhideQuickReply(hidden, "未登録")).toBe(hidden); // unchanged, so the same reference
  });

  it("treats full-width and half-width as the same entry", () => {
    // Even when the learned entry sits under an old fullwidth key, deleting in halfwidth removes it.
    const m: QuickReplyMap = { "ｏｋ": { text: "ＯＫ", count: 2, at: 10 } };
    expect(forgetQuickReply(m, "ok")).toEqual({});
    expect(hideQuickReply(["ok"], "ＯＫ")).toEqual(["ok"]); // no duplicate entry
    expect(unhideQuickReply(["ｏｋ"], "OK")).toEqual([]); // hiding under an old key can be lifted too
    expect(isQuickReplyPinned(["ＯＫ"], "ok")).toBe(true);
    expect(unpinQuickReply(["ＯＫ", "進めて"], "ok")).toEqual(["進めて"]);
    expect(pinQuickReply(["OK"], "ＯＫ")).toEqual(["OK"]); // the same pin is not added twice
  });
});

describe("oneTimeQuickReplies", () => {
  it("picks only the entries sent once, keeping the spelling", () => {
    const m: QuickReplyMap = {
      ok: { text: "OK", count: 3, at: 30 },
      "進めて": { text: "進めて", count: 1, at: 20 },
      "あとで見る": { text: "あとで見る", count: 1, at: 10 },
    };
    expect(oneTimeQuickReplies(m).sort()).toEqual(["あとで見る", "進めて"]);
    // After deleting, only the everyday replies remain; settings feeds this list to forgetQuickReply.
    const next = oneTimeQuickReplies(m).reduce((acc, text) => forgetQuickReply(acc, text), m);
    expect(Object.keys(next)).toEqual(["ok"]);
  });

  it("counts full-width and half-width as one entry, so once plus once is not a one-off", () => {
    const m: QuickReplyMap = {
      "ｏｋ": { text: "ＯＫ", count: 1, at: 10 }, // an old key stored before the key normalization
      ok: { text: "ok", count: 1, at: 20 },
      "進めて": { text: "進めて", count: 1, at: 30 },
    };
    expect(oneTimeQuickReplies(m)).toEqual(["進めて"]); // OK totals 2, so it stays
  });

  it("never touches pinned suggestions", () => {
    const m: QuickReplyMap = {
      "後で": { text: "後で", count: 1, at: 10 },
      ok: { text: "ok", count: 1, at: 20 },
    };
    expect(oneTimeQuickReplies(m, [])).toHaveLength(2);
    expect(oneTimeQuickReplies(m, ["後で"])).toEqual(["ok"]);
    expect(oneTimeQuickReplies(m, ["ＯＫ"])).toEqual(["後で"]); // a pin spelled fullwidth still matches
  });
});

describe("pin / unpin", () => {
  it("keeps the spelling and pin order, ignores case-only duplicates", () => {
    let p = pinQuickReply([], "  親レポにマージしてプッシュ ");
    expect(p).toEqual(["親レポにマージしてプッシュ"]); // stored as the normalized display spelling
    p = pinQuickReply(p, "OK");
    expect(p).toEqual(["親レポにマージしてプッシュ", "OK"]); // in pin order
    expect(pinQuickReply(p, "ok")).toBe(p); // case-only difference is the same pin, so the same reference
  });

  it("caps the list at 12, dropping the oldest pin", () => {
    let p: string[] = [];
    for (let i = 0; i < 15; i++) p = pinQuickReply(p, "pin" + i);
    expect(p).toHaveLength(12);
    expect(p[0]).toBe("pin3");
    expect(p).toContain("pin14");
  });

  it("unpins case-insensitively and reports pinned state", () => {
    const p = ["OK", "進めて"];
    expect(isQuickReplyPinned(p, " ok ")).toBe(true);
    expect(isQuickReplyPinned(p, "やめて")).toBe(false);
    expect(isQuickReplyPinned(undefined, "OK")).toBe(false);
    expect(unpinQuickReply(p, "ok")).toEqual(["進めて"]);
    expect(unpinQuickReply(p, "未登録")).toBe(p); // unchanged, so the same reference
  });
});
