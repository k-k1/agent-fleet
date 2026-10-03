import { describe, expect, it } from "vitest";
import { railOrder, rotatableSessions, rotateTarget, rotationCurrent } from "./rotate.ts";
import type { Repo } from "../repos/store.ts";
import type { WorkingSet } from "../../lib/workingSets.ts";
import type { Session } from "../../types/session.ts";

const s = (name: string, extra: Partial<Session> = {}): Session => ({
  name,
  kind: "claude",
  alive: true,
  ...extra,
});

const set = (over: Partial<WorkingSet> = {}): WorkingSet => ({
  id: "wabcdef",
  name: "g",
  repos: [],
  convs: [],
  sessions: [],
  schedules: [],
  ...over,
});

describe("railOrder", () => {
  const at = (n: number) => `2026-09-0${n}T00:00:00Z`;
  const repos: Repo[] = [
    { name: "beta" },
    { name: "af" },
    { name: "af@p", worktree: true, parent: "af", createdAt: at(2) },
    { name: "af@q", worktree: true, parent: "af", createdAt: at(3) },
    { name: "af@c", worktree: true, parent: "af", createdAt: at(4) },
  ];
  const sessions = [
    s("sh", { kind: "shell", createdAt: at(7) }), // no folder: the other-sessions section
    s("b1", { repo: "beta", createdAt: at(6) }),
    s("child", { repo: "af@c", createdAt: at(4), originSession: "parent" }),
    s("other", { repo: "af@q", createdAt: at(3) }),
    s("parent", { repo: "af@p", createdAt: at(2) }),
    s("main", { repo: "af", createdAt: at(1) }),
  ];

  it("lists sessions as the rail does: repo tree depth first with nested worktrees, then the rest", () => {
    // The API order (newest first) would be sh, b1, child, other, parent, main.
    expect(railOrder(sessions, repos).map((x) => x.name)).toEqual(["main", "parent", "child", "other", "b1", "sh"]);
  });

  it("drives the swipe: +1 is the row below, -1 the row above, across a nested child", () => {
    const order = railOrder(sessions, repos);
    const list = rotatableSessions(order, null);
    expect(rotateTarget(list, "parent", 1, order)?.session.name).toBe("child");
    expect(rotateTarget(list, "child", 1, order)?.session.name).toBe("other");
    expect(rotateTarget(list, "child", -1, order)?.session.name).toBe("parent");
  });

  it("skips shell and ssm, and steps from one by its place in the rail", () => {
    const withSsm = [...sessions, s("box", { kind: "ssm", repo: "af@p", createdAt: at(5) })];
    const order = railOrder(withSsm, repos);
    const list = rotatableSessions(order, null);
    expect(list.map((x) => x.name)).toEqual(["main", "parent", "child", "other", "b1"]);
    // box sits above parent in af@p (newest first within a folder).
    expect(rotateTarget(list, "box", 1, order)).toMatchObject({ index: 1, total: 5, session: { name: "parent" } });
    expect(rotateTarget(list, "box", -1, order)?.session.name).toBe("main");
    // sh is the last row: forward wraps to the head, back lands on the last candidate.
    expect(rotateTarget(list, "sh", 1, order)?.session.name).toBe("main");
    expect(rotateTarget(list, "sh", -1, order)?.session.name).toBe("b1");
  });
});

describe("rotatableSessions", () => {
  it("returns only the alive sessions, in the list's own order", () => {
    const list = [s("s1"), s("s2", { alive: false }), s("s3", { alive: undefined }), s("s4")];
    expect(rotatableSessions(list, null).map((x) => x.name)).toEqual(["s1", "s4"]);
  });

  it("leaves out shell and ssm sessions", () => {
    const list = [s("s1", { kind: "shell" }), s("s2"), s("s3", { kind: "ssm" }), s("s4", { kind: "codex" })];
    expect(rotatableSessions(list, null).map((x) => x.name)).toEqual(["s2", "s4"]);
  });

  it("follows the filter when a working set is selected", () => {
    const list = [
      s("s1", { dir: "/home/dev/repos/alpha" }),
      s("s2", { dir: "/home/dev/repos/beta" }),
      s("s3"), // no repo: included only when named directly
    ];
    const w = set({ repos: ["alpha"], sessions: ["s3"] });
    expect(rotatableSessions(list, w).map((x) => x.name)).toEqual(["s1", "s3"]);
  });

  it("lets a worktree inherit the parent clone's membership", () => {
    const list = [s("s1", { dir: "/home/dev/repos/alpha@wip-x1" })];
    expect(rotatableSessions(list, set({ repos: ["alpha"] })).map((x) => x.name)).toEqual(["s1"]);
  });
});

describe("rotateTarget", () => {
  const list = [s("s1"), s("s2"), s("s3")];

  it("advances to the next one, wrapping from the tail to the head", () => {
    expect(rotateTarget(list, "s1", 1)?.session.name).toBe("s2");
    expect(rotateTarget(list, "s3", 1)?.session.name).toBe("s1");
  });

  it("applies the same rule going backwards", () => {
    expect(rotateTarget(list, "s1", -1)?.session.name).toBe("s3");
    expect(rotateTarget(list, "s3", -1)?.session.name).toBe("s2");
  });

  it("returns a zero-based index and the total, for the toast's n/total", () => {
    expect(rotateTarget(list, "s1", 1)).toMatchObject({ index: 1, total: 3 });
  });

  it("starts at the head going forward and the tail going back when current is not a candidate", () => {
    // stopped / in another working set / a pane that is not a session at all (null)
    expect(rotateTarget(list, null, 1)?.session.name).toBe("s1");
    expect(rotateTarget(list, "gone", 1)?.session.name).toBe("s1");
    expect(rotateTarget(list, null, -1)?.session.name).toBe("s3");
  });

  it("returns null when there is no candidate", () => {
    expect(rotateTarget([], "s1", 1)).toBeNull();
  });

  it("returns null when the only candidate is the current session", () => {
    expect(rotateTarget([s("s1")], "s1", 1)).toBeNull();
    // with one candidate it is still a destination when we are looking at something else
    expect(rotateTarget([s("s1")], "other", 1)?.session.name).toBe("s1");
  });

  it("never produces a negative index, even for a delta larger than the list", () => {
    expect(rotateTarget(list, "s1", -7)?.session.name).toBe("s3");
    expect(rotateTarget(list, "s1", 7)?.session.name).toBe("s2");
  });
});

describe("rotationCurrent", () => {
  const list = [s("painter", { studio: "st-1" }), s("coder")];
  it("an image-studio pane stands for the session bound to its studio, so a swipe moves on from it", () => {
    const pane = { session: null, content: { kind: "imagegen", studioId: "st-1" } };
    const cur = rotationCurrent(pane, list);
    expect(cur).toBe("painter");
    expect(rotateTarget(list, cur, 1)?.session.name).toBe("coder");
  });
  it("a studio with no bound session stands for nothing (the rotation starts at the head)", () => {
    expect(rotationCurrent({ session: null, content: { kind: "imagegen", studioId: "st-9" } }, list)).toBeNull();
  });
  it("any other pane stands for its own session", () => {
    expect(rotationCurrent({ session: "coder", content: { kind: "terminal" } }, list)).toBe("coder");
    expect(rotationCurrent(null, list)).toBeNull();
  });
});
