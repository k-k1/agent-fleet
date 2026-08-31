import { describe, it, expect } from "vitest";
import { autoGrowTextarea } from "./autoGrow.ts";

// jsdom にレイアウトは無いので「転写がずれない」ことは書けない（それは
// scripts/mirror-scroll の typing シナリオ＝実 Chromium の担当）。ここで固定するのは、その
// 対策が成り立つための唯一の条件 — 計測のために入力欄を縮めている“その瞬間”に、親の行が
// min-height で止まっていること。ここを外すと縮みが兄弟の転写へ漏れる。
function composer(): { row: HTMLDivElement; input: HTMLTextAreaElement } {
  const row = document.createElement("div");
  const input = document.createElement("textarea");
  row.appendChild(input);
  document.body.appendChild(row);
  return { row, input };
}

describe("autoGrowTextarea", () => {
  it("計測のあいだ親の行を min-height で止め、終わったら元に戻す", () => {
    const { row, input } = composer();
    row.getBoundingClientRect = () => ({ height: 212 }) as DOMRect;
    const seen: string[] = [];
    // scrollHeight を読む＝計測の瞬間。そのときの親の min-height を記録する。
    Object.defineProperty(input, "scrollHeight", {
      get() {
        seen.push(row.style.minHeight);
        return 260;
      },
    });

    autoGrowTextarea(input);

    expect(seen).toEqual(["212px"]); // 縮めた状態が外へ漏れない
    expect(input.style.height).toBe("260px");
    expect(row.style.minHeight).toBe(""); // 後片付け（次のレイアウトを縛らない）
  });

  it("親が持っていた min-height は元の値に戻す", () => {
    const { row, input } = composer();
    row.style.minHeight = "54px";
    row.getBoundingClientRect = () => ({ height: 54 }) as DOMRect;
    Object.defineProperty(input, "scrollHeight", { get: () => 38 });

    autoGrowTextarea(input);

    expect(row.style.minHeight).toBe("54px");
  });

  it("要素が無ければ何もしない（マウント前・アンマウント後）", () => {
    expect(() => autoGrowTextarea(null)).not.toThrow();
  });
});
