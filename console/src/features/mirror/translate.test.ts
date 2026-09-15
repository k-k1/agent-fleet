import { describe, expect, it } from "vitest";
import {
  looksForeign,
  splitForTranslate,
  targetLang,
  translatableTexts,
  translateHash,
  turnTranslateKey,
} from "./translate.ts";
import type { Group } from "./transcript/types.ts";

const encoder = new TextEncoder();
const byteLen = (s: string): number => encoder.encode(s).length;

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

  it("still offers an English answer that names a Japanese UI label", () => {
    // Real shape (session sb4ghpu): a long English report naming Console labels that have no
    // English name. Eight CJK characters in ~350 Latin letters is a term being pointed at, not
    // an answer the Japanese reader can read — with "any CJK at all" as the rule, the button
    // was withheld from exactly the answers that need it.
    const englishReportNamingJaLabels =
      "Left to you, and it is the one thing not yet verified: the engines are still 無効 in " +
      "Console. Please set llm and image back to オンデマンド, and if you want the purchase path " +
      "proven on this account, run one image generation and I will watch the fleet calls and " +
      "the box it buys from the AWS side.";
    expect(looksForeign(englishReportNamingJaLabels, "ja")).toBe(true);
    // Same text to an English reader: still English prose, so no translation is offered.
    expect(looksForeign(englishReportNamingJaLabels, "en")).toBe(false);
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

  it("does not let a quoted label alone flip the verdict", () => {
    // An English report that cites a UI label next to its Japanese original is still English
    // prose — the quoted characters are a citation, not the reader's own language showing up.
    const englishReportCitingJa =
      'The button label changed from "wi.detail_start_head_pr" ("レビューする"/"Review this ' +
      'pull request") to a new key, and the rest of this report is written in English throughout.';
    expect(looksForeign(englishReportCitingJa, "ja")).toBe(true);

    // Same shape the other way: a Japanese report citing an English label in 「」 stays Japanese.
    const japaneseReportCitingEn =
      "ボタンの表示は「wi.detail_start_head_pr」から新しいラベルに変わりました。この報告の残りは" +
      "すべて日本語で書かれています。テストも一式通っています。";
    expect(looksForeign(japaneseReportCitingEn, "en")).toBe(true);

    // Nothing left to translate once the quoted label is the whole message.
    expect(looksForeign('"レビューする"', "ja")).toBe(false);
  });
});

describe("splitForTranslate", () => {
  it("leaves a part under the cap alone", () => {
    expect(splitForTranslate("Short answer.", 100)).toEqual(["Short answer."]);
  });

  it("splits an over-cap part at paragraph breaks, staying under the cap", () => {
    const paragraphs = ["First paragraph, long enough on its own.", "Second paragraph, also long enough on its own."];
    const text = paragraphs.join("\n\n");
    const cap = byteLen(paragraphs[0]) + 5; // room for the paragraph itself plus a little slack, not both
    const chunks = splitForTranslate(text, cap);
    expect(chunks.length).toBeGreaterThan(1);
    for (const c of chunks) expect(byteLen(c)).toBeLessThanOrEqual(cap);
    // No text lost or reordered: concatenating the chunks reproduces the original exactly.
    expect(chunks.join("")).toBe(text);
  });

  it("falls back to line breaks when a single paragraph alone is over the cap", () => {
    const lines = ["line one is fairly long", "line two is fairly long", "line three is fairly long"];
    const text = lines.join("\n"); // one paragraph (no blank line), so no paragraph break exists
    const cap = byteLen(lines[0]) + 5;
    const chunks = splitForTranslate(text, cap);
    expect(chunks.length).toBeGreaterThan(1);
    for (const c of chunks) expect(byteLen(c)).toBeLessThanOrEqual(cap);
    expect(chunks.join("")).toBe(text);
  });

  it("falls back to sentence boundaries when a line has no newline at all", () => {
    const sentences = [
      "This is the first sentence in one long unbroken line",
      "here is the second one",
      "and a third clause follows",
      "finally the last sentence ends it",
    ];
    const text = sentences.join(". ") + "."; // one line, no newlines anywhere
    const cap = byteLen(sentences[0]) + 5;
    const chunks = splitForTranslate(text, cap);
    expect(chunks.length).toBeGreaterThan(1);
    expect(chunks.join("")).toBe(text);
    // Cuts land right after ". ", not mid-word: every chunk but the last ends with ". ".
    for (const c of chunks.slice(0, -1)) expect(c.endsWith(". ")).toBe(true);
  });

  it("hard-splits at a byte-safe boundary when even one line has no break", () => {
    const text = "a".repeat(50) + "あ".repeat(50); // a run with no newline anywhere, incl. multibyte
    const cap = 30;
    const chunks = splitForTranslate(text, cap);
    expect(chunks.length).toBeGreaterThan(1);
    for (const c of chunks) expect(byteLen(c)).toBeLessThanOrEqual(cap);
    // Every cut lands on a whole character: re-encoding never throws and nothing is lost.
    expect(chunks.join("")).toBe(text);
  });

  it("keeps a fenced code block whole when it fits in one chunk", () => {
    const before = "Explanation before the code, long enough to need a split on its own here.";
    const fence = "```go\nfunc main() {\n\tfmt.Println(\"hi\")\n}\n```";
    const after = "Explanation after the code, also long enough to need a split on its own here.";
    const text = [before, fence, after].join("\n\n");
    const cap = byteLen(before) + 5; // forces a split near the fence, not inside it
    const chunks = splitForTranslate(text, cap);
    expect(chunks.join("")).toBe(text);
    // The fence never straddles a chunk boundary: each chunk holds an even number of ``` markers.
    for (const c of chunks) expect((c.match(/```/g) || []).length % 2).toBe(0);
  });

  it("never lands a hard cut inside a surrogate pair (an emoji)", () => {
    // No newlines anywhere, so this can only be resolved by the byte-boundary fallback — and the
    // cap is picked to fall exactly between the emoji's two UTF-16 code units.
    const text = "a".repeat(20) + "🔴" + "b".repeat(20);
    const cap = byteLen("a".repeat(20)) + 3;
    const chunks = splitForTranslate(text, cap);
    expect(chunks.join("")).toBe(text);
    // A cut between the two halves leaves a lone surrogate at each side. JS string equality
    // tolerates that (it is still 1:1 in code units), but the actual bytes a fetch body sends
    // do not: a real UTF-8 encode turns each lone half into its OWN U+FFFD, destroying the
    // character into two replacement characters instead of reassembling it. Round-tripping each
    // chunk through the same UTF-8 encode/decode the network does catches that even though the
    // chunks' JS-level concatenation above already looked fine.
    for (const c of chunks) expect(new TextDecoder().decode(encoder.encode(c))).toBe(c);
    expect(chunks.some((c) => c.includes("🔴"))).toBe(true);
  });
});
