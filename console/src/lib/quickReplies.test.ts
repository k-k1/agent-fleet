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
  it("folds full-width to half-width (IME の ON/OFF で別エントリにしない)", () => {
    let m: QuickReplyMap = {};
    m = recordQuickReply(m, "１", 1);
    m = recordQuickReply(m, "1", 2);
    expect(Object.keys(m)).toEqual(["1"]);
    expect(m["1"]).toEqual({ text: "1", count: 2, at: 2 }); // 綴りは最後に送ったもの
    let n: QuickReplyMap = {};
    n = recordQuickReply(n, "ｂ", 1);
    n = recordQuickReply(n, "b", 2);
    expect(n["b"].count).toBe(2);
    // 半角カナ（濁点の合成込み）も同じ候補として畳む
    let k: QuickReplyMap = {};
    k = recordQuickReply(k, "ｺﾐｯﾄして", 1);
    k = recordQuickReply(k, "コミットして", 2);
    expect(Object.keys(k)).toHaveLength(1);
    expect(k["コミットして"].count).toBe(2);
  });

  it("merges legacy entries that were learned under a full-width key", () => {
    // キー正規化を変える前に貯まったエントリ（キーも綴りも全角）。次に半角で送ったら1件に畳む。
    const m: QuickReplyMap = { "ＯＫ": { text: "ＯＫ", count: 4, at: 10 } };
    const next = recordQuickReply(m, "OK", 20);
    expect(Object.keys(next)).toEqual(["ok"]);
    expect(next["ok"]).toEqual({ text: "OK", count: 5, at: 20 }); // 回数を引き継ぐ
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
    expect(out.filter((s) => s === "親レポにマージしてプッシュ")).toHaveLength(1); // 学習側と二重に出さない
  });

  it("keeps pinned entries on top of the ranked limit, not squeezed out of it", () => {
    // limit=2 は学習側だけの上限。ピンは別枠なので、合計はピン件数 + limit まで出る。
    const m: QuickReplyMap = {
      a: { text: "aaa", count: 9, at: 100 },
      b: { text: "bbb", count: 8, at: 90 },
      c: { text: "ccc", count: 7, at: 80 },
    };
    const out = rankQuickReplies(m, { draft: "", lastReply: "", locale: "ja", pinned: ["固定"], limit: 2 });
    expect(out[0]).toBe("固定");
    expect(out).toHaveLength(3); // ピン1件 + 学習上位2件（limit を圧迫しない）
  });

  it("shows a pinned entry that was hidden or never learned, and still honors the draft prefix", () => {
    const out = rankQuickReplies({}, { draft: "", lastReply: "", locale: "ja", hidden: ["固定"], pinned: ["固定"] });
    expect(out[0]).toBe("固定"); // ピンは隠しより強い
    const typing = rankQuickReplies({}, { draft: "こ", lastReply: "", locale: "ja", pinned: ["固定", "commit"] });
    expect(typing).not.toContain("commit"); // 入力中の前方一致だけはピンにも効く
  });

  it("merges full-width variants into one chip and matches a full-width draft", () => {
    // 旧キーのまま残っている全角エントリと半角エントリ。表示は1件（回数は合算）。
    const m: QuickReplyMap = {
      "ｃｏｍｍｉｔ": { text: "ｃｏｍｍｉｔ", count: 2, at: 50 },
      commit: { text: "commit", count: 1, at: 90 },
    };
    const out = rankQuickReplies(m, { draft: "", lastReply: "", locale: "ja" });
    expect(out.filter((s) => quickReplyKey(s) === "commit")).toEqual(["commit"]); // 新しい綴りで1件だけ
    // count は 2+1=3。シード「commit して」(count 0) より当然上。
    expect(out[0]).toBe("commit");
    // 全角で打ちかけても前方一致する（IME を切り替えずに絞り込める）。
    expect(rankQuickReplies(m, { draft: "ｃｏ", lastReply: "", locale: "ja" })).toContain("commit");
    expect(rankQuickReplies({ ...m, commit: m.commit }, { draft: "co", lastReply: "", locale: "ja" })).toContain(
      "commit",
    );
  });

  it("hides / pins / boosts across the width difference", () => {
    const m: QuickReplyMap = { "ｏｋ": { text: "ＯＫ", count: 5, at: 100 } };
    expect(rankQuickReplies(m, { draft: "", lastReply: "", locale: "ja", hidden: ["ok"] })).not.toContain("ＯＫ");
    expect(rankQuickReplies(m, { draft: "", lastReply: "", locale: "ja", pinned: ["OK"] })[0]).toBe("OK");
    // 全角で書かれた回答でも B-1 の話題判定に当たる。
    const c: QuickReplyMap = {
      "進めて": { text: "進めて", count: 9, at: 100 },
      commit: { text: "commit", count: 1, at: 50 },
    };
    expect(rankQuickReplies(c, { draft: "", lastReply: "ここで ｃｏｍｍｉｔ します", locale: "ja" })[0]).toBe("commit");
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
    for (let i = 0; i < 110; i++) hidden = hideQuickReply(hidden, "reply" + i);
    expect(hidden).toHaveLength(100);
    expect(hidden).toContain("reply109");
    expect(hidden).not.toContain("reply0"); // 古いものから落ちる
  });

  it("unhides when the user sends the same text again", () => {
    const hidden = ["ok", "進めて"];
    expect(unhideQuickReply(hidden, "OK")).toEqual(["進めて"]);
    expect(unhideQuickReply(hidden, "未登録")).toBe(hidden); // 無変化なら同じ参照
  });

  it("treats full-width and half-width as the same entry", () => {
    // 学習側は旧キー（全角）でも、半角で消せば消える。
    const m: QuickReplyMap = { "ｏｋ": { text: "ＯＫ", count: 2, at: 10 } };
    expect(forgetQuickReply(m, "ok")).toEqual({});
    expect(hideQuickReply(["ok"], "ＯＫ")).toEqual(["ok"]); // 二重登録しない
    expect(unhideQuickReply(["ｏｋ"], "OK")).toEqual([]); // 旧キーの隠しも解除できる
    expect(isQuickReplyPinned(["ＯＫ"], "ok")).toBe(true);
    expect(unpinQuickReply(["ＯＫ", "進めて"], "ok")).toEqual(["進めて"]);
    expect(pinQuickReply(["OK"], "ＯＫ")).toEqual(["OK"]); // 同じピンは増やさない
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
    // 消した結果は常用だけが残る（設定画面はこのリストを forgetQuickReply に流す）。
    const next = oneTimeQuickReplies(m).reduce((acc, text) => forgetQuickReply(acc, text), m);
    expect(Object.keys(next)).toEqual(["ok"]);
  });

  it("counts full-width and half-width as one entry (1回 + 1回 = 一度きりではない)", () => {
    const m: QuickReplyMap = {
      "ｏｋ": { text: "ＯＫ", count: 1, at: 10 }, // キー正規化前に積まれた旧キー
      ok: { text: "ok", count: 1, at: 20 },
      "進めて": { text: "進めて", count: 1, at: 30 },
    };
    expect(oneTimeQuickReplies(m)).toEqual(["進めて"]); // OK は合算2回なので残る
  });

  it("never touches pinned suggestions", () => {
    const m: QuickReplyMap = {
      "後で": { text: "後で", count: 1, at: 10 },
      ok: { text: "ok", count: 1, at: 20 },
    };
    expect(oneTimeQuickReplies(m, [])).toHaveLength(2);
    expect(oneTimeQuickReplies(m, ["後で"])).toEqual(["ok"]);
    expect(oneTimeQuickReplies(m, ["ＯＫ"])).toEqual(["後で"]); // ピンの綴りが全角でも当たる
  });
});

describe("pin / unpin", () => {
  it("keeps the spelling and pin order, ignores case-only duplicates", () => {
    let p = pinQuickReply([], "  親レポにマージしてプッシュ ");
    expect(p).toEqual(["親レポにマージしてプッシュ"]); // 正規化した表示綴りで持つ
    p = pinQuickReply(p, "OK");
    expect(p).toEqual(["親レポにマージしてプッシュ", "OK"]); // ピンした順
    expect(pinQuickReply(p, "ok")).toBe(p); // 大小違いは同じピン（無変化なら同じ参照）
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
    expect(unpinQuickReply(p, "未登録")).toBe(p); // 無変化なら同じ参照
  });
});
