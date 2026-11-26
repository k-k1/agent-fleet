import { describe, expect, it } from "vitest";
import { indentJSON, optionTexts } from "./format.ts";

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
  it("counts labels that differ only in what does not show as colliding", () => {
    expect(
      optionTexts(
        field([
          { value: "A", label: "Allow" },
          { value: "B", label: "Allow\u200B " },
          { value: "C", label: "\uFF21\uFF4C\uFF4C\uFF4F\uFF57" },
        ]),
      ),
    ).toEqual(["Allow (A)", "Allow\u200B  (B)", "\uFF21\uFF4C\uFF4C\uFF4F\uFF57 (C)"]);
  });
  it("shows the value for an option with no label", () => {
    expect(optionTexts(field([{ value: "A", label: " " }, { value: "B", label: "Bee" }]))).toEqual(["A", "Bee"]);
  });
});
