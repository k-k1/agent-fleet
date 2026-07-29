import { describe, it, expect } from "vitest";
import type { Repo } from "../repos/store.ts";
import type { Session } from "../../types/session.ts";
import { normQuery, repoMatches, sessionMatches } from "./filter.ts";

const repo = (name: string, extra: Partial<Repo> = {}): Repo => ({ name, ...extra });
const sess = (name: string, extra: Partial<Session> = {}): Session => ({ name, kind: "claude", ...extra });

describe("normQuery", () => {
  it("trims and lowercases; whitespace-only means no filter", () => {
    expect(normQuery("  Agent ")).toBe("agent");
    expect(normQuery("   ")).toBe("");
  });
});

describe("repoMatches", () => {
  it("matches name and current branch, case-insensitively", () => {
    expect(repoMatches(repo("agent-fleet"), "fleet")).toBe(true);
    expect(repoMatches(repo("agent-fleet", { branch: "Feat/TTS" }), "tts")).toBe(true);
    expect(repoMatches(repo("agent-fleet"), "zunda")).toBe(false);
  });
  it("matches the worktree slug embedded in the folder name and branch", () => {
    expect(repoMatches(repo("agent-fleet@wip-smdcx75", { branch: "temp/smdcx75" }), "smdcx75")).toBe(true);
  });
  it("matches the SVN URL", () => {
    expect(repoMatches(repo("lib", { vcs: "svn", url: "https://svn.example.com/proj/trunk" }), "trunk")).toBe(true);
  });
  it("empty query matches everything", () => {
    expect(repoMatches(repo("x"), "")).toBe(true);
  });
});

describe("sessionMatches", () => {
  it("matches the display name (title / label / repo fallback) and the id", () => {
    expect(sessionMatches(sess("s1", { title: "左ペイン改善" }), "左ペイン")).toBe(true);
    expect(sessionMatches(sess("s2", { label: "[AF] mirror fix" }), "mirror")).toBe(true);
    expect(sessionMatches(sess("s3", { repo: "agent-fleet" }), "agent")).toBe(true);
    expect(sessionMatches(sess("abc123", { title: "x" }), "abc12")).toBe(true);
    expect(sessionMatches(sess("s4", { title: "無関係" }), "fleet")).toBe(false);
  });
  it("matches branch / folder / dir even when a title hides them from the display name", () => {
    expect(sessionMatches(sess("s5", { title: "改善", branch: "temp/smdcx75" }), "smdcx75")).toBe(true);
    expect(sessionMatches(sess("s6", { title: "改善", branch: "temp/x", currentBranch: "feat/tts" }), "tts")).toBe(true);
    expect(sessionMatches(sess("s7", { title: "改善", repo: "agent-fleet@wip-abc" }), "wip-abc")).toBe(true);
    expect(sessionMatches(sess("s8", { title: "改善", dir: "/home/dev/notes" }), "notes")).toBe(true);
  });
  it("matches only the dir basename, not the whole path", () => {
    expect(sessionMatches(sess("s9", { title: "改善", dir: "/home/dev/repos/foo" }), "repos")).toBe(false);
  });
});
