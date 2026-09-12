import { describe, expect, it } from "vitest";
import { aliveCount, elapsedShort, orderByFamily, overviewGroups } from "./overview.ts";
import type { Repo } from "../repos/store.ts";
import type { WorkingSet } from "../../lib/workingSets.ts";
import type { Session } from "../../types/session.ts";

const s = (name: string, over: Partial<Session> = {}): Session => ({ name, kind: "claude", alive: true, state: "idle", ...over });
const set = (over: Partial<WorkingSet>): WorkingSet => ({ id: "w1", name: "A", repos: [], convs: [], sessions: [], schedules: [], ...over });
const repo = (name: string, over: Partial<Repo> = {}): Repo => ({ name, ...over });
const names = (g: { sessions: Session[] }[]): string[][] => g.map((x) => x.sessions.map((y) => y.name));

describe("overviewGroups", () => {
  it("shows only running sessions unless stopped ones are asked for", () => {
    const list = [s("a"), s("b", { alive: false }), s("c")];
    expect(names(overviewGroups(list, [], null, false))).toEqual([["a", "c"]]);
    expect(names(overviewGroups(list, [], null, true))).toEqual([["a", "c", "b"]]);
  });

  it("follows the active working set, by direct membership or by the session's repo", () => {
    const list = [s("a", { repo: "proj" }), s("b", { repo: "other" }), s("c")];
    const w = set({ repos: ["proj"], sessions: ["c"] });
    expect(overviewGroups(list, [], w, false).flatMap((g) => g.sessions.map((x) => x.name)).sort()).toEqual(["a", "c"]);
  });

  it("puts every working copy of one repository under one heading — base, worktree and a second clone", () => {
    const repos = [
      repo("app", { remote: "github.com", remotePath: "acme/app" }),
      repo("app@wip-x", { remote: "github.com", remotePath: "acme/app", worktree: true, parent: "app" }),
      repo("app-review", { remote: "github.com", remotePath: "acme/app" }),
      repo("other", { remote: "github.com", remotePath: "acme/other" }),
    ];
    const list = [s("a", { repo: "app" }), s("b", { repo: "app@wip-x" }), s("c", { repo: "app-review" }), s("d", { repo: "other" })];
    const groups = overviewGroups(list, repos, null, false);
    expect(groups.map((g) => g.label)).toEqual(["app", "other"]);
    expect(groups[0].sessions.map((x) => x.name).sort()).toEqual(["a", "b", "c"]);
    expect(groups[0].hint).toBe("github.com/acme/app");
  });

  it("falls back to the base working copy when there is no remote, and trails the sessions that have no folder", () => {
    const repos = [repo("local"), repo("local@wip-y", { worktree: true, parent: "local" })];
    const list = [s("a", { repo: "local@wip-y" }), s("b", { repo: "local" }), s("z", { kind: "shell" })];
    const groups = overviewGroups(list, repos, null, false);
    expect(groups.map((g) => g.label)).toEqual(["local", ""]);
    expect(groups[0].sessions.map((x) => x.name).sort()).toEqual(["a", "b"]);
    expect(groups[1].sessions.map((x) => x.name)).toEqual(["z"]);
  });

  it("counts the running cards per heading", () => {
    const list = [s("a"), s("b", { alive: false })];
    const groups = overviewGroups(list, [], null, true);
    expect(groups[0].alive).toBe(1);
    expect(aliveCount(list)).toBe(1);
  });
});

describe("orderByFamily", () => {
  const at = (d: number) => `2026-09-0${d}T00:00:00Z`;

  it("lays a family out parent → children in spawn order, stopped children included", () => {
    const list = [
      s("child2", { createdAt: at(4), originSession: "parent" }),
      s("child1", { createdAt: at(3), originSession: "parent", alive: false }),
      s("parent", { createdAt: at(2) }),
    ];
    expect(orderByFamily(list).map((x) => x.name)).toEqual(["parent", "child1", "child2"]);
  });

  it("stages whole families: the family holding a waiting session comes first, and stays whole", () => {
    const list = [
      s("lone", { createdAt: at(5) }),
      s("parent", { createdAt: at(1) }),
      s("kid", { createdAt: at(2), originSession: "parent", state: "question" }),
    ];
    // Without the family rule the question would sit alone at the top and its parent at the
    // bottom; here the pair moves together.
    expect(orderByFamily(list).map((x) => x.name)).toEqual(["parent", "kid", "lone"]);
  });

  it("keeps a grandchild under its own parent", () => {
    const list = [
      s("gc", { createdAt: at(3), originSession: "kid" }),
      s("kid", { createdAt: at(2), originSession: "root" }),
      s("root", { createdAt: at(1) }),
      s("other", { createdAt: at(9) }),
    ];
    expect(orderByFamily(list).map((x) => x.name)).toEqual(["other", "root", "kid", "gc"]);
  });

  it("treats a parent that is not on this grid as no parent (a child started in another repo)", () => {
    const list = [s("kid", { createdAt: at(2), originSession: "elsewhere" })];
    expect(orderByFamily(list).map((x) => x.name)).toEqual(["kid"]);
  });

  it("does not hide or hang on a cycle", () => {
    const list = [s("a", { originSession: "b" }), s("b", { originSession: "a" })];
    expect(orderByFamily(list).map((x) => x.name).sort()).toEqual(["a", "b"]);
  });

  it("does not reorder on a second call with the same input (a grid that stays open must be stable)", () => {
    const list = [s("a", { createdAt: at(2) }), s("b", { createdAt: at(2) })];
    const first = orderByFamily(list).map((x) => x.name);
    expect(orderByFamily([...list].reverse()).map((x) => x.name)).toEqual(first);
  });
});

describe("elapsedShort", () => {
  const now = Date.UTC(2026, 8, 12, 12, 0, 0);
  it("renders seconds, minutes, hours and days", () => {
    expect(elapsedShort(now - 45_000, now)).toBe("45s");
    expect(elapsedShort(now - 12 * 60_000, now)).toBe("12m");
    expect(elapsedShort(now - (3 * 3600 + 20 * 60) * 1000, now)).toBe("3h20m");
    expect(elapsedShort(now - 5 * 3600 * 1000, now)).toBe("5h");
    expect(elapsedShort(now - 50 * 3600 * 1000, now)).toBe("2d2h");
  });
  it("says nothing when the instant is unknown or in the future", () => {
    expect(elapsedShort(0, now)).toBe("");
    expect(elapsedShort(now + 1000, now)).toBe("");
  });
});
