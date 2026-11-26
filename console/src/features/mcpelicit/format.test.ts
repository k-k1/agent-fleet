import { describe, expect, it } from "vitest";
import { indentJSON, optionTexts, visible } from "./format.ts";

describe("indentJSON", () => {
  it("lays the text out without changing a token", () => {
    const raw = `{"to":"a@example.com","to":"b@example.com","n":1.0,"big":12345678901234567890,"e":[],"o":{},"s":"a, b: {c}","q":"x\\"y","l":[1,{"k":null}]}`;
    const out = indentJSON(raw);
    expect(out).toBe(
      [
        "{",
        `  "to": "a@example.com",`,
        `  "to": "b@example.com",`,
        `  "n": 1.0,`,
        `  "big": 12345678901234567890,`,
        `  "e": [],`,
        `  "o": {},`,
        `  "s": "a, b: {c}",`,
        `  "q": "x\\"y",`,
        `  "l": [`,
        "    1,",
        "    {",
        `      "k": null`,
        "    }",
        "  ]",
        "}",
      ].join("\n"),
    );
    // Only layout was added: every token is still there, in order.
    expect(out.replace(/\s+/g, "")).toBe(raw.replace(/\s+/g, ""));
  });
  it("keeps white space inside strings", () => {
    expect(indentJSON(`{"body":"line 1\\n  two  spaces"}`)).toBe(`{\n  "body": "line 1\\n  two  spaces"\n}`);
  });
  it("returns text it cannot follow as it is", () => {
    for (const raw of [`{"a":1`, `{"a":1}}`, `{"a":"open`]) expect(indentJSON(raw)).toBe(raw);
  });
  // Each level indents every line inside it: deep nesting would otherwise grow without bound.
  it("stays bounded on deeply nested text", () => {
    const deep = "[".repeat(100_000) + "1" + "]".repeat(100_000);
    const out = indentJSON(deep);
    expect(out).toBe(deep);
    const mid = "[".repeat(40) + "1" + "]".repeat(40);
    expect(indentJSON(mid).length).toBeLessThan(mid.length * 4 + 4096);
  });
  it("writes a hidden character inside a string as its escape", () => {
    // The template literal holds the characters themselves; the result holds their escapes.
    expect(indentJSON(`{"f":"a\u202Eb\u200Bc"}`)).toBe(`{\n  "f": "a\\u202Eb\\u200Bc"\n}`);
  });
  it("marks hidden characters in text it returns as it is", () => {
    expect(indentJSON(`{"a":"\u202E`)).toBe(`{"a":"⟨U+202E⟩`);
  });
});

describe("optionTexts", () => {
  const field = (options: { value: string; label: string }[]) => ({ name: "f", options });
  it("shows the labels alone when they differ", () => {
    expect(optionTexts(field([{ value: "A", label: "Allow" }, { value: "D", label: "Deny" }]))).toEqual(["Allow", "Deny"]);
  });
  // #5282: the relay cuts labels at 256 bytes, so two options can read the same.
  it("adds the value to labels that collide", () => {
    expect(
      optionTexts(
        field([
          { value: "ALLOW_ONCE", label: "Allow…" },
          { value: "ALLOW_ALWAYS", label: "Allow…" },
          { value: "DENY", label: "Deny" },
        ]),
      ),
    ).toEqual(["Allow… (ALLOW_ONCE)", "Allow… (ALLOW_ALWAYS)", "Deny"]);
  });
  // A compatibility form reads the same and collides; a hidden character is shown as a marker.
  it("counts labels that read the same as colliding", () => {
    expect(
      optionTexts(
        field([
          { value: "A", label: "Allow" },
          { value: "B", label: "Allow\u200B " },
          { value: "C", label: "\uFF21\uFF4C\uFF4C\uFF4F\uFF57" },
        ]),
      ),
    ).toEqual(["Allow (A)", "Allow⟨U+200B⟩ ", "\uFF21\uFF4C\uFF4C\uFF4F\uFF57 (C)"]);
  });
  // A label can spell another option's label and value: the texts are checked once more as shown.
  it("numbers the options when a label spells another's label and value", () => {
    expect(
      optionTexts(
        field([
          { value: "A", label: "Allow" },
          { value: "D", label: "Allow" },
          { value: "OTHER", label: "Allow (A)" },
        ]),
      ),
    ).toEqual(["1. Allow (A)", "2. Allow (D)", "3. Allow (A)"]);
  });
  it("marks hidden characters in the values it adds", () => {
    expect(optionTexts(field([{ value: "A", label: "X" }, { value: "A\u200B", label: "X" }]))).toEqual(["X (A)", "X (A⟨U+200B⟩)"]);
  });
  it("shows the value for an option with no label", () => {
    expect(optionTexts(field([{ value: "A", label: " " }, { value: "B", label: "Bee" }]))).toEqual(["A", "Bee"]);
  });
});

describe("visible", () => {
  it("marks direction controls and other hidden characters, and leaves right-to-left letters alone", () => {
    expect(visible("a\u202Eb\u2066c\uFEFF")).toBe("a⟨U+202E⟩b⟨U+2066⟩c⟨U+FEFF⟩");
    expect(visible("שלום abc")).toBe("שלום abc");
  });
});
