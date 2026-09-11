import { describe, expect, it } from "vitest";
import { aliveCount, overviewSessions } from "./overview.ts";
import type { WorkingSet } from "../../lib/workingSets.ts";
import type { Session } from "../../types/session.ts";

const s = (name: string, over: Partial<Session> = {}): Session => ({ name, kind: "claude", alive: true, state: "idle", ...over });
const set = (over: Partial<WorkingSet>): WorkingSet => ({ id: "w1", name: "A", repos: [], convs: [], sessions: [], schedules: [], ...over });

describe("overviewSessions", () => {
  it("shows only running sessions unless stopped ones are asked for", () => {
    const list = [s("a"), s("b", { alive: false }), s("c")];
    expect(overviewSessions(list, null, false).map((x) => x.name)).toEqual(["a", "c"]);
    expect(overviewSessions(list, null, true).map((x) => x.name)).toEqual(["a", "c", "b"]);
  });

  it("follows the active working set, by direct membership or by the session's repo", () => {
    const list = [s("a", { repo: "proj" }), s("b", { repo: "other" }), s("c")];
    const w = set({ repos: ["proj"], sessions: ["c"] });
    expect(overviewSessions(list, w, false).map((x) => x.name)).toEqual(["a", "c"]);
  });

  it("lifts a session waiting for a person to the front, and keeps the rest newest first", () => {
    const list = [
      s("old", { createdAt: "2026-09-01T00:00:00Z" }),
      s("new", { createdAt: "2026-09-02T00:00:00Z" }),
      s("ask", { createdAt: "2026-08-01T00:00:00Z", state: "question" }),
    ];
    expect(overviewSessions(list, null, false).map((x) => x.name)).toEqual(["ask", "new", "old"]);
  });

  it("does not reorder on a second call with the same input (a grid that stays open must be stable)", () => {
    const list = [s("a", { createdAt: "2026-09-02T00:00:00Z" }), s("b", { createdAt: "2026-09-02T00:00:00Z" })];
    const first = overviewSessions(list, null, false).map((x) => x.name);
    expect(overviewSessions([...list].reverse(), null, false).map((x) => x.name)).toEqual(first);
  });

  it("counts the running cards", () => {
    expect(aliveCount([s("a"), s("b", { alive: false })])).toBe(1);
  });
});
