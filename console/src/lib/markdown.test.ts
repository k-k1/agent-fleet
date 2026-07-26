import { describe, expect, it } from "vitest";
import { withoutYamlFrontMatter } from "./markdown.ts";

describe("withoutYamlFrontMatter", () => {
  it("removes a leading YAML front matter block", () => {
    expect(withoutYamlFrontMatter("---\ntitle: Example\ntags:\n  - docs\n---\n\n# Body")).toBe("\n# Body");
  });

  it("supports a YAML end marker and CRLF", () => {
    expect(withoutYamlFrontMatter("\uFEFF---\r\ntitle: Example\r\n...\r\n# Body")).toBe("# Body");
  });

  it("keeps non-leading and incomplete delimiters as Markdown", () => {
    expect(withoutYamlFrontMatter("# Body\n\n---\ntitle: Example\n---")).toBe("# Body\n\n---\ntitle: Example\n---");
    expect(withoutYamlFrontMatter("---\ntitle: Example\n# Body")).toBe("---\ntitle: Example\n# Body");
  });
});
