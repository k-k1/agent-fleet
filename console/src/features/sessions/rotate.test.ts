import { describe, expect, it } from "vitest";
import { rotatableSessions, rotateTarget } from "./rotate.ts";
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

describe("rotatableSessions", () => {
  it("returns only the alive sessions, in the list's own order", () => {
    const list = [s("s1"), s("s2", { alive: false }), s("s3", { alive: undefined }), s("s4")];
    expect(rotatableSessions(list, null).map((x) => x.name)).toEqual(["s1", "s4"]);
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
