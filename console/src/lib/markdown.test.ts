import { describe, expect, it } from "vitest";
import { splitYamlFrontMatter } from "./markdown.ts";

describe("splitYamlFrontMatter", () => {
  it("extracts a leading YAML front matter block", () => {
    expect(splitYamlFrontMatter("---\ntitle: Example\ntags:\n  - docs\n---\n\n# Body")).toEqual({
      attributes: { title: "Example", tags: ["docs"] },
      body: "\n# Body",
    });
  });

  it("supports a YAML end marker and CRLF", () => {
    expect(splitYamlFrontMatter("\uFEFF---\r\ntitle: Example\r\n...\r\n# Body")).toEqual({
      attributes: { title: "Example" },
      body: "# Body",
    });
  });

  it("ignores non-leading, incomplete, and invalid blocks", () => {
    expect(splitYamlFrontMatter("# Body\n\n---\ntitle: Example\n---")).toBeNull();
    expect(splitYamlFrontMatter("---\ntitle: Example\n# Body")).toBeNull();
    expect(splitYamlFrontMatter("---\ntitle: [\n---\n# Body")).toBeNull();
  });
});
