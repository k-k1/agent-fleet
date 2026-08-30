import { describe, expect, it } from "vitest";
import { BODY_PART, markRootKey, parseRootKey, turnKey } from "./marks.ts";
import { groupTurns } from "./model.ts";
import type { Turn } from "./types.ts";

describe("turnKey", () => {
  it("prefers the agent's own anchor", () => {
    expect(turnKey({ role: "assistant", anchorId: "uuid-1", text: "hi" })).toBe("uuid-1");
  });

  it("falls back to a hash of the text when the kind emits no anchor", () => {
    const a = turnKey({ role: "assistant", text: "same words" });
    const b = turnKey({ role: "assistant", text: "same words" });
    const c = turnKey({ role: "assistant", text: "other words" });
    expect(a).toMatch(/^h:/);
    expect(a).toBe(b); // 両側が同じ本文から同じキーを出す — 共有の前提
    expect(a).not.toBe(c);
  });

  // 送信直後のローカルエコーに印を置かせると、本物のターンが届いた瞬間に anchorId が付いて
  // キーが変わり、置いた印が宙に浮く。
  it("refuses a pending echo and a queued prompt", () => {
    expect(turnKey({ role: "user", text: "hi", pending: true })).toBe("");
    expect(turnKey({ role: "user", text: "hi", queued: true })).toBe("");
    expect(turnKey({ role: "user" })).toBe("");
  });
});

describe("markRootKey / parseRootKey", () => {
  it("round-trips a part and the turn body", () => {
    expect(parseRootKey(markRootKey("uuid-1", 3))).toEqual({ turn: "uuid-1", part: 3 });
    expect(parseRootKey(markRootKey("uuid-1", BODY_PART))).toEqual({ turn: "uuid-1", part: BODY_PART });
  });

  it("survives an anchor that itself contains the separator", () => {
    expect(parseRootKey(markRootKey("msg#odd", 1))).toEqual({ turn: "msg#odd", part: 1 });
  });

  it("rejects a malformed key rather than guessing", () => {
    expect(parseRootKey("no-separator")).toBeNull();
    expect(parseRootKey("#0")).toBeNull();
    expect(parseRootKey("uuid-1#nope")).toBeNull();
  });
});

// ⚠️ これが本命の回帰テスト。ミラーと共有ビューは別々の tail 窓を持つので、ブロックへ
// 何行畳み込まれたかは両側で違う。root がブロック相対だと、その差がそのまま「共有先だけ
// 印が 1 つ隣」になる（docs/log/69 §69.3.2）。
describe("groupTurns の origins", () => {
  const older: Turn = {
    role: "assistant",
    anchorId: "uuid-1",
    text: "first",
    parts: [{ kind: "text", text: "first" }],
  };
  const newer: Turn = {
    role: "assistant",
    anchorId: "uuid-2",
    text: "second",
    parts: [
      { kind: "text", text: "second" },
      { kind: "text", text: "third" },
    ],
  };

  it("keys every part to its SOURCE turn, not to the block", () => {
    const [block] = groupTurns([older, newer]);
    expect(block.parts).toHaveLength(3);
    expect(block.origins).toEqual(["uuid-1#0", "uuid-2#0", "uuid-2#1"]);
  });

  it("gives the same part the same root whether or not the earlier turn is in the window", () => {
    const whole = groupTurns([older, newer]);
    const windowed = groupTurns([newer]); // 上へ遡っていない側の窓
    expect(whole[0].origins.slice(1)).toEqual(windowed[0].origins);
  });

  it("leaves no root on a part whose kind cannot cross the shared DTO verbatim", () => {
    const [block] = groupTurns([
      {
        role: "assistant",
        anchorId: "uuid-3",
        text: "x",
        parts: [
          { kind: "text", text: "prose" },
          { kind: "tool", tool: "Edit", file: "/home/dev/repos/private/secret.ts" },
        ],
      },
    ]);
    expect(block.origins).toEqual(["uuid-3#0", ""]);
  });

  // ユーザーの吹き出しはブロックの text を描くので、2 行以上畳んだ時点でその文字列は
  // 「窓に何行入っていたか」で変わる。出現番号のアンカーはそこに置けない。
  it("drops the user body root once a block folds more than one turn", () => {
    const one = groupTurns([{ role: "user", anchorId: "u-1", text: "hello" }]);
    expect(one[0].bodyRoot).toBe("u-1#b");
    expect(one[0].folded).toBe(1);

    const merged = groupTurns([
      { role: "user", anchorId: "u-1", text: "hello" },
      { role: "user", anchorId: "u-2", text: "again" },
    ]);
    expect(merged).toHaveLength(1);
    expect(merged[0].folded).toBe(2);
    expect(merged[0].bodyRoot).toBe("");
  });
});
