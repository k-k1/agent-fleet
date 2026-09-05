import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  addPlanComment,
  deliverPlanComments,
  formatPlanFeedback,
  getPlanComments,
  markPlanCommentsSent,
  planKey,
  removePlanComment,
  resetPlanCommentsForTest,
  unsentComments,
} from "./planComments.ts";
import { setLocale } from "../../lib/i18n/index.ts";

// The node environment has no localStorage, so install a minimal one: this store's whole point
// is the exchange that goes through localStorage, so that path has to be pinned too.
class MemStorage {
  private m = new Map<string, string>();
  getItem(k: string) {
    return this.m.has(k) ? this.m.get(k)! : null;
  }
  setItem(k: string, v: string) {
    this.m.set(k, v);
  }
  removeItem(k: string) {
    this.m.delete(k);
  }
  clear() {
    this.m.clear();
  }
}
beforeEach(() => {
  (globalThis as any).localStorage = new MemStorage();
  resetPlanCommentsForTest();
  setLocale("ja");
});

const PLAN = "# タイトル\n\n本文です。\n";

describe("planKey", () => {
  it("same session and same text give the same key; a revision gives a different one", () => {
    expect(planKey("s1", PLAN)).toBe(planKey("s1", PLAN));
    expect(planKey("s1", PLAN)).not.toBe(planKey("s1", PLAN + "追記\n"));
  });

  it("a different session gives a different key, so identical texts never mix", () => {
    expect(planKey("s1", PLAN)).not.toBe(planKey("s2", PLAN));
  });

  it("ignores only trailing whitespace and surrounding blank lines, so rendering jitter does not split a group", () => {
    expect(planKey("s1", PLAN)).toBe(planKey("s1", "\n# タイトル   \n\n本文です。   \n\n"));
  });
});

describe("collecting comments", () => {
  it("adds and removes, and ignores an empty comment", () => {
    const k = planKey("s1", PLAN);
    addPlanComment(k, { quote: "本文", nth: 0, body: "ここが違う" });
    addPlanComment(k, { quote: "本文", nth: 0, body: "   " }); // empty -> ignored
    expect(getPlanComments(k)).toHaveLength(1);
    removePlanComment(k, getPlanComments(k)[0].id);
    expect(getPlanComments(k)).toHaveLength(0);
  });

  it("folds sent comments instead of deleting them; only unsent ones are sent next", () => {
    const k = planKey("s1", PLAN);
    addPlanComment(k, { quote: "本文", nth: 0, body: "1つめ" });
    addPlanComment(k, { quote: "タイトル", nth: 0, body: "2つめ" });
    const first = getPlanComments(k)[0];
    markPlanCommentsSent(k, [first.id]);
    expect(getPlanComments(k)).toHaveLength(2);
    expect(unsentComments(getPlanComments(k)).map((c) => c.body)).toEqual(["2つめ"]);
  });

  it("survives a re-read because it lives in localStorage (for other tabs and reloads)", () => {
    const k = planKey("s1", PLAN);
    addPlanComment(k, { quote: "本文", nth: 0, body: "残ってほしい" });
    resetPlanCommentsForTest(); // re-read from the stored JSON
    expect(getPlanComments(k).map((c) => c.body)).toEqual(["残ってほしい"]);
  });
});

describe("formatPlanFeedback", () => {
  it("folds into one message with the quote as a blockquote and the remark below it", () => {
    const k = planKey("s1", PLAN);
    addPlanComment(k, { quote: "正しく", nth: 0, body: "正しく無い" });
    addPlanComment(k, { quote: "ユーザーに承認/拒否", nth: 1, body: "拒否だけで良い" });
    const text = formatPlanFeedback(getPlanComments(k));
    expect(text).toContain("プランへのコメント 2 件");
    expect(text).toContain("> 正しく\n\n正しく無い");
    expect(text).toContain("> ユーザーに承認/拒否\n\n拒否だけで良い");
    // The numbering matches the list order, so it lines up with the highlight numbers on the card.
    expect(text.indexOf("1.")).toBeLessThan(text.indexOf("2."));
  });

  it("prefixes every line of a multi-line quote with the blockquote marker", () => {
    expect(
      formatPlanFeedback([{ id: "a", quote: "1行目\n2行目", nth: 0, body: "指摘", ts: 0 }]),
    ).toContain("> 1行目\n> 2行目");
  });

  it("appends an overall note at the end, and sends only the note when there are no comments", () => {
    const one = [{ id: "a", quote: "q", nth: 0, body: "b", ts: 0 }];
    expect(formatPlanFeedback(one, "全体的に急いで")).toMatch(/全体的に急いで$/);
    expect(formatPlanFeedback([], "これだけ")).toBe("これだけ");
    expect(formatPlanFeedback([])).toBe("");
  });
});

// The delivery-route decision. This is the part that could be lifted out of MirrorView, and it is
// where the failure lived: undelivered comments were marked sent, which removed the send button
// and left the user unable to retype them.
describe("deliverPlanComments", () => {
  const k = () => planKey("s1", PLAN);
  const seed = (...bodies: string[]) => {
    for (const b of bodies) addPlanComment(k(), { quote: "本文", nth: 0, body: b });
    return unsentComments(getPlanComments(k())).map((c) => c.id);
  };
  const sentBodies = () =>
    getPlanComments(k())
      .filter((c) => c.sentAt)
      .map((c) => c.body);
  const okRespond = { ok: true, delivered: true };
  /** Pull out the text actually sent, which exists only on success (fail the test otherwise). */
  const feedbackOf = (r: Awaited<ReturnType<typeof deliverPlanComments>>) => {
    if (!r?.ok) throw new Error("not delivered: " + JSON.stringify(r));
    return r.feedback;
  };

  it("while approval is pending, sends via respond (reject) and folds only what was delivered", async () => {
    seed("1つめ", "2つめ");
    const respond = vi.fn().mockResolvedValue(okRespond);
    const say = vi.fn().mockResolvedValue(true);
    const res = await deliverPlanComments(k(), { pending: true, respond, say });

    expect(res).toMatchObject({ ok: true, via: "reject" });
    expect(say).not.toHaveBeenCalled(); // never send free text while the approval dialog is open
    // The text that was sent comes back verbatim; the caller uses it for the optimistic echo.
    expect(respond).toHaveBeenCalledWith(feedbackOf(res));
    expect(feedbackOf(res)).toContain("1つめ");
    expect(sentBodies()).toEqual(["1つめ", "2つめ"]);
  });

  it("sends as an ordinary utterance when approval is not pending", async () => {
    seed("指摘");
    const respond = vi.fn();
    const say = vi.fn().mockResolvedValue(true);
    const res = await deliverPlanComments(k(), { pending: false, respond, say });

    expect(res).toMatchObject({ ok: true, via: "prompt" });
    expect(respond).not.toHaveBeenCalled();
    expect(sentBodies()).toEqual(["指摘"]);
  });

  // The failure itself: comments were folded even though the utterance was refused (awaiting
// permission, session stopped, and so on).
  it("folds nothing when the utterance does not arrive, so it stays retypable", async () => {
    seed("指摘");
    const say = vi.fn().mockResolvedValue(false);
    const res = await deliverPlanComments(k(), { pending: false, respond: vi.fn(), say });

    // reason "say" = the utterance route failed. sendPrompt has already reported why, so the
    // caller must not toast on top of it; this one value decides whether a duplicate toast appears.
    expect(res).toEqual({ ok: false, reason: "say" });
    expect(sentBodies()).toEqual([]);
    expect(unsentComments(getPlanComments(k()))).toHaveLength(1);
  });

  it("falls back to an utterance on no_plan, and folds nothing if that utterance fails too", async () => {
    seed("指摘");
    const respond = vi.fn().mockResolvedValue({ ok: false, code: "no_plan" });
    const say = vi.fn().mockResolvedValue(false);
    const res = await deliverPlanComments(k(), { pending: true, respond, say });

    expect(say).toHaveBeenCalledTimes(1); // the fallback does run
    expect(res).toEqual({ ok: false, reason: "say" });
    expect(sentBodies()).toEqual([]);
  });

  it("folds when the no_plan fallback utterance is delivered (route: prompt)", async () => {
    seed("指摘");
    const respond = vi.fn().mockResolvedValue({ ok: false, code: "no_plan" });
    const res = await deliverPlanComments(k(), {
      pending: true,
      respond,
      say: vi.fn().mockResolvedValue(true),
    });

    expect(res).toMatchObject({ ok: true, via: "prompt" });
    expect(sentBodies()).toEqual(["指摘"]);
  });

  it("folds nothing when the rejection went through but the body did not land (undelivered)", async () => {
    seed("指摘");
    const res = await deliverPlanComments(k(), {
      pending: true,
      respond: vi.fn().mockResolvedValue({ ok: true, delivered: false, message: "コンポーザ復帰を確認できず" }),
      say: vi.fn().mockResolvedValue(true),
    });

    expect(res).toEqual({ ok: false, reason: "undelivered", message: "コンポーザ復帰を確認できず" });
    expect(sentBodies()).toEqual([]);
  });

  it("returns respond's failure reason verbatim for the caller to toast", async () => {
    seed("指摘");
    const res = await deliverPlanComments(k(), {
      pending: true,
      respond: vi.fn().mockResolvedValue({ ok: false, code: "not_running", message: "セッションが停止しています" }),
      say: vi.fn().mockResolvedValue(true),
    });

    expect(res).toEqual({ ok: false, reason: "respond", message: "セッションが停止しています" });
    expect(sentBodies()).toEqual([]);
  });

  it("sends nothing and returns null when there is nothing unsent", async () => {
    const ids = seed("送信済みにする");
    markPlanCommentsSent(k(), ids);
    const respond = vi.fn();
    const say = vi.fn();
    expect(await deliverPlanComments(k(), { pending: true, respond, say })).toBeNull();
    expect(respond).not.toHaveBeenCalled();
    expect(say).not.toHaveBeenCalled();
  });

  it("sends and folds only the unsent ones; already-sent comments stay out of the body", async () => {
    const ids = seed("古い指摘");
    markPlanCommentsSent(k(), ids);
    addPlanComment(k(), { quote: "本文", nth: 0, body: "新しい指摘" });
    const respond = vi.fn().mockResolvedValue(okRespond);
    const res = await deliverPlanComments(k(), { pending: true, respond, say: vi.fn() });

    expect(feedbackOf(res)).not.toContain("古い指摘");
    expect(feedbackOf(res)).toContain("新しい指摘");
    expect(sentBodies()).toEqual(["古い指摘", "新しい指摘"]);
  });
});
