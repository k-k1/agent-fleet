import { describe, expect, it } from "vitest";
import { previewBody } from "./optionPreview.ts";

describe("previewBody", () => {
  it("keeps an ASCII mockup byte-for-byte", () => {
    const art = "┌───┬───┐\n│ a │ b │\n└───┴───┘";
    expect(previewBody(art)).toBe(art);
  });

  it("keeps leading indentation inside a fenced snippet", () => {
    expect(previewBody('```ts\nif (x) {\n  return "y";\n}\n```')).toBe('if (x) {\n  return "y";\n}');
  });

  it("unwraps a fence that wraps the whole preview", () => {
    expect(previewBody("```\n# .gitmodules\nbranch = release/3.0.3\n```")).toBe("# .gitmodules\nbranch = release/3.0.3");
  });

  it("leaves several fences alone — they separate variants", () => {
    const two = "```\nA\n```\n\n```\nB\n```";
    expect(previewBody(two)).toBe(two);
  });

  it("is empty for a missing or blank preview", () => {
    expect(previewBody(undefined)).toBe("");
    expect(previewBody("   \n ")).toBe("");
  });
});
