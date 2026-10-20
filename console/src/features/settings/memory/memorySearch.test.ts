// What the Memories view and the command palette share (#2407): which listed memories a query
// finds and in what order, and the round trip of a memory's name through a settings target.
import { describe, it, expect } from "vitest";
import { keyOf, matchMemories, memoryTarget, parseMemoryTarget, scopeKey } from "./memorySearch.ts";
import type { MemoryListed, MemorySearchHit } from "./memoryTypes.ts";

const proj = { id: "app-1", root: "/r/app", vcs: "git", display: "app" };
const mem = (name: string, extra: Partial<MemoryListed> = {}): MemoryListed => ({
  name,
  scope: "user",
  description: "",
  updated: "2026-10-01T00:00:00Z",
  pinned: false,
  uses: 0,
  revision: 1,
  authorKind: "claude",
  authorSession: "s1",
  ...extra,
});
const hit = (m: MemoryListed): MemorySearchHit => ({ ...m, snippets: ["line"] });

describe("matchMemories", () => {
  const a = mem("alpha", { description: "build steps" });
  const b = mem("beta", { scope: "project", project: proj });
  const c = mem("gamma", { type: "feedback" });
  const rows = [a, b, c];

  it("returns every row for an empty query", () => {
    expect(matchMemories(rows, [], "")).toEqual(rows);
  });
  it("matches name, description, type and author", () => {
    expect(matchMemories(rows, [], "build").map((m) => m.name)).toEqual(["alpha"]);
    expect(matchMemories(rows, [], "feedback").map((m) => m.name)).toEqual(["gamma"]);
    expect(matchMemories(rows, [], "s1")).toHaveLength(3);
  });
  it("lists field matches as listed, then body-only hits in rank order, each once", () => {
    // "zzz" matches no field; the body search found gamma, then alpha, then alpha again.
    expect(matchMemories(rows, [hit(c), hit(a), hit(a)], "zzz").map((m) => m.name)).toEqual(["gamma", "alpha"]);
    // alpha matches by field AND is a hit: it stays first and is not repeated.
    expect(matchMemories(rows, [hit(c), hit(a)], "build").map((m) => m.name)).toEqual(["alpha", "gamma"]);
  });
  it("drops a hit that is not among the rows", () => {
    expect(matchMemories([a], [hit(b)], "zzz")).toEqual([]);
  });
  it("tells a user memory from a project one of the same name", () => {
    const same = mem("alpha", { scope: "project", project: proj });
    expect(keyOf(a)).not.toBe(keyOf(same));
    expect(scopeKey(same)).toBe("project:app-1");
    expect(matchMemories([a, same], [hit(same)], "zzz")).toEqual([same]);
  });
});

describe("memory target", () => {
  it("round-trips a user and a project memory, whatever the name holds", () => {
    expect(parseMemoryTarget(memoryTarget(mem("a:b/c")))).toEqual({ scope: "user", project: "", name: "a:b/c" });
    expect(parseMemoryTarget(memoryTarget(mem("x", { scope: "project", project: proj })))).toEqual({
      scope: "project",
      project: "app-1",
      name: "x",
    });
  });
  it("is not taken for another section's target, nor for a broken one", () => {
    expect(parseMemoryTarget("codex")).toBeNull();
    expect(parseMemoryTarget(null)).toBeNull();
    expect(parseMemoryTarget("memory-entry:{")).toBeNull();
    expect(parseMemoryTarget('memory-entry:{"scope":"x","project":"","name":"n"}')).toBeNull();
  });
});
