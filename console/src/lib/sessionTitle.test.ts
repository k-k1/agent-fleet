import { describe, it, expect } from "vitest";
import { SESSION_TITLE_MAX, clampSessionTitle } from "./sessionTitle.ts";

// 正は Agent の cleanTitle（workspace/agent/session.go）。ここが緩むと、提案は保存
// できるのに起動だけ bad_title で落ちる（引き継ぎタイトルの実障害）。
describe("clampSessionTitle", () => {
  it("leaves a normal title alone", () => {
    expect(clampSessionTitle("docs/log/80 の続き")).toBe("docs/log/80 の続き");
    expect(clampSessionTitle("  前後の空白は落とす  ")).toBe("前後の空白は落とす");
    expect(clampSessionTitle("")).toBe("");
  });
  it("cuts to the create API's limit, counting code points like Go's runes", () => {
    expect(clampSessionTitle("あ".repeat(200))).toHaveLength(SESSION_TITLE_MAX);
    expect(clampSessionTitle("x".repeat(81))).toBe("x".repeat(SESSION_TITLE_MAX));
    // 絵文字（サロゲートペア）も 1 文字として数える — UTF-16 length で切ると
    // Go 側の 80 runes と食い違い、通るはずの名前まで削れる。
    expect(Array.from(clampSessionTitle("🙂".repeat(100)))).toHaveLength(SESSION_TITLE_MAX);
  });
  it("strips control characters instead of failing the launch on them", () => {
    expect(clampSessionTitle("改行\nを含む\tタイトル")).toBe("改行 を含む タイトル");
    expect(clampSessionTitle("末尾に改行がある\n")).toBe("末尾に改行がある");
  });
  it("never leaves a trailing space created by the cut", () => {
    expect(clampSessionTitle("y".repeat(79) + " tail")).toBe("y".repeat(79));
  });
});
