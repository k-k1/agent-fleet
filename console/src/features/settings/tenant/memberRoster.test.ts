// The roster's state words, "needs attention" and the filter / sort / search (#2385 phase 1).
import { describe, it, expect } from "vitest";
import { attentionReasons, matchesFilter, matchesQuery, rosterCounts, rosterState, sortMembers } from "./memberRoster.ts";
import type { Member } from "../parts/adminShared.ts";

const NOW = Date.parse("2026-10-20T12:00:00Z");
const AUTO_STOP = { kind: "start-deadline", phase: "x", limit_minutes: 30, stopped_at: "2026-10-20T10:00:00Z" };
const m = (over: Partial<Member>): Member => ({ user_key: "k", role: "member", status: "active", ...over });
const working = (lapseAt?: string) => ({
  enabled: true,
  holders: [{ kind: "working", session: "s1", lapseAt }],
  observedAt: "2026-10-20T11:59:00Z",
});

describe("rosterState", () => {
  it("names each state the CP reports", () => {
    expect(rosterState(m({ state: "running" }))).toBe("running");
    expect(rosterState(m({ state: "starting" }))).toBe("starting");
    expect(rosterState(m({ state: "stopped" }))).toBe("stopped");
    expect(rosterState(m({ state: "stopped", auto_stop: AUTO_STOP }))).toBe("auto_stopped");
    expect(rosterState(m({ state: "stopped", status: "removed" }))).toBe("removed");
  });

  // "none" also means "stopped" on ecs-ec2 after Clean home and on native, so only a CP that
  // says there is no workspace row earns "never started".
  it("says never started only when the CP says there is no workspace row", () => {
    expect(rosterState(m({ state: "none", has_workspace: false }))).toBe("never");
    expect(rosterState(m({ state: "none", has_workspace: true }))).toBe("stopped");
    expect(rosterState(m({ state: "none" }))).toBe("stopped"); // older CP: no has_workspace
  });

  // Stopping a removed member's workspace is the next offboarding step: hiding that it is up
  // behind "removed" would hide the cost.
  it("keeps a removed member's live workspace visible as running", () => {
    expect(rosterState(m({ state: "running", status: "removed" }))).toBe("running");
  });
});

describe("attentionReasons", () => {
  it("flags a workspace the Control Plane stopped", () => {
    expect(attentionReasons(m({ state: "stopped", auto_stop: AUTO_STOP }), NOW)).toEqual(["auto_stop"]);
  });

  it("flags a working hold only once its lapseAt has passed", () => {
    expect(attentionReasons(m({ state: "running", idle: working("2026-10-20T11:00:00Z") }), NOW)).toEqual(["hold_lapsed"]);
    expect(attentionReasons(m({ state: "running", idle: working("2026-10-20T13:00:00Z") }), NOW)).toEqual([]);
    expect(attentionReasons(m({ state: "running", idle: working(undefined) }), NOW)).toEqual([]);
  });

  it("ignores a stale idle observation on a workspace that is no longer running", () => {
    expect(attentionReasons(m({ state: "stopped", idle: working("2026-10-20T11:00:00Z") }), NOW)).toEqual([]);
  });
});

describe("filter, search and sort", () => {
  const list = [
    m({ user_key: "c", email: "carol@x.com", state: "running" }),
    m({ user_key: "a", email: "alice@x.com", state: "stopped" }),
    m({ user_key: "b", email: "bob@x.com", state: "stopped", auto_stop: AUTO_STOP }),
    m({ user_key: "d", email: "dave@x.com", state: "stopped", status: "removed" }),
    m({ user_key: "e", email: "erin@x.com", state: "starting" }),
  ];

  it("puts members who need attention first, then by name", () => {
    expect(sortMembers(list, "attention", NOW).map((x) => x.user_key)).toEqual(["b", "a", "c", "d", "e"]);
    expect(sortMembers(list, "name", NOW).map((x) => x.user_key)).toEqual(["a", "b", "c", "d", "e"]);
  });

  it("counts each filter over the same rules", () => {
    const c = rosterCounts(list, NOW);
    expect(c.total).toBe(5);
    expect(c.running).toBe(2); // starting counts, as in the "running" filter
    expect(c.attention).toBe(1);
    expect(c.byFilter).toEqual({ all: 5, running: 2, attention: 1, stopped: 2, removed: 1 });
    expect(list.filter((x) => matchesFilter(x, "stopped", NOW)).map((x) => x.user_key)).toEqual(["a", "b"]);
  });

  it("searches e-mail and user_key, case-insensitively", () => {
    expect(list.filter((x) => matchesQuery(x, " ALI ")).map((x) => x.user_key)).toEqual(["a"]);
    expect(list.filter((x) => matchesQuery(x, "da")).map((x) => x.user_key)).toEqual(["d"]);
    expect(list.filter((x) => matchesQuery(x, "")).length).toBe(5);
  });
});
