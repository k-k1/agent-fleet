import { describe, expect, it } from "vitest";
import type { Session } from "../../types/session.ts";
import { buildRuns, failedRunKeys, scopeRuns, fmtElapsed, itemLabel, runRootName, waitingRunCount, type RunDelivery } from "./runs.ts";

const dlv = (categoryId: string, state: string, extra: Partial<RunDelivery> = {}): RunDelivery => ({ id: "d-" + categoryId, categoryId, state, outcome: "", turnOutcome: "", sentAt: "", createdAt: "", closedAt: "", heldBy: "", ...extra });
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
  const rows = buildRuns(sessions, new Map([["run-a", dlv("cat1", "open")]]));

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

describe("a run's state follows its queue's delivery", () => {
  const t = (s: Session[], d?: RunDelivery) => buildRuns(s, d ? new Map([["r", d]]) : new Map())[0];
  it("is waiting while a session the run started waits, though the root is stopped", () => {
    const r = t([sess("r", { origin: "queue", alive: false }), sess("c", { origin: "session", originSession: "r", state: "question" })], dlv("c1", "open"));
    expect(r.state).toBe("waiting");
  });
  it("lists a closed run whose root is alive as running and resumed, and waiting when it waits for the member", () => {
    const r = t([sess("r", { origin: "queue" })], dlv("c1", "closed", { closedAt: "2026-11-01T01:00:00Z" }));
    expect(r.state).toBe("running");
    expect(r.resumed).toBe(true);
    expect(Number.isNaN(r.endedAt)).toBe(true);
    expect(t([sess("r", { origin: "queue", state: "question" })], dlv("c1", "closed")).state).toBe("waiting");
  });
  it("keeps a closed run ended when only a child it started is alive", () => {
    const r = t([sess("r", { origin: "queue", alive: false }), sess("c", { origin: "session", originSession: "r" })], dlv("c1", "closed"));
    expect(r.state).toBe("ended");
    expect(r.resumed).toBe(false);
  });
  it("is not resumed while the delivery is open", () => {
    expect(t([sess("r", { origin: "queue" })], dlv("c1", "open")).resumed).toBe(false);
  });
  it("is ended once the delivery closed and the root stopped", () => {
    expect(t([sess("r", { origin: "queue", alive: false })], dlv("c1", "closed", { closedAt: "2026-11-01T01:00:00Z" })).state).toBe("ended");
  });
  it("stays running while a child works after the root stopped", () => {
    const r = t([sess("r", { origin: "queue", alive: false }), sess("c", { origin: "session", originSession: "r" })], dlv("c1", "open"));
    expect(r.state).toBe("running");
  });
  it("falls back to the sessions when no delivery is known", () => {
    expect(t([sess("r", { origin: "queue", alive: false })]).state).toBe("ended");
    expect(t([sess("r", { origin: "queue", alive: false }), sess("c", { origin: "session", originSession: "r" })]).state).toBe("running");
  });
  it("keeps the start and end of a finished run", () => {
    const r = t([sess("r", { origin: "queue", alive: false })], dlv("c1", "closed", { sentAt: "2026-11-01T00:00:00Z", closedAt: "2026-11-01T00:30:00Z" }));
    expect(r.endedAt - r.startedAt).toBe(30 * 60_000);
  });
  it("is waiting when a child was stopped on a question, or the queue holds the run for the member", () => {
    const stopped = [sess("r", { origin: "queue", alive: false }), sess("c", { origin: "session", originSession: "r", alive: false, carried: "question" })];
    expect(t(stopped, dlv("c1", "open")).state).toBe("waiting");
    expect(t(stopped).state).toBe("waiting"); // and with no delivery at all
    expect(t([sess("r", { origin: "queue", alive: false })], dlv("c1", "open", { heldBy: "person" })).state).toBe("waiting");
  });
  it("uses the sessions again once a delivery's state expired", () => {
    expect(t([sess("r", { origin: "queue", alive: false })], dlv("c1", "")).state).toBe("ended");
  });
  it("names only the started work item, not one linked later", () => {
    const r = t([sess("r", { origin: "queue", links: [{ provider: "github", key: "o/r#9", role: "watching", title: "Other" }] })]);
    expect(r.item).toBeNull();
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
  it("labels items and elapsed time", () => {
    expect(itemLabel("o/r#5")).toBe("#5");
    expect(itemLabel("PROJ-12")).toBe("PROJ-12");
    expect(fmtElapsed(30_000)).toBe("<1m");
    expect(fmtElapsed(12 * 60_000)).toBe("12m");
    expect(fmtElapsed(65 * 60_000)).toBe("1h 5m");
    expect(fmtElapsed(27 * 3600_000)).toBe("1d 3h");
  });
});

describe("run badges", () => {
  it("counts the waiting runs and keys the failed ones by delivery", () => {
    const closedRun = (name: string) => sess(name, { origin: "queue", alive: false });
    const rows = buildRuns(
      [sess("a", { origin: "queue", state: "question" }), closedRun("b"), closedRun("c"), closedRun("d"), closedRun("e")],
      new Map([
        ["a", dlv("c", "open", { id: "da" })],
        // The first turn failed and the queue closed the run: a failure.
        ["b", dlv("c", "closed", { id: "db", outcome: "clean", turnOutcome: "turn-failed" })],
        // The member ended or aborted it: their doing, not a failure.
        ["c", dlv("c", "closed", { id: "dc", outcome: "aborted", turnOutcome: "turn-failed" })],
        ["d", dlv("c", "closed", { id: "dd", outcome: "ended", turnOutcome: "turn-failed" })],
        ["e", dlv("c", "closed", { id: "de", outcome: "clean", turnOutcome: "clean" })],
      ]),
    );
    expect(waitingRunCount(rows)).toBe(1);
    expect(failedRunKeys(rows)).toEqual(["db"]);
  });
});

describe("a failed run stays failed", () => {
  it("when its session is resumed, which only changes what the row shows", () => {
    const rows = buildRuns(
      [sess("a", { origin: "queue", alive: true })],
      new Map([["a", dlv("c", "closed", { id: "da", outcome: "clean", turnOutcome: "turn-failed" })]]),
    );
    expect(rows[0].resumed).toBe(true);
    expect(failedRunKeys(rows)).toEqual(["da"]);
  });
});

describe("scopeRuns", () => {
  const rows = buildRuns(
    [sess("a", { origin: "queue", state: "question" }), sess("b", { origin: "queue", state: "question" }), sess("c", { origin: "queue", state: "question" })],
    new Map([["a", dlv("ca", "open")], ["b", dlv("cb", "open")], ["c", dlv("cx", "open")]]),
  );
  const queues = [{ categoryId: "ca", repo: "A" }, { categoryId: "cb", repo: "B" }] as never;
  const set = { id: "w", name: "w", repos: ["A"], sessions: [], convs: [], schedules: [] } as never;

  it("keeps the runs of the set's repositories and those no category names; count and view agree", () => {
    expect(scopeRuns(rows, queues, null)).toHaveLength(3);
    const scoped = scopeRuns(rows, queues, set);
    expect(scoped.map((r) => r.root.name).sort()).toEqual(["a", "c"]);
    expect(waitingRunCount(scoped)).toBe(2);
  });

  it("keeps a space's runs in every set, as a category without a repository", () => {
    const spaced = [{ categoryId: "ca", repo: "A" }, { categoryId: "cb", repo: "spaces/b" }] as never;
    expect(scopeRuns(rows, spaced, set).map((r) => r.root.name).sort()).toEqual(["a", "b", "c"]);
  });
});

describe("a run's category", () => {
  const cat = (sessions: Session[], d?: RunDelivery) => buildRuns(sessions, new Map(d ? [["r", d]] : [])).find((r) => r.root.name === "r")!.categoryId;
  it("is the delivery's when there is one, even if the session names another", () => {
    expect(cat([sess("r", { origin: "queue", queueCategoryId: "cat2" })], dlv("cat1", "closed"))).toBe("cat1");
  });
  it("falls back to the session's own once the delivery left the history window", () => {
    expect(cat([sess("r", { origin: "queue", queueCategoryId: "cat2", alive: false })])).toBe("cat2");
  });
  it("is empty with neither, which is what \"Other runs\" holds", () => {
    expect(cat([sess("r", { origin: "queue" })])).toBe("");
  });
});

describe("buildRuns tie-break", () => {
  const a = sess("run-a", { origin: "queue", createdAt: "2026-11-22T18:39:31+09:00", alive: false });
  const b = sess("run-b", { origin: "queue", createdAt: "2026-11-22T18:39:31+09:00", alive: false });
  it("orders roots sharing a createdAt the same way for either input order", () => {
    const one = buildRuns([a, b], new Map()).map((r) => r.root.name);
    const two = buildRuns([b, a], new Map()).map((r) => r.root.name);
    expect(one).toEqual(two);
  });
  it("orders a run's children oldest first, ties by name, for either input order", () => {
    const root = sess("run-r", { origin: "queue", createdAt: "2026-11-22T18:00:00+09:00", alive: false });
    const k = (n: string, at: string) => sess(n, { origin: "session", originSession: "run-r", createdAt: at });
    const kids = [k("kid-b", "2026-11-22T18:39:31+09:00"), k("kid-a", "2026-11-22T18:39:31+09:00"), k("kid-z", "2026-11-22T18:10:00+09:00")];
    const names = (list: Session[]) => buildRuns([root, ...list], new Map())[0].children.map((c) => c.name);
    expect(names(kids)).toEqual(["kid-z", "kid-a", "kid-b"]);
    expect(names([...kids].reverse())).toEqual(["kid-z", "kid-a", "kid-b"]);
  });
});
