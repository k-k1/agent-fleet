// 作業グループ (docs/52) — membership predicates + the normalize fail-safe.
// Pure derivation only: the settings-backed mutations are thin setSetting wrappers.
import { describe, expect, it } from "vitest";
import {
  normalizeWorkingSets,
  folderBase,
  repoBaseName,
  repoInSet,
  sessionInSet,
  convInSet,
} from "./workingSets.ts";
import type { WorkingSet } from "./workingSets.ts";
import type { Session } from "../types/session.ts";

const set = (over: Partial<WorkingSet> = {}): WorkingSet => ({
  id: "w111111",
  name: "案件X",
  repos: [],
  convs: [],
  sessions: [],
  ...over,
});

const sess = (over: Partial<Session>): Session => ({ name: "s111111", kind: "claude", ...over }) as Session;

describe("normalizeWorkingSets", () => {
  it("drops non-arrays and malformed entries instead of throwing", () => {
    expect(normalizeWorkingSets(undefined)).toEqual([]);
    expect(normalizeWorkingSets("x")).toEqual([]);
    expect(normalizeWorkingSets({})).toEqual([]);
    expect(
      normalizeWorkingSets([
        null,
        "junk",
        { name: "no id" },
        { id: "", name: "empty id" },
        { id: "wabcdef", name: "ok" },
      ]),
    ).toEqual([{ id: "wabcdef", name: "ok", repos: [], convs: [], sessions: [] }]);
  });

  it("keeps only string members inside the id arrays", () => {
    const [w] = normalizeWorkingSets([
      { id: "wabcdef", name: "n", repos: ["a", 1, null], convs: "junk", sessions: ["s1"] },
    ]);
    expect(w.repos).toEqual(["a"]);
    expect(w.convs).toEqual([]);
    expect(w.sessions).toEqual(["s1"]);
  });
});

describe("folderBase / repoBaseName", () => {
  it("resolves the base part of a worktree folder", () => {
    expect(folderBase("app@slug")).toBe("app");
    expect(folderBase("app")).toBe("app");
    expect(folderBase("")).toBe("");
  });

  it("a worktree belongs by its parent; an orphan worktree by its folder prefix", () => {
    expect(repoBaseName({ name: "app", worktree: false })).toBe("app");
    expect(repoBaseName({ name: "app@x", worktree: true, parent: "app" })).toBe("app");
    // parent record gone → the "<base>@" prefix of the folder still resolves
    expect(repoBaseName({ name: "app@x", worktree: true })).toBe("app");
  });
});

describe("repoInSet", () => {
  const w = set({ repos: ["app"] });
  it("matches the base clone and lets its worktrees follow", () => {
    expect(repoInSet(w, { name: "app", worktree: false })).toBe(true);
    expect(repoInSet(w, { name: "app@t1", worktree: true, parent: "app" })).toBe(true);
    expect(repoInSet(w, { name: "other", worktree: false })).toBe(false);
  });
});

describe("sessionInSet", () => {
  const w = set({ repos: ["app"], sessions: ["sdirect"] });
  it("inherits membership from the working copy (base and worktree folders)", () => {
    expect(sessionInSet(w, sess({ repo: "app" }))).toBe(true);
    // worktree session: repo = "<base>@<slug>" resolves via the base prefix —
    // this also covers a session whose repo record was deleted.
    expect(sessionInSet(w, sess({ repo: "app@t1" }))).toBe(true);
    expect(sessionInSet(w, sess({ repo: "other" }))).toBe(false);
  });
  it("falls back to the working dir's basename like sessionFolder does", () => {
    expect(sessionInSet(w, sess({ dir: "/home/dev/repos/app@t2" }))).toBe(true);
  });
  it("matches direct (repo-less) assignment by session name", () => {
    expect(sessionInSet(w, sess({ name: "sdirect" }))).toBe(true);
    expect(sessionInSet(w, sess({ name: "sother" }))).toBe(false);
  });
});

describe("convInSet", () => {
  it("matches by conversation id only", () => {
    const w = set({ convs: ["c-1"] });
    expect(convInSet(w, "c-1")).toBe(true);
    expect(convInSet(w, "c-2")).toBe(false);
  });
});
