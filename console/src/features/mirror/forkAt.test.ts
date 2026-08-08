import { describe, it, expect } from "vitest";
import { canBranchFrom, carriedUserTurns } from "./forkAt.ts";
import type { BranchableTurn } from "./forkAt.ts";

const user = (anchorId?: string, extra: Partial<BranchableTurn> = {}): BranchableTurn => ({
  role: "user",
  anchorId,
  ...extra,
});
const bot = (): BranchableTurn => ({ role: "assistant", anchorId: "msg_a" });

describe("canBranchFrom", () => {
  it("allows a landed user turn that carries an anchor", () => {
    expect(canBranchFrom(user("msg_1"))).toBe(true);
  });

  it("refuses an assistant turn", () => {
    // v1 は「その発言を打ち直す」ための分岐なので、回答からは分岐させない。
    expect(canBranchFrom(bot())).toBe(false);
  });

  it("refuses a turn with no anchor", () => {
    // アンカー無しで分岐要求を投げると at が空になり、会話まるごと分岐に化ける。
    expect(canBranchFrom(user(undefined))).toBe(false);
    expect(canBranchFrom(user(""))).toBe(false);
  });

  it("refuses an optimistic echo and a queued prompt", () => {
    // どちらもまだ転写に無いので、指せる id が存在しない。
    expect(canBranchFrom(user("msg_1", { pending: true }))).toBe(false);
    expect(canBranchFrom(user("msg_1", { queued: true }))).toBe(false);
  });

  it("refuses a compaction summary", () => {
    expect(canBranchFrom(user("msg_1", { compact: true }))).toBe(false);
  });
});

describe("carriedUserTurns", () => {
  it("counts the user turns BEFORE the branch point, excluding it", () => {
    const u1 = user("msg_1");
    const u2 = user("msg_3");
    const u3 = user("msg_5");
    const groups = [u1, bot(), u2, bot(), u3];
    expect(carriedUserTurns(groups, u1)).toBe(0); // 最初の発言から分岐 = まっさら
    expect(carriedUserTurns(groups, u2)).toBe(1);
    expect(carriedUserTurns(groups, u3)).toBe(2);
  });

  it("does not count compaction summaries as user turns", () => {
    const summary = user("msg_2", { compact: true });
    const u2 = user("msg_3");
    const groups = [user("msg_1"), summary, u2];
    expect(carriedUserTurns(groups, u2)).toBe(1);
  });

  it("returns 0 for a turn that is not in the list", () => {
    expect(carriedUserTurns([user("msg_1")], user("msg_9"))).toBe(0);
  });
});
