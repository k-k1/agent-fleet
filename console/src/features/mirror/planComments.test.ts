import { beforeEach, describe, expect, it } from "vitest";
import {
  addPlanComment,
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
