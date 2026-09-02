// コマンドパレットのセッション欄の並び。「最後に入力待ちになったものが一番上、停止中は
// 一番下」という約束は、ここが唯一の実装なのでここで固定する。
import { describe, expect, it } from "vitest";
import { sessionTier, sortSessionsByAttention, waitingAtFromNotifications, TIER_ALIVE, TIER_STOPPED, TIER_WAITING } from "./order.ts";
import type { Session } from "../../types/session.ts";

const s = (name: string, extra: Partial<Session> = {}): Session => ({
  name,
  kind: "claude",
  alive: true,
  createdAt: "2026-09-01T00:00:00Z",
  ...extra,
});

/** 名前 → 「最後に人待ちになった時刻」。表に無ければ 0（不明）。 */
const ledger = (map: Record<string, number>) => (name: string) => map[name] || 0;
const names = (list: Session[]) => list.map((x) => x.name);

describe("sessionTier", () => {
  it("treats only a live question/plan/permission as 入力待ち", () => {
    expect(sessionTier(s("a", { state: "question" }))).toBe(TIER_WAITING);
    expect(sessionTier(s("b", { state: "plan" }))).toBe(TIER_WAITING);
    expect(sessionTier(s("c", { state: "permission" }))).toBe(TIER_WAITING);
    expect(sessionTier(s("d", { state: "working" }))).toBe(TIER_ALIVE);
    // 上限リセット待ちは「時計待ち」— 人は何も答えられないので最上段には上げない。
    expect(sessionTier(s("e", { state: "limited" }))).toBe(TIER_ALIVE);
    // 畳まれたときに質問を抱えていた行も、段としては停止中（「下部でよい」）。
    expect(sessionTier(s("f", { alive: false, carried: "question" }))).toBe(TIER_STOPPED);
  });
});

describe("sortSessionsByAttention", () => {
  it("puts the most recently waiting session on top and the stopped ones at the foot", () => {
    const list = [
      s("stopped", { alive: false }),
      s("workingOld", { state: "working" }),
      s("askedFirst", { state: "question" }),
      s("askedLast", { state: "permission" }),
    ];
    const at = ledger({ askedFirst: 1_000, askedLast: 2_000 });
    expect(names(sortSessionsByAttention(list, at))).toEqual(["askedLast", "askedFirst", "workingOld", "stopped"]);
  });

  it("orders the live non-waiting ones by when they last waited, newest first", () => {
    // 直前に答えたセッション（just）が、ずっと黙って進んでいるものより上。
    const list = [s("never", { state: "working" }), s("old", { state: "idle" }), s("just", { state: "working" })];
    const at = ledger({ just: 5_000, old: 100 });
    expect(names(sortSessionsByAttention(list, at))).toEqual(["just", "old", "never"]);
  });

  it("falls back to newest-created when nothing ever waited", () => {
    const list = [
      s("old", { state: "working", createdAt: "2026-08-01T00:00:00Z" }),
      s("new", { state: "working", createdAt: "2026-09-01T00:00:00Z" }),
    ];
    expect(names(sortSessionsByAttention(list, ledger({})))).toEqual(["new", "old"]);
  });

  it("keeps a stopped session that carried an unanswered question ahead of the plain stopped ones", () => {
    const list = [
      s("plain", { alive: false, createdAt: "2026-09-01T00:00:00Z" }),
      s("carried", { alive: false, carried: "question", createdAt: "2026-08-01T00:00:00Z" }),
    ];
    expect(names(sortSessionsByAttention(list, ledger({})))).toEqual(["carried", "plain"]);
  });

  it("does not reorder its input", () => {
    const list = [s("b", { alive: false }), s("a", { state: "question" })];
    sortSessionsByAttention(list, ledger({ a: 1 }));
    expect(names(list)).toEqual(["b", "a"]);
  });
});

describe("waitingAtFromNotifications", () => {
  const n = (kind: string, id: string, createdAt: string, type = "session") => ({
    kind,
    target: { type, id },
    createdAt,
  });

  it("takes the newest 人待ち notification per session", () => {
    const at = waitingAtFromNotifications([
      n("question", "s1", "2026-09-01T10:00:00Z"),
      n("permission-request", "s1", "2026-09-01T12:00:00Z"),
      n("plan-approval", "s2", "2026-09-01T09:00:00Z"),
    ]);
    expect(at.s1).toBe(Date.parse("2026-09-01T12:00:00Z"));
    expect(at.s2).toBe(Date.parse("2026-09-01T09:00:00Z"));
  });

  it("ignores notifications that are not a session waiting for a person", () => {
    const at = waitingAtFromNotifications([
      n("answer-ready", "s1", "2026-09-01T10:00:00Z"), // 回答が返ってきた＝待っていない
      n("question", "sched1", "2026-09-01T10:00:00Z", "schedule"), // セッション宛ではない
      n("question", "s2", "not-a-date"),
    ]);
    expect(at).toEqual({});
  });
});
