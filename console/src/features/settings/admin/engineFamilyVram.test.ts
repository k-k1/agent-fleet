// 族ごとの実測 VRAM（ADR 0094 決定 8）。表そのものは数字の羅列なので、試験が押さえるのは
// 「引けること」と「測っていない族には何も返さないこと」の 2 つだけ——後者がこの表の要点で、
// 推定値を実測として出さないためにある。
import { describe, expect, it } from "vitest";
import { FAMILY_VRAM_MEASURED, familyVramMeasurement } from "./engineFamilyVram.ts";

describe("族の実測 VRAM", () => {
  it("2509 は実測値と測定条件をひと組で返す", () => {
    const m = familyVramMeasurement("qwen-image-edit-2509");
    // 実測（開発配備・L4 24GB）: vram_total 23,659,151,360 B − vram_free 1,783,934,774 B。
    expect(m?.mib).toBe(20862);
    expect(m?.size).toBe("1024x1024");
    expect(m?.batch).toBe(1);
    expect(m?.inputs).toBe(1);
  });

  it("base_model の綴りは行と同じ扱いで引ける（前後の空白・大小）", () => {
    expect(familyVramMeasurement(" QWEN-IMAGE-EDIT-2509 ")?.mib).toBe(20862);
  });

  // 🔴 これが表の主張そのもの。測っていない族に「たぶんこれくらい」を返した時点で、
  // vram_mib の意味が「誰かが測った数字」から「誰かが書いた数字」に変わる。
  it("測っていない族には何も返さない（2511 も含む）", () => {
    expect(familyVramMeasurement("sdxl")).toBeNull();
    expect(familyVramMeasurement("qwen-image-edit-2511")).toBeNull();
    expect(familyVramMeasurement("")).toBeNull();
    expect(familyVramMeasurement(undefined)).toBeNull();
  });

  // 表に足すときは測定条件も一緒に、という約束を機械側でも保つ（条件の無い数字は
  // 比べようがない——寸法と batch で VRAM は動く）。
  it("すべての行が測定条件を持つ", () => {
    for (const [family, m] of Object.entries(FAMILY_VRAM_MEASURED)) {
      expect(m.mib, family).toBeGreaterThan(0);
      expect(m.size, family).toMatch(/^\d+x\d+$/);
      expect(m.batch, family).toBeGreaterThan(0);
      expect(m.inputs, family).toBeGreaterThanOrEqual(0);
    }
  });
});
