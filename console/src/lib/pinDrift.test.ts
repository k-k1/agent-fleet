import { describe, expect, it } from "vitest";
import { pinDrift } from "./pinDrift.ts";

// 実測の版形状（env_tool_versions の extractVer 出力と versions.json のピン）で
// 方向判定を固定する。
describe("pinDrift", () => {
  it("behind: ピンより古い（kiro の固着で実際に起きた形）", () => {
    expect(pinDrift("2.14.1", "2.14.2")).toBe("behind");
    expect(pinDrift("1.18.5", "1.18.8")).toBe("behind");
    expect(pinDrift("2.1.219", "2.1.220")).toBe("behind");
  });

  it("ahead: ピンより新しい（自己更新 ON の前進で実際に起きた形）", () => {
    expect(pinDrift("1.18.8", "1.18.5")).toBe("ahead");
    expect(pinDrift("0.44.0", "0.43.0")).toBe("ahead");
    expect(pinDrift("1.1.8", "1.1.7")).toBe("ahead");
  });

  it("same: 一致（バッジなし）", () => {
    expect(pinDrift("2.1.220", "2.1.220")).toBe("same");
    expect(pinDrift("1.0.75", "1.0.75")).toBe("same");
  });

  it("cursor の日付版数: ピンの sha 接尾辞は無視して日付部で比較", () => {
    // 実効は extractVer が接尾辞を落とした "2026.07.23"、ピンは sha 付きの生値
    expect(pinDrift("2026.07.23", "2026.07.23-e383d2b")).toBe("same");
    expect(pinDrift("2026.07.20", "2026.07.23-e383d2b")).toBe("behind");
    expect(pinDrift("2026.07.30", "2026.07.23-e383d2b")).toBe("ahead");
  });

  it("セグメント数の差は欠けを 0 扱い", () => {
    expect(pinDrift("1.2", "1.2.3")).toBe("behind");
    expect(pinDrift("1.2.3", "1.2")).toBe("ahead");
    expect(pinDrift("1.2.0", "1.2")).toBe("same");
  });

  it("unknown: 判定できない形はバッジを出さない", () => {
    expect(pinDrift("(取得失敗)", "1.1.7")).toBe("unknown"); // agy の RDRAND SIGABRT ホスト
    expect(pinDrift("(timeout)", "1.1.7")).toBe("unknown");
    expect(pinDrift("", "1.1.7")).toBe("unknown");
    expect(pinDrift(undefined, "1.1.7")).toBe("unknown");
    expect(pinDrift("1.1.7", "")).toBe("unknown");
    expect(pinDrift("1.1.7", undefined)).toBe("unknown"); // ピン無しツール（node/python）
  });
});
