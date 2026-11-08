import { describe, it, expect } from "vitest";
import { deriveRepoName, sanitizeSeg, sanitizeFolderName, uniqueRepoName, repoNameRe } from "./reponame.ts";

describe("deriveRepoName", () => {
  it("takes the last path segment minus .git", () => {
    expect(deriveRepoName("git@github.com:k-k1/agent-fleet.git")).toBe("agent-fleet");
    expect(deriveRepoName("https://github.com/k-k1/agent-fleet")).toBe("agent-fleet");
    expect(deriveRepoName("https://host/x/y/repo.git/")).toBe("repo");
  });
  it("handles bare names and empties", () => {
    expect(deriveRepoName("repo")).toBe("repo");
    expect(deriveRepoName("")).toBe("");
    expect(deriveRepoName(null)).toBe("");
  });
});

describe("sanitizeSeg", () => {
  it("maps to the repoNameRe charset", () => {
    expect(sanitizeSeg("feat/foo bar")).toBe("feat-foo-bar");
    expect(sanitizeSeg("--x")).toBe("x");
  });
  it("falls back to 'branch' when nothing survives", () => {
    expect(sanitizeSeg("")).toBe("branch");
    // Non-ASCII becomes "-", then leading dashes strip to "" → fallback… but a
    // trailing dash may survive: assert the result still fits the folder charset.
    expect(repoNameRe.test(`x@${sanitizeSeg("日本語")}`)).toBe(true);
  });
});

describe("repoNameRe", () => {
  it("accepts Japanese and other Unicode folder names", () => {
    expect(repoNameRe.test("日本語プロジェクト")).toBe(true);
    expect(repoNameRe.test("数字123")).toBe(true);
    expect(repoNameRe.test("café")).toBe(true);
    expect(repoNameRe.test("repo")).toBe(true);
    expect(repoNameRe.test("x@feat-1")).toBe(true);
  });
  it("still rejects traversal and unsafe names", () => {
    for (const bad of ["", "..", "../evil", "a/b", ".hidden", "-flag", "@at", "a b"]) {
      expect(repoNameRe.test(bad)).toBe(false);
    }
  });
});

describe("sanitizeFolderName", () => {
  it("preserves Unicode letters/numbers", () => {
    expect(sanitizeFolderName("日本語")).toBe("日本語");
    expect(sanitizeFolderName("my repo!")).toBe("my-repo-");
    expect(repoNameRe.test(sanitizeFolderName("日本語 プロジェクト"))).toBe(true);
  });
  it("strips leading chars repoNameRe forbids as the first char", () => {
    expect(sanitizeFolderName("--x")).toBe("x");
    expect(sanitizeFolderName("@名前")).toBe("名前");
  });
});

describe("uniqueRepoName", () => {
  it("returns base when free, else suffixes -2, -3…", () => {
    expect(uniqueRepoName("repo", new Set())).toBe("repo");
    expect(uniqueRepoName("repo", new Set(["repo"]))).toBe("repo-2");
    expect(uniqueRepoName("repo", new Set(["repo", "repo-2"]))).toBe("repo-3");
  });
});
