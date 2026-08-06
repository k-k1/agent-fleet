import { describe, expect, it } from "vitest";
import { repairFullwidthTables, splitYamlFrontMatter } from "./markdown.ts";

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

describe("repairFullwidthTables", () => {
  it("rewrites a table written entirely with fullwidth pipes", () => {
    const repair = repairFullwidthTables("｜章｜点｜\n｜---｜---｜\n｜A1｜6.5｜\n");
    expect(repair?.body).toBe("|章|点|\n|---|---|\n|A1|6.5|\n");
    expect(repair).toMatchObject({ repaired: [0], total: 1 });
  });

  it("repairs when only the delimiter row is ASCII — it carries no text to judge by", () => {
    expect(repairFullwidthTables("｜章｜点｜\n|---|---|\n｜A1｜6.5｜")?.body).toBe("|章|点|\n|---|---|\n|A1|6.5|");
  });

  it("repairs a half-converted table where only the header row was left fullwidth", () => {
    expect(repairFullwidthTables("｜章｜点｜\n|---|---|\n| A1 | 6.5 |\n| A2 | 7 |")?.body).toBe(
      "|章|点|\n|---|---|\n| A1 | 6.5 |\n| A2 | 7 |",
    );
  });

  it("supplies a missing delimiter row once enough rows agree on a column count", () => {
    expect(repairFullwidthTables("｜章｜点｜\n｜A1｜6｜\n｜A2｜7｜\n｜A3｜8｜")?.body).toBe(
      "|章|点|\n|---|---|\n|A1|6|\n|A2|7|\n|A3|8|",
    );
    // Two rows are as easily a coincidence as a table — left as written.
    expect(repairFullwidthTables("｜章｜点｜\n｜A1｜6｜")).toBeNull();
  });

  it("leaves a fullwidth pipe that is cell content of a working table", () => {
    // The only way to put a vertical bar in a cell without splitting it, so the ｜ here
    // is deliberate — rewriting it would break a table that renders fine today.
    expect(repairFullwidthTables("| status | `pending｜failed` |\n|---|---|\n| a | b |")).toBeNull();
  });

  it("leaves prose and fenced code alone", () => {
    expect(repairFullwidthTables("- 集中度：A1 高｜A2 低｜A3 高")).toBeNull();
    expect(repairFullwidthTables("```\n｜章｜点｜\n｜---｜---｜\n｜A1｜6｜\n```")).toBeNull();
    expect(repairFullwidthTables("    ｜章｜点｜\n    ｜---｜---｜\n    ｜A1｜6｜")).toBeNull();
  });

  it("counts every table so a caller can line the indexes up with rendered tables", () => {
    const repair = repairFullwidthTables("| a | b |\n|---|---|\n| 1 | 2 |\n\n｜c｜d｜\n｜---｜---｜\n｜3｜4｜");
    expect(repair).toMatchObject({ repaired: [1], total: 2 });
  });

  it("returns null for a document with no fullwidth pipe at all", () => {
    expect(repairFullwidthTables("| a | b |\n|---|---|\n| 1 | 2 |")).toBeNull();
  });
});
