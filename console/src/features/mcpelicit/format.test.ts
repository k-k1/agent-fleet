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
  // Text the layout gives up on still shows its hidden characters as escapes, never as a marker a
  // string could spell out itself: the two arguments below reach the server as different values.
  it("keeps escapes in text it does not lay out", () => {
    const deep = (s: string) => "[".repeat(5000) + `"${s}"` + "]".repeat(5000);
    const real = indentJSON(deep("\u202E"));
    const spelled = indentJSON(deep("⟨U+202E⟩"));
    expect(real).toContain(`"\\u202E"`);
    expect(spelled).toContain(`"⟨U+202E⟩"`);
    expect(real).not.toBe(spelled);
    expect(indentJSON(`{"a":"\u202E`)).toBe(`{"a":"\\u202E`);
  });
  it("writes a supplementary-plane hidden character as a surrogate pair of escapes", () => {
    expect(indentJSON(`["a\u{E0061}"]`)).toBe(`[\n  "a\\uDB40\\uDC61"\n]`);
  });
  // Escapes grow text up to six times; the layout's budget is taken on the escaped text, so the
  // output stays within a fixed multiple of it.
  it("stays bounded on text full of hidden characters", () => {
    const raw = `["${"\u200B".repeat(50_000)}"]`;
    expect(indentJSON(raw).length).toBeLessThan(raw.length * 6 * 4 + 4096);
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
  it("tells apart labels that differ by a tag character", () => {
    expect(optionTexts(field([{ value: "A", label: "Same" }, { value: "B", label: "Same\u{E0061}" }]))).toEqual([
      "Same",
      "Same⟨U+E0061⟩",
    ]);
  });
  it("quotes a value that would not show", () => {
    expect(optionTexts(field([{ value: "", label: "" }, { value: "B", label: "Bee" }]))).toEqual([`""`, "Bee"]);
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
