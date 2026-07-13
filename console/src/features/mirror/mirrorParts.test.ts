import { describe, expect, it } from "vitest";
import { confirmedWorkEnd, textOfParts, workSplit } from "./mirrorParts.ts";

describe("workSplit", () => {
  it("最後のツール以前を作業過程、以後を最終回答に分ける", () => {
    const parts = [
      { kind: "text", text: "調べます" },
      { kind: "tool" },
      { kind: "text", text: "追加確認します" },
      { kind: "tool" },
      { kind: "text", text: "修正できました" },
      { kind: "userfile" },
    ];
    expect(workSplit(parts)).toEqual({ at: 4, tools: 2, responses: 2 });
    expect(textOfParts(parts.slice(4))).toBe("修正できました");
  });

  it("質問とプランも作業境界として扱う", () => {
    expect(workSplit([{ kind: "question" }, { kind: "text", text: "回答です" }])).toEqual({
      at: 1,
      tools: 1,
      responses: 0,
    });
    expect(workSplit([{ kind: "plan" }, { kind: "text", text: "完了しました" }])?.at).toBe(1);
  });

  it("ツール無し、または最終テキスト未到着なら分割しない", () => {
    expect(workSplit([{ kind: "text", text: "通常回答" }])).toBeNull();
    expect(workSplit([{ kind: "text", text: "確認中" }, { kind: "tool" }])).toBeNull();
    expect(workSplit([{ kind: "tool" }, { kind: "thinking", text: "推論" }])).toBeNull();
  });

  it("最終回答を待たず、ツール到着時点までを確定済み作業過程として返す", () => {
    expect(confirmedWorkEnd([{ kind: "text" }, { kind: "tool" }, { kind: "text" }])).toBe(2);
    expect(confirmedWorkEnd([{ kind: "text" }])).toBe(0);
  });
});
