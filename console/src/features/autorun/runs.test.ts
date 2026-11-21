import { describe, expect, it } from "vitest";
import type { Session } from "../../types/session.ts";
import { buildRuns, categoryIndex, fmtElapsed, itemLabel, runRootName } from "./runs.ts";

const sess = (name: string, extra: Partial<Session>): Session => ({ name, kind: "claude", alive: true, ...extra });

describe("buildRuns", () => {
  const sessions: Session[] = [
    sess("run-a", {
      origin: "queue",
      createdAt: "2026-11-01T00:00:00Z",
      links: [{ provider: "github", key: "o/r#12", role: "started", title: "Fix it" }],
      pr: { number: 30, state: "open", url: "u" },
    }),
    sess("rev-a", { origin: "session", originSession: "run-a", createdAt: "2026-11-01T01:00:00Z" }),
    sess("rev-a2", { origin: "session", originSession: "rev-a", createdAt: "2026-11-01T02:00:00Z" }),
    sess("run-b", { origin: "queue", createdAt: "2026-11-02T00:00:00Z", alive: false }),
    sess("run-c", { origin: "queue", createdAt: "2026-11-03T00:00:00Z", state: "question" }),
    sess("hand", { origin: "user" }),
    sess("cron", { origin: "schedule" }),
    sess("kid-of-hand", { origin: "session", originSession: "hand" }),
  ];
  const rows = buildRuns(sessions, new Map([["run-a", "cat1"]]));

  it("makes one row per run, newest first, and none for the member's or a schedule's sessions", () => {
    expect(rows.map((r) => r.root.name)).toEqual(["run-c", "run-b", "run-a"]);
  });

  it("derives running, waiting and ended", () => {
    expect(Object.fromEntries(rows.map((r) => [r.root.name, r.state]))).toEqual({ "run-a": "running", "run-b": "ended", "run-c": "waiting" });
  });

  it("carries the work item, the PR and the category", () => {
    const a = rows.find((r) => r.root.name === "run-a")!;
    expect(a.item).toEqual({ key: "o/r#12", label: "#12", title: "Fix it" });
    expect(a.pr?.number).toBe(30);
    expect(a.categoryId).toBe("cat1");
    expect(rows[0].categoryId).toBe("");
    expect(rows[0].item).toBeNull();
  });

  it("lists every session the run started under the run, oldest first", () => {
    expect(rows.find((r) => r.root.name === "run-a")!.children.map((c) => c.name)).toEqual(["rev-a", "rev-a2"]);
  });
});

describe("runRootName", () => {
  it("answers nothing for a cycle, a vanished parent and a hand-started parent", () => {
    const by = new Map([
      ["x", sess("x", { origin: "session", originSession: "y" })],
      ["y", sess("y", { origin: "session", originSession: "x" })],
      ["h", sess("h", { origin: "user" })],
    ]);
    expect(runRootName(by.get("x")!, by)).toBe("");
    expect(runRootName(sess("z", { origin: "session", originSession: "gone" }), by)).toBe("");
    expect(runRootName(sess("k", { origin: "session", originSession: "h" }), by)).toBe("");
  });
});

describe("helpers", () => {
  it("maps deliveries to categories", () => {
    expect(categoryIndex({ c1: [{ session: "s1" } as never, { session: "" } as never] }).get("s1")).toBe("c1");
  });
  it("labels items and elapsed time", () => {
    expect(itemLabel("o/r#5")).toBe("#5");
    expect(itemLabel("PROJ-12")).toBe("PROJ-12");
    expect(fmtElapsed(30_000)).toBe("<1m");
    expect(fmtElapsed(12 * 60_000)).toBe("12m");
    expect(fmtElapsed(65 * 60_000)).toBe("1h 5m");
    expect(fmtElapsed(27 * 3600_000)).toBe("1d 3h");
  });
});
