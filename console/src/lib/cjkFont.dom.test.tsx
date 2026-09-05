import { describe, expect, it, afterEach } from "vitest";
import {
  applyCjkFont,
  fontStack,
  termFontStack,
  chatFontStack,
  readerFontStack,
  getSettings,
  setSetting,
  CJK_FAMILY,
  CJK_FAMILY_PROSE,
  CJK_FONT_AUTO,
  CJK_FONT_OFF,
  CJK_UNICODE_RANGE,
} from "./settings.ts";

// Japanese font settings: they make East Asian Width = Ambiguous characters (①②③ and friends)
// render with the Japanese face. Whether they take effect comes down to two things - the
// @font-face rules being installed, and the family sitting first in the stack - so both are
// pinned here.
const rule = () => document.getElementById("af-cjk-font")?.textContent ?? "";

afterEach(() => {
  setSetting("cjkFont", CJK_FONT_AUTO);
});

describe("applyCjkFont", () => {
  it("installs two unicode-range-limited @font-face rules by default (auto)", () => {
    setSetting("cjkFont", CJK_FONT_AUTO);
    expect(rule()).toContain(`font-family:"${CJK_FAMILY}"`);
    expect(rule()).toContain(`font-family:"${CJK_FAMILY_PROSE}"`);
    expect(rule()).toContain(`unicode-range:${CJK_UNICODE_RANGE}`);
    // Without ① (U+2460) in range the symptom is not fixed
    expect(rule()).toContain("U+2460-24FF");
    // Arrows and box-drawing, which are used for column alignment, stay out of both ranges
    expect(rule()).not.toContain("U+2190");
    expect(rule()).not.toContain("U+2500");
  });

  it("keeps ■ ○ ★ out of the monospace face (they skew CLI output) and only in the prose face", () => {
    setSetting("cjkFont", CJK_FONT_AUTO);
    const [mono, prose] = rule().split("@font-face").filter(Boolean);
    expect(mono).toContain(CJK_FAMILY);
    expect(mono).not.toContain("U+25A0-25FF");
    expect(prose).toContain(CJK_FAMILY_PROSE);
    expect(prose).toContain("U+25A0-25FF");
    expect(prose).toContain("U+2600-26FF");
  });

  it("puts the chosen font first and stacks the generic Japanese chain behind it", () => {
    setSetting("cjkFont", "Meiryo");
    const src = rule();
    expect(src.indexOf('local("Meiryo")')).toBeLessThan(src.indexOf('local("Noto Sans CJK JP")'));
    // The generic entries always remain, so Mac/Linux still find a face when it is not installed
    expect(src).toContain('local("Hiragino Kaku Gothic ProN")');
  });

  it("removes the whole rule for Latin-first (back to the previous appearance)", () => {
    setSetting("cjkFont", CJK_FONT_OFF);
    expect(document.getElementById("af-cjk-font")).toBeNull();
  });

  it("keeps exactly one <style> across setting changes", () => {
    setSetting("cjkFont", "Yu Gothic");
    setSetting("cjkFont", CJK_FONT_AUTO);
    applyCjkFont(getSettings());
    expect(document.querySelectorAll("#af-cjk-font").length).toBe(1);
  });
});

describe("font stack prefixing", () => {
  it("puts CJK_FAMILY first for monospace (viewer, diff, mirror)", () => {
    expect(fontStack("JetBrains Mono").startsWith(`"${CJK_FAMILY}",`)).toBe(true);
    expect(fontStack("システム等幅").startsWith(`"${CJK_FAMILY}",`)).toBe(true);
  });

  it("does not prefix the terminal (xterm counts Ambiguous as one column)", () => {
    expect(termFontStack("Source Code Pro")).not.toContain(CJK_FAMILY);
    expect(termFontStack("システム等幅")).not.toContain(CJK_FAMILY);
  });

  it("prefixes only the gothic side for chat, never the serif or mincho ones", () => {
    expect(chatFontStack("システム")).toContain(CJK_FAMILY_PROSE);
    expect(chatFontStack("セリフ")).not.toContain(CJK_FAMILY);
    expect(readerFontStack("明朝")).not.toContain(CJK_FAMILY);
    expect(readerFontStack("ゴシック")).not.toContain(CJK_FAMILY);
  });
});
