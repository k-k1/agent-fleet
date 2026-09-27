import { describe, expect, it } from "vitest";
import { clipboardText } from "./pdfText.ts";

const same = (s: string) => s;

describe("clipboardText", () => {
  it("folds Kangxi radicals to the ideographs they stand for", () => {
    expect(clipboardText("\u2f47本語の\u2f92出し", same)).toBe("日本語の見出し");
  });

  it("keeps full-width punctuation, digits and half-width kana as they are", () => {
    expect(clipboardText("ページ １：ｶﾅ", same)).toBe("ページ １：ｶﾅ");
  });

  it("drops NULs and applies the given normaliser first", () => {
    expect(clipboardText("a\0bﬁ", (s) => s.replace("ﬁ", "fi"))).toBe("abfi");
  });
});
