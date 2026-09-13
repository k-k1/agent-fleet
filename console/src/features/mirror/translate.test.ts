import { describe, expect, it } from "vitest";
import { looksForeign, targetLang, translatableTexts, translateHash, turnTranslateKey } from "./translate.ts";
import type { Group } from "./transcript/types.ts";

const groupOf = (parts: Group["parts"]): Pick<Group, "parts"> => ({ parts });

describe("translateHash", () => {
  // The cache key lives on both sides of the wire, so these vectors are the contract with
  // workspace/agent/session_translate.go (TestTranslateHashVectors holds the same table).
  // The Japanese and emoji rows are the ones that matter: hashing UTF-16 code units instead of
  // UTF-8 bytes passes every ASCII case and then keys a different entry for every real answer.
  it("matches the Go implementation's vectors", () => {
    expect(translateHash("")).toBe("811c9dc51f2be47c");
    expect(translateHash("hello")).toBe("4f9f2cabc425dbfc");
    expect(translateHash("こんにちは")).toBe("1cfa9ccd6dcd6ab2");
    expect(translateHash("🙂")).toBe("57a37a4beec1d01e");
    expect(translateHash("# Heading\n\nbody\n")).toBe("0f79ab9eda6468f3");
  });

  it("separates inputs that differ", () => {
    expect(translateHash("a")).not.toBe(translateHash("b"));
    expect(translateHash("hello ")).not.toBe(translateHash("hello"));
  });
});

describe("turnTranslateKey", () => {
  it("is derived from the prose, so it survives a block index shift", () => {
    expect(turnTranslateKey(["one", "two"])).toBe(translateHash("one\n\ntwo"));
    expect(turnTranslateKey([])).toBe("");
  });
});

describe("translatableTexts", () => {
  it("takes prose parts only, in order", () => {
    const g = groupOf([
      { kind: "text", text: "first" },
      { kind: "tool", tool: "Read", text: "not prose" },
      { kind: "plan", plan: "a plan" },
      { kind: "text", text: "  " },
      { kind: "text", text: "second" },
    ]);
    expect(translatableTexts(g)).toEqual(["first", "second"]);
  });
});

describe("targetLang", () => {
  it("prefers a fixed answer language, else the display language", () => {
    expect(targetLang("en", "ja")).toBe("en");
    expect(targetLang("ja", "en")).toBe("ja");
    expect(targetLang("auto", "en")).toBe("en");
    expect(targetLang("auto", "ja")).toBe("ja");
    expect(targetLang("", "")).toBe("ja");
  });
});

describe("looksForeign", () => {
  it("offers English prose to a Japanese reader", () => {
    expect(looksForeign("Done — the tests are green and the branch is pushed.", "ja")).toBe(true);
  });

  it("leaves an answer that already has Japanese in it alone", () => {
    // Mixed is readable, and translating it would spend tokens to re-say what is there.
    expect(looksForeign("実装完了。see the diff for details", "ja")).toBe(false);
    expect(looksForeign("テストは全部緑です。", "ja")).toBe(false);
  });

  it("does not offer a one-word answer", () => {
    expect(looksForeign("Done.", "ja")).toBe(false);
    expect(looksForeign("OK", "ja")).toBe(false);
  });

  it("judges the prose, not the code in it", () => {
    // An English answer whose code happens to contain Japanese strings is still English prose;
    // a Japanese answer that is mostly a code block is still Japanese.
    const jaInsideCode = "Here is what changed:\n\n```go\ns := \"日本語のテストデータです\"\n```\n";
    expect(looksForeign(jaInsideCode, "ja")).toBe(true);
    const jaProseWithCode = "変更点はこれです。\n\n```go\nfmt.Println(\"hello world, this is a long line\")\n```\n";
    expect(looksForeign(jaProseWithCode, "en")).toBe(true);
  });

  it("offers Japanese prose to an English reader", () => {
    expect(looksForeign("テストは全部緑になりました。ブランチも push 済みです。", "en")).toBe(true);
    expect(looksForeign("Done. The tests are green.", "en")).toBe(false);
  });
});
