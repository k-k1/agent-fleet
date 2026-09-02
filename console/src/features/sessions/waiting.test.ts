// 「最後に人待ちに入った時刻」の観測台帳。並び順の材料なので、間違って記録すると
// パレットの最上段が嘘になる。とくに **初観測では記録しない**（いつ入ったか分からない
// のに now を焼くと、リロード直後に全員が同時刻で並び、通知台帳の本物の順序を潰す）。
import { beforeEach, describe, expect, it, vi } from "vitest";

const values = new Map<string, string>();
vi.stubGlobal("localStorage", {
  getItem: (key: string) => values.get(key) ?? null,
  setItem: (key: string, value: string) => values.set(key, value),
  removeItem: (key: string) => values.delete(key),
});

import { isWaiting, noteSessions, observedWaitingAt, resetWaitingLedgerForTest } from "./waiting.ts";
import type { Session } from "../../types/session.ts";

const s = (name: string, state: string, alive = true): Session => ({ name, kind: "claude", alive, state });

describe("waiting ledger", () => {
  beforeEach(() => {
    values.clear();
    resetWaitingLedgerForTest();
  });

  it("counts only a live question/plan/permission as 人待ち", () => {
    expect(isWaiting(s("a", "question"))).toBe(true);
    expect(isWaiting(s("a", "plan"))).toBe(true);
    expect(isWaiting(s("a", "permission"))).toBe(true);
    expect(isWaiting(s("a", "working"))).toBe(false);
    expect(isWaiting(s("a", "question", false))).toBe(false); // 停止中は「今すぐ答えれば進む」ではない
  });

  it("does not stamp a session that was ALREADY waiting when first seen", () => {
    noteSessions([s("a", "question")], 1_000);
    expect(observedWaitingAt("a")).toBe(0);
  });

  it("stamps the transition into waiting, and only that transition", () => {
    noteSessions([s("a", "working")], 1_000);
    noteSessions([s("a", "question")], 2_000);
    expect(observedWaitingAt("a")).toBe(2_000);
    // 待ち続けている間は「入った時刻」であって「今」ではない。
    noteSessions([s("a", "question")], 3_000);
    expect(observedWaitingAt("a")).toBe(2_000);
    // 答えて進み、また質問 → 2 度目の時刻に更新される（通知台帳だけでは古いままになる穴）。
    noteSessions([s("a", "working")], 4_000);
    noteSessions([s("a", "plan")], 5_000);
    expect(observedWaitingAt("a")).toBe(5_000);
  });

  it("survives a reload through localStorage", () => {
    noteSessions([s("a", "working")], 1_000);
    noteSessions([s("a", "question")], 2_000);
    resetWaitingLedgerForTest(); // = 新しいタブ / リロード
    expect(observedWaitingAt("a")).toBe(2_000);
  });

  it("forgets sessions that left the list, but never on an empty list", () => {
    noteSessions([s("a", "working"), s("b", "working")], 1_000);
    noteSessions([s("a", "question"), s("b", "question")], 2_000);
    // 空一覧は「全部消えた」ではない（取得失敗でも初期値でも空になる）。
    noteSessions([], 3_000);
    expect(observedWaitingAt("a")).toBe(2_000);
    expect(observedWaitingAt("b")).toBe(2_000);
    // b が消えた一覧が届いたら b だけ落とす。
    noteSessions([s("a", "question")], 4_000);
    expect(observedWaitingAt("a")).toBe(2_000);
    expect(observedWaitingAt("b")).toBe(0);
  });

  it("ignores a corrupted stored value instead of throwing", () => {
    values.set("af.sessionWaitingAt", "{not json");
    expect(observedWaitingAt("a")).toBe(0);
  });
});
