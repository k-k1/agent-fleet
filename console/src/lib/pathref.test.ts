// pathRefCandidate is the filter that decides which inline-code token is worth a
// directory listing. These cases pin the two failure modes that matter: a shell snippet
// that must NOT cost a request, and a real path (Japanese names, a worktree "@" directory,
// a line suffix, a trailing slash) that must survive to the existence check.
import { describe, it, expect } from "vitest";
import { pathRefCandidate } from "./pathref.ts";

describe("pathRefCandidate", () => {
  it("accepts paths an agent actually cites", () => {
    expect(pathRefCandidate("docs/65-drawio-viewer.md")).toEqual({ ref: "docs/65-drawio-viewer.md" });
    expect(pathRefCandidate("94-freeze/辛口編集者レビュー/00_講評.md")).toEqual({
      ref: "94-freeze/辛口編集者レビュー/00_講評.md",
    });
    expect(pathRefCandidate("~/repos/agent-fleet@wip-sighxwi/AGENTS.md")).toEqual({
      ref: "~/repos/agent-fleet@wip-sighxwi/AGENTS.md",
    });
    expect(pathRefCandidate("/home/dev/repos/x/README.md")).toEqual({ ref: "/home/dev/repos/x/README.md" });
    expect(pathRefCandidate(".gitignore")).toEqual({ ref: ".gitignore" });
  });

  it("splits off the line:column an agent cites a source line with", () => {
    expect(pathRefCandidate("console/src/lib/filemeta.ts:73")).toEqual({
      ref: "console/src/lib/filemeta.ts",
      line: 73,
    });
    expect(pathRefCandidate("src/a.ts:12:5")).toEqual({ ref: "src/a.ts", line: 12, column: 5 });
    // Not a coordinate — a name that merely ends in digits keeps them.
    expect(pathRefCandidate("docs/65")).toEqual({ ref: "docs/65" });
  });

  it("drops a trailing slash so a directory resolves like any other path", () => {
    expect(pathRefCandidate("_act-parts/")).toEqual({ ref: "_act-parts" });
    expect(pathRefCandidate("docs/dev/")).toEqual({ ref: "docs/dev" });
  });

  it("trims surrounding whitespace but rejects a token with any inside", () => {
    expect(pathRefCandidate("  docs/README.md \n")).toEqual({ ref: "docs/README.md" });
    expect(pathRefCandidate("npm run build")).toBeNull();
    expect(pathRefCandidate("git -C ~/repos/x status")).toBeNull();
  });

  it("rejects commands and expressions that merely look path-ish", () => {
    expect(pathRefCandidate("rm -rf *.log")).toBeNull();
    expect(pathRefCandidate("src/**/*.ts")).toBeNull();
    expect(pathRefCandidate("--path=docs/a.md")).toBeNull();
    expect(pathRefCandidate("af_report(session=…)")).toBeNull();
    expect(pathRefCandidate("$HOME/.config")).toBeNull();
  });

  it("rejects bare words and version-shaped tokens (no slash, no real extension)", () => {
    expect(pathRefCandidate("develop")).toBeNull();
    expect(pathRefCandidate("MarkdownView")).toBeNull();
    expect(pathRefCandidate("1.2.3")).toBeNull();
    expect(pathRefCandidate("v0.14.2")).toBeNull();
  });

  it("rejects URLs, empties and tokens that name no file", () => {
    expect(pathRefCandidate("https://example.com/a.md")).toBeNull();
    expect(pathRefCandidate("//host/share/a.md")).toBeNull();
    expect(pathRefCandidate("")).toBeNull();
    expect(pathRefCandidate(null)).toBeNull();
    expect(pathRefCandidate("/")).toBeNull();
    expect(pathRefCandidate("../")).toBeNull();
    expect(pathRefCandidate("~")).toBeNull();
    expect(pathRefCandidate("~/")).toBeNull();
  });

  it("rejects an absurdly long token instead of resolving it", () => {
    expect(pathRefCandidate("a/".repeat(400) + "b.md")).toBeNull();
  });
});
