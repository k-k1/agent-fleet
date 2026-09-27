// The picker's names: a title wins, and an untitled studio is named by when it was made.
import { describe, expect, it } from "vitest";
import { studioName, studioStamp } from "./studios.ts";

const dated = (s: string) => `スタジオ ${s}`;

describe("スタジオの名前", () => {
  it("名前があればそれを使う（前後の空白は落とす）", () => {
    expect(studioName({ id: "x", title: "  港の夕暮れ ", updated_at: "" }, dated)).toBe("港の夕暮れ");
  });

  it("無題は作成日時で呼ぶ（ローカル時刻の M/D HH:MM）", () => {
    const iso = new Date(2026, 8, 27, 14, 5).toISOString();
    expect(studioStamp(iso)).toBe("9/27 14:05");
    expect(studioName({ id: "48c71fb2-aaaa", title: "", created_at: iso, updated_at: "" }, dated)).toBe("スタジオ 9/27 14:05");
  });

  it("created_at の無い古い Agent は updated_at、どちらも読めなければ id の頭", () => {
    const iso = new Date(2026, 0, 3, 9, 7).toISOString();
    expect(studioName({ id: "x", title: "", updated_at: iso }, dated)).toBe("スタジオ 1/3 09:07");
    expect(studioName({ id: "48c71fb2-aaaa", title: "", updated_at: "garbage" }, dated)).toBe("スタジオ 48c71fb2");
  });
});
