import { describe, expect, it } from "vitest";
import { confirmedWorkEnd, latestWorkPromptIndex, textOfParts, workSplit } from "./mirrorParts.ts";

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

  it("実回答の後にメモ書き込み等の後始末ツール＋短い続きが来ても、実回答から最終回答扱いにする", () => {
    const parts = [
      { kind: "text", text: "調べます" },
      { kind: "tool" },
      { kind: "text", text: "ここが実際の最終回答で、いちばん長い本文になります。" },
      { kind: "tool" }, // メモリ書き込み（後始末）
      { kind: "text", text: "メモしました" },
    ];
    // 境界は末尾ツール直後(4)ではなく、実回答(2)。後始末ツールと続きは最終回答側に残す。
    expect(workSplit(parts)).toEqual({ at: 2, tools: 1, responses: 1 });
    expect(textOfParts(parts.slice(2))).toBe(
      "ここが実際の最終回答で、いちばん長い本文になります。\n\nメモしました",
    );
  });

  it("末尾テキストが直前テキスト以上なら剥がさない（通常のナレーション→ツール→回答）", () => {
    const parts = [
      { kind: "text", text: "確認します" },
      { kind: "tool" },
      { kind: "text", text: "短い経過" },
      { kind: "tool" },
      { kind: "text", text: "これが最終的な回答の本文です。" },
    ];
    expect(workSplit(parts)).toEqual({ at: 4, tools: 2, responses: 2 });
  });

  it("複数の後始末ツールを繰り返し剥がす", () => {
    const parts = [
      { kind: "tool" },
      { kind: "text", text: "十分に長い実際の最終回答の本文です。" },
      { kind: "tool" }, // 後始末1
      { kind: "text", text: "追記" },
      { kind: "tool" }, // 後始末2
      { kind: "text", text: "完了" },
    ];
    expect(workSplit(parts)).toEqual({ at: 1, tools: 1, responses: 0 });
  });

  it("最終回答を待たず、ツール到着時点までを確定済み作業過程として返す", () => {
    expect(confirmedWorkEnd([{ kind: "text" }, { kind: "tool" }, { kind: "text" }])).toBe(2);
    expect(confirmedWorkEnd([{ kind: "text" }])).toBe(0);
  });
});

describe("latestWorkPromptIndex", () => {
  it("送信直後のpendingターンを今回の作業境界として扱う", () => {
    const groups = [
      { role: "user" },
      { role: "assistant" },
      { role: "user", pending: true },
    ];
    expect(latestWorkPromptIndex(groups)).toBe(2);
  });

  it("未実行のqueued promptは現在の作業境界にしない", () => {
    const groups = [
      { role: "user" },
      { role: "assistant" },
      { role: "user", queued: true },
    ];
    expect(latestWorkPromptIndex(groups)).toBe(0);
  });
});
