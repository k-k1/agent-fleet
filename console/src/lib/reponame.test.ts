import { describe, it, expect } from "vitest";
import {
  deriveRepoName,
  sanitizeSeg,
  deriveBranchName,
  uniqueRepoName,
  repoNameRe,
} from "./reponame.ts";

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

describe("deriveBranchName", () => {
  it("slugs leading ASCII words", () => {
    expect(deriveBranchName("Fix the login redirect loop")).toBe("fix-the-login-redirect-loop");
  });
  it("returns '' for non-ASCII-only prompts (caller falls back to wip-<slug>)", () => {
    expect(deriveBranchName("ログインを直す")).toBe("");
  });
  it("caps length at 40", () => {
    expect(deriveBranchName("a".repeat(80)).length).toBeLessThanOrEqual(40);
  });
});

describe("uniqueRepoName", () => {
  it("returns base when free, else suffixes -2, -3…", () => {
    expect(uniqueRepoName("repo", new Set())).toBe("repo");
    expect(uniqueRepoName("repo", new Set(["repo"]))).toBe("repo-2");
    expect(uniqueRepoName("repo", new Set(["repo", "repo-2"]))).toBe("repo-3");
  });
});
