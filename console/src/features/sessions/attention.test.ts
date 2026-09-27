// The "needs you" queue and the walk the jump command takes along it. The walk is the part
// that is easy to break: jumping to an unread session marks it read, so a queue re-derived on
// every press loses the position and loops over the head without ever reaching the tail.
import { describe, expect, it } from "vitest";
import { attentionQueue, nextAttention, unreadAtFromNotifications } from "./attention.ts";
import type { AttentionWalk } from "./attention.ts";
import type { Session } from "../../types/session.ts";

const s = (name: string, state = "", alive = true): Session => ({ name, kind: "claude", alive, state });
const names = (list: Session[]) => list.map((x) => x.name);
const note = (id: string, createdAt: string, seen = false, type = "session") => ({ seen, target: { type, id }, createdAt });

describe("unreadAtFromNotifications", () => {
  it("keeps the newest unseen session notification per session", () => {
    expect(
      unreadAtFromNotifications([
        note("a", "2026-09-27T10:00:00Z"),
        note("a", "2026-09-27T11:00:00Z"),
        note("b", "2026-09-27T12:00:00Z", true), // seen
        note("c", "2026-09-27T12:00:00Z", false, "schedule"), // not a session
        note("d", "garbage"), // unread all the same, sorts last
      ]),
    ).toEqual({ a: Date.parse("2026-09-27T11:00:00Z"), d: 0 });
  });
});

describe("attentionQueue", () => {
  it("puts waiting sessions first by when they started waiting, then unread ones by newest notification", () => {
    const sessions = [s("idle"), s("q1", "question"), s("r1"), s("p1", "plan"), s("r2"), s("gone", "question", false)];
    const waitingAt = (n: string) => ({ q1: 100, p1: 200 })[n] ?? 0;
    expect(names(attentionQueue(sessions, waitingAt, { r1: 10, r2: 20, q1: 5 }))).toEqual(["p1", "q1", "r2", "r1"]);
  });

  it("leaves out a stopped session's carried question but keeps its unread notification", () => {
    expect(names(attentionQueue([s("x", "question", false)], () => 0, {}))).toEqual([]);
    expect(names(attentionQueue([s("x", "", false)], () => 0, { x: 1 }))).toEqual(["x"]);
  });

  it("ignores notifications for sessions no longer in the list", () => {
    expect(names(attentionQueue([s("a")], () => 0, { deleted: 1 }))).toEqual([]);
  });
});

describe("nextAttention", () => {
  it("starts at the head when the active pane is not in the queue", () => {
    expect(nextAttention(["a", "b"], "elsewhere", null)?.at).toBe("a");
    expect(nextAttention(["a", "b"], null, null)?.at).toBe("a");
  });

  it("steps past the active session when it is itself in the queue", () => {
    expect(nextAttention(["a", "b", "c"], "b", null)?.at).toBe("c");
    expect(nextAttention(["a", "b", "c"], "c", null)?.at).toBe("a");
  });

  it("keeps the current session as the last stop while one of its destinations is not on screen", () => {
    expect(nextAttention(["a"], "a", null, false)?.at).toBe("a");
    expect(nextAttention(["a", "b"], "a", null, false)?.at).toBe("b");
    expect(nextAttention(["a", "b"], "b", null, false)?.at).toBe("a");
  });

  it("returns null when nothing but the current session needs you", () => {
    expect(nextAttention([], "a", null)).toBeNull();
    expect(nextAttention(["a"], "a", null)).toBeNull();
  });

  it("walks the whole queue even though each unread stop drops out once visited", () => {
    // w1, w2 are waiting (stay); u1, u2 are unread (read the moment they are shown).
    let queue = ["w1", "w2", "u1", "u2"];
    let walk: AttentionWalk | null = null;
    let current = "elsewhere";
    const visited: string[] = [];
    for (let i = 0; i < 5; i++) {
      walk = nextAttention(queue, current, walk);
      if (!walk) break;
      current = walk.at;
      visited.push(current);
      queue = queue.filter((n) => !n.startsWith("u") || n !== current);
    }
    expect(visited).toEqual(["w1", "w2", "u1", "u2", "w1"]);
  });

  it("takes a new arrival after the rest of the walk, before wrapping", () => {
    const walk = nextAttention(["a", "b"], null, null)!; // at a
    expect(nextAttention(["a", "b", "new"], "a", walk)?.at).toBe("b");
    const next = nextAttention(["a", "b", "new"], "a", walk)!;
    expect(nextAttention(["a", "b", "new"], "b", next)?.at).toBe("new");
  });

  it("starts over from the fresh queue when the user moved elsewhere in between", () => {
    const walk = nextAttention(["a", "b", "c"], null, null)!; // at a
    expect(nextAttention(["a", "b", "c"], "b", walk)?.at).toBe("c");
  });
});
