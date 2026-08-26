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

// 和文フォント（①②③ など East Asian Width = Ambiguous の文字を和文側で描く指定）。
// 効き目は「@font-face が張られていること」と「スタックの先頭に family が居ること」の
// 2 点で決まるので、その両方を押さえる。
const rule = () => document.getElementById("af-cjk-font")?.textContent ?? "";

afterEach(() => {
  setSetting("cjkFont", CJK_FONT_AUTO);
});

describe("applyCjkFont", () => {
  it("既定（自動）で unicode-range 限定の @font-face を 2 つ張る", () => {
    setSetting("cjkFont", CJK_FONT_AUTO);
    expect(rule()).toContain(`font-family:"${CJK_FAMILY}"`);
    expect(rule()).toContain(`font-family:"${CJK_FAMILY_PROSE}"`);
    expect(rule()).toContain(`unicode-range:${CJK_UNICODE_RANGE}`);
    // ①（U+2460）が入っていないと今回の症状が直らない
    expect(rule()).toContain("U+2460-24FF");
    // 桁揃えに使う矢印・罫線はどちらの範囲からも外れている
    expect(rule()).not.toContain("U+2190");
    expect(rule()).not.toContain("U+2500");
  });

  it("等幅には ■ ○ ★ を含めない（CLI 出力の枠がずれる）／文章側だけ含める", () => {
    setSetting("cjkFont", CJK_FONT_AUTO);
    const [mono, prose] = rule().split("@font-face").filter(Boolean);
    expect(mono).toContain(CJK_FAMILY);
    expect(mono).not.toContain("U+25A0-25FF");
    expect(prose).toContain(CJK_FAMILY_PROSE);
    expect(prose).toContain("U+25A0-25FF");
    expect(prose).toContain("U+2600-26FF");
  });

  it("選んだフォントを先頭に、汎用の和文チェーンを後ろに積む", () => {
    setSetting("cjkFont", "Meiryo");
    const src = rule();
    expect(src.indexOf('local("Meiryo")')).toBeLessThan(src.indexOf('local("Noto Sans CJK JP")'));
    // 未インストールでも Mac/Linux で拾えるように、汎用側は必ず残る
    expect(src).toContain('local("Hiragino Kaku Gothic ProN")');
  });

  it("「欧文優先」では規則ごと外す（従来の見た目に戻る）", () => {
    setSetting("cjkFont", CJK_FONT_OFF);
    expect(document.getElementById("af-cjk-font")).toBeNull();
  });

  it("設定を変えても <style> は 1 つだけ", () => {
    setSetting("cjkFont", "Yu Gothic");
    setSetting("cjkFont", CJK_FONT_AUTO);
    applyCjkFont(getSettings());
    expect(document.querySelectorAll("#af-cjk-font").length).toBe(1);
  });
});

describe("フォントスタックの前置", () => {
  it("等幅（ビューア・diff・ミラー）は CJK_FAMILY が先頭", () => {
    expect(fontStack("JetBrains Mono").startsWith(`"${CJK_FAMILY}",`)).toBe(true);
    expect(fontStack("システム等幅").startsWith(`"${CJK_FAMILY}",`)).toBe(true);
  });

  it("ターミナルには前置しない（xterm は Ambiguous を 1 桁として数える）", () => {
    expect(termFontStack("Source Code Pro")).not.toContain(CJK_FAMILY);
    expect(termFontStack("システム等幅")).not.toContain(CJK_FAMILY);
  });

  it("チャットはゴシック側だけ前置し、セリフ・明朝には混ぜない", () => {
    expect(chatFontStack("システム")).toContain(CJK_FAMILY_PROSE);
    expect(chatFontStack("セリフ")).not.toContain(CJK_FAMILY);
    expect(readerFontStack("明朝")).not.toContain(CJK_FAMILY);
    expect(readerFontStack("ゴシック")).not.toContain(CJK_FAMILY);
  });
});
