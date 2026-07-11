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
});
