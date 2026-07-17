import { describe, expect, it } from "vitest";
import { resolveMarkdownFileTarget } from "./filemeta.ts";

describe("resolveMarkdownFileTarget", () => {
  it("resolves an agent absolute source citation under home", () => {
    expect(resolveMarkdownFileTarget("/home/dev/repos/app/src/main.ts:635", "", "/home/dev/repos/other")).toEqual({
      path: "repos/app/src/main.ts",
      line: 635,
    });
  });

  it("resolves line and column suffixes", () => {
    expect(resolveMarkdownFileTarget("~/repos/app/main.ts:12:7")).toEqual({
      path: "repos/app/main.ts",
      line: 12,
      column: 7,
    });
  });

  it("resolves GitHub line fragments", () => {
    expect(resolveMarkdownFileTarget("repos/app/main.ts#L9C3", "", "repos/other")).toEqual({
      path: "repos/app/main.ts",
      line: 9,
      column: 3,
    });
  });

  it("resolves relative links from a turn cwd", () => {
    expect(resolveMarkdownFileTarget("src/main.ts:4", "", "/home/dev/repos/app")).toEqual({
      path: "repos/app/src/main.ts",
      line: 4,
    });
  });

  it("keeps document-root links within the repository", () => {
    expect(resolveMarkdownFileTarget("/docs/guide.md", "repos/app/README.md")).toEqual({
      path: "repos/app/docs/guide.md",
    });
  });

  it("keeps an allowed absolute document root when following a relative link", () => {
    expect(resolveMarkdownFileTarget(
      "06-agents.md",
      "/usr/local/share/agent-fleet/docs/guide/member/README.md",
    )).toEqual({
      path: "/usr/local/share/agent-fleet/docs/guide/member/06-agents.md",
    });
  });

  it("decodes Japanese paths and ignores external URLs", () => {
    expect(resolveMarkdownFileTarget("docs/%E8%A8%AD%E8%A8%88.md:20", "", "repos/app")).toEqual({
      path: "repos/app/docs/設計.md",
      line: 20,
    });
    expect(resolveMarkdownFileTarget("https://example.com:443/a.ts:8")).toBeNull();
  });
});
