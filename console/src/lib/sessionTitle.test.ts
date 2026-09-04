import { describe, it, expect } from "vitest";
import { SESSION_TITLE_MAX, clampSessionTitle } from "./sessionTitle.ts";

// The Agent's cleanTitle (workspace/agent/session.go) is authoritative. If this side gets laxer, a
// proposal saves fine and only the launch fails with bad_title.
describe("clampSessionTitle", () => {
  it("leaves a normal title alone", () => {
    expect(clampSessionTitle("docs/log/80 の続き")).toBe("docs/log/80 の続き");
    expect(clampSessionTitle("  前後の空白は落とす  ")).toBe("前後の空白は落とす");
    expect(clampSessionTitle("")).toBe("");
  });
  it("cuts to the create API's limit, counting code points like Go's runes", () => {
    expect(clampSessionTitle("あ".repeat(200))).toHaveLength(SESSION_TITLE_MAX);
    expect(clampSessionTitle("x".repeat(81))).toBe("x".repeat(SESSION_TITLE_MAX));
    // An emoji (a surrogate pair) counts as one character. Cutting on UTF-16 length would
    // disagree with Go's 80 runes and truncate names that should have passed.
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
