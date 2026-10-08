import { describe, it, expect } from "vitest";
import { layoutFamilies } from "./family.ts";
import type { Session } from "../../types/session.ts";

const s = (name: string, extra: Partial<Session> = {}): Session => ({
  name,
  kind: "claude",
  alive: true,
  createdAt: "2026-09-01T00:00:00Z",
  ...extra,
});
const flat = (rows: ReturnType<typeof layoutFamilies>) => rows.map((r) => r.session.name + ":" + r.depth);

describe("layoutFamilies", () => {
  it("gathers a family into one block, parent first, children in spawn order", () => {
    // Input is attention order: both children sort before and after unrelated rows.
    const list = [
      s("c2", { originSession: "p", createdAt: "2026-09-01T02:00:00Z" }),
      s("other"),
      s("c1", { originSession: "p", createdAt: "2026-09-01T01:00:00Z" }),
      s("p"),
    ];
    expect(flat(layoutFamilies(list))).toEqual(["p:0", "c1:1", "c2:1", "other:0"]);
  });

  it("puts a stage-spanning family where its most urgent member sits", () => {
    const list = [s("waitingChild", { originSession: "stoppedParent" }), s("mid"), s("stoppedParent", { alive: false })];
    expect(flat(layoutFamilies(list))).toEqual(["stoppedParent:0", "waitingChild:1", "mid:0"]);
  });

  it("keeps a child whose parent is not in the list as a root", () => {
    expect(flat(layoutFamilies([s("orphan", { originSession: "gone" }), s("x")]))).toEqual(["orphan:0", "x:0"]);
  });

  it("indents grandchildren one level deeper", () => {
    const list = [s("g", { originSession: "c" }), s("c", { originSession: "p" }), s("p")];
    expect(flat(layoutFamilies(list))).toEqual(["p:0", "c:1", "g:2"]);
  });

  it("neither hides nor duplicates rows in an originSession cycle", () => {
    const list = [s("a", { originSession: "b" }), s("b", { originSession: "a" }), s("self", { originSession: "self" })];
    const out = flat(layoutFamilies(list));
    expect(out.map((r) => r.split(":")[0]).sort()).toEqual(["a", "b", "self"]);
  });

  it("leaves sessions the predicate rejects as roots at their own position", () => {
    const list = [s("p"), s("late", { originSession: "p" }), s("c", { originSession: "p", createdAt: "2026-09-02T00:00:00Z" })];
    expect(flat(layoutFamilies(list, (x) => x.name !== "late"))).toEqual(["p:0", "c:1", "late:0"]);
  });
});
