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

// node 環境には localStorage が無いので最小の実体を置く（このストアの本体は
// localStorage 越しのやり取りなので、そこも含めて固定したい）。
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
  it("同じセッション・同じ本文なら同じ鍵、改訂すると別の鍵", () => {
    expect(planKey("s1", PLAN)).toBe(planKey("s1", PLAN));
    expect(planKey("s1", PLAN)).not.toBe(planKey("s1", PLAN + "追記\n"));
  });

  it("セッションが違えば別の鍵（同じ本文でも混ざらない）", () => {
    expect(planKey("s1", PLAN)).not.toBe(planKey("s2", PLAN));
  });

  it("行末空白と前後の空行だけは無視する（同じプランの表示揺れで束が割れない）", () => {
    expect(planKey("s1", PLAN)).toBe(planKey("s1", "\n# タイトル   \n\n本文です。   \n\n"));
  });
});

describe("コメントの蓄積", () => {
  it("追加・削除でき、空のコメントは無視される", () => {
    const k = planKey("s1", PLAN);
    addPlanComment(k, { quote: "本文", nth: 0, body: "ここが違う" });
    addPlanComment(k, { quote: "本文", nth: 0, body: "   " }); // 空 → 無視
    expect(getPlanComments(k)).toHaveLength(1);
    removePlanComment(k, getPlanComments(k)[0].id);
    expect(getPlanComments(k)).toHaveLength(0);
  });

  it("送信済みは消さずに畳む — 未送信だけが次の送信対象", () => {
    const k = planKey("s1", PLAN);
    addPlanComment(k, { quote: "本文", nth: 0, body: "1つめ" });
    addPlanComment(k, { quote: "タイトル", nth: 0, body: "2つめ" });
    const first = getPlanComments(k)[0];
    markPlanCommentsSent(k, [first.id]);
    expect(getPlanComments(k)).toHaveLength(2);
    expect(unsentComments(getPlanComments(k)).map((c) => c.body)).toEqual(["2つめ"]);
  });

  it("localStorage に載るので、読み直しても残る（別タブ・リロード対策）", () => {
    const k = planKey("s1", PLAN);
    addPlanComment(k, { quote: "本文", nth: 0, body: "残ってほしい" });
    resetPlanCommentsForTest(); // 保存済み JSON から読み直す
    expect(getPlanComments(k).map((c) => c.body)).toEqual(["残ってほしい"]);
  });
});

describe("formatPlanFeedback", () => {
  it("引用を blockquote、指摘をその下に置いた1本の文へ畳む", () => {
    const k = planKey("s1", PLAN);
    addPlanComment(k, { quote: "正しく", nth: 0, body: "正しく無い" });
    addPlanComment(k, { quote: "ユーザーに承認/拒否", nth: 1, body: "拒否だけで良い" });
    const text = formatPlanFeedback(getPlanComments(k));
    expect(text).toContain("プランへのコメント 2 件");
    expect(text).toContain("> 正しく\n\n正しく無い");
    expect(text).toContain("> ユーザーに承認/拒否\n\n拒否だけで良い");
    // 番号は一覧の並びと一致する（カードのハイライト番号と突き合わせるため）。
    expect(text.indexOf("1.")).toBeLessThan(text.indexOf("2."));
  });

  it("複数行の引用は行ごとに blockquote 記号を付ける", () => {
    expect(
      formatPlanFeedback([{ id: "a", quote: "1行目\n2行目", nth: 0, body: "指摘", ts: 0 }]),
    ).toContain("> 1行目\n> 2行目");
  });

  it("全体への追記があれば末尾に足す。コメントが無ければ追記だけ", () => {
    const one = [{ id: "a", quote: "q", nth: 0, body: "b", ts: 0 }];
    expect(formatPlanFeedback(one, "全体的に急いで")).toMatch(/全体的に急いで$/);
    expect(formatPlanFeedback([], "これだけ")).toBe("これだけ");
    expect(formatPlanFeedback([])).toBe("");
  });
});

// 送信経路の判断。ここが唯一 MirrorView の外に出せた部分で、実障害（届いていない
// コメントが「送信済み」になり、送信ボタンごと消えて打ち直せない）はこの判断にあった。
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
  /** 送信できたときだけ在る「実際に送った本文」を取り出す（届いていなければ失敗させる）。 */
  const feedbackOf = (r: Awaited<ReturnType<typeof deliverPlanComments>>) => {
    if (!r?.ok) throw new Error("届いていない: " + JSON.stringify(r));
    return r.feedback;
  };

  it("承認待ちなら respond（却下）で送り、届いたぶんだけ畳む", async () => {
    seed("1つめ", "2つめ");
    const respond = vi.fn().mockResolvedValue(okRespond);
    const say = vi.fn().mockResolvedValue(true);
    const res = await deliverPlanComments(k(), { pending: true, respond, say });

    expect(res).toMatchObject({ ok: true, via: "reject" });
    expect(say).not.toHaveBeenCalled(); // 承認ダイアログが開いたまま自由文を送らない
    // 送った本文がそのまま返る（呼び出し側はこれを楽観エコーに使う）。
    expect(respond).toHaveBeenCalledWith(feedbackOf(res));
    expect(feedbackOf(res)).toContain("1つめ");
    expect(sentBodies()).toEqual(["1つめ", "2つめ"]);
  });

  it("承認待ちでなければ普通の発話として送る", async () => {
    seed("指摘");
    const respond = vi.fn();
    const say = vi.fn().mockResolvedValue(true);
    const res = await deliverPlanComments(k(), { pending: false, respond, say });

    expect(res).toMatchObject({ ok: true, via: "prompt" });
    expect(respond).not.toHaveBeenCalled();
    expect(sentBodies()).toEqual(["指摘"]);
  });

  // 実障害そのもの: 発話が弾かれた（許可待ち・停止中など）のに畳んでいた。
  it("発話が届かなければ何も畳まない — 打ち直せる状態を保つ", async () => {
    seed("指摘");
    const say = vi.fn().mockResolvedValue(false);
    const res = await deliverPlanComments(k(), { pending: false, respond: vi.fn(), say });

    expect(res).toEqual({ ok: false, reason: "failed" });
    expect(sentBodies()).toEqual([]);
    expect(unsentComments(getPlanComments(k()))).toHaveLength(1);
  });

  it("no_plan は発話へ落とす。その発話も失敗したら畳まない", async () => {
    seed("指摘");
    const respond = vi.fn().mockResolvedValue({ ok: false, code: "no_plan" });
    const say = vi.fn().mockResolvedValue(false);
    const res = await deliverPlanComments(k(), { pending: true, respond, say });

    expect(say).toHaveBeenCalledTimes(1); // フォールバックは走る
    expect(res).toEqual({ ok: false, reason: "failed" });
    expect(sentBodies()).toEqual([]);
  });

  it("no_plan から発話で届けば畳む（経路は prompt）", async () => {
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

  it("却下は通ったが本文が入らなかった（undelivered）ときは畳まない", async () => {
    seed("指摘");
    const res = await deliverPlanComments(k(), {
      pending: true,
      respond: vi.fn().mockResolvedValue({ ok: true, delivered: false, message: "コンポーザ復帰を確認できず" }),
      say: vi.fn().mockResolvedValue(true),
    });

    expect(res).toEqual({ ok: false, reason: "undelivered", message: "コンポーザ復帰を確認できず" });
    expect(sentBodies()).toEqual([]);
  });

  it("respond の失敗理由はそのまま返す（呼び出し側がトーストする）", async () => {
    seed("指摘");
    const res = await deliverPlanComments(k(), {
      pending: true,
      respond: vi.fn().mockResolvedValue({ ok: false, code: "not_running", message: "セッションが停止しています" }),
      say: vi.fn().mockResolvedValue(true),
    });

    expect(res).toEqual({ ok: false, reason: "failed", message: "セッションが停止しています" });
    expect(sentBodies()).toEqual([]);
  });

  it("未送信が無ければ何も送らない（null）", async () => {
    const ids = seed("送信済みにする");
    markPlanCommentsSent(k(), ids);
    const respond = vi.fn();
    const say = vi.fn();
    expect(await deliverPlanComments(k(), { pending: true, respond, say })).toBeNull();
    expect(respond).not.toHaveBeenCalled();
    expect(say).not.toHaveBeenCalled();
  });

  it("送るのも畳むのも未送信ぶんだけ — 送信済みは本文にも入らない", async () => {
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
