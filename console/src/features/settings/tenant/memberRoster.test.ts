// The roster's state words, "needs attention" and the filter / sort / search (#2385 phase 1).
import { describe, it, expect } from "vitest";
import { attentionReasons, bootFailures, bootSkipped, diskFigureNote, diskLevel, homeFullFigures, memberStopAvailable, nextMemberHomePressure, matchesFilter, matchesQuery, rosterCounts, rosterState, sortMembers } from "./memberRoster.ts";
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

describe("sortMembers by last active", () => {
  it("puts the most recent first and members with no value last", () => {
    const list = [
      m({ user_key: "none", email: "a@x.com" }),
      m({ user_key: "old", email: "b@x.com", last_active_at: "2026-10-01T00:00:00Z" }),
      m({ user_key: "bad", email: "c@x.com", last_active_at: "not a date" }),
      m({ user_key: "new", email: "d@x.com", last_active_at: "2026-10-19T00:00:00Z" }),
    ];
    expect(sortMembers(list, "last_active", NOW).map((x) => x.user_key)).toEqual(["new", "old", "none", "bad"]);
  });
});

const GIB = 1024 ** 3;
const withDisk = (used: number, total?: number) => m({ disk: { used, total } });

describe("default attention order", () => {
  it("leads with a full disk, whatever order the members arrive in", () => {
    const err = m({ user_key: "zed", disk: { used: 96 * GIB, total: 100 * GIB } });
    const warn = m({ user_key: "alice", disk: { used: 92 * GIB, total: 100 * GIB } });
    const stopped = m({ user_key: "carol", state: "stopped", auto_stop: AUTO_STOP });
    const calm = m({ user_key: "bob" });
    const orders = [
      [calm, warn, stopped, err],
      [err, stopped, calm, warn],
      [warn, calm, err, stopped],
    ];
    for (const list of orders) {
      expect(sortMembers(list, "attention", NOW).map((x) => x.user_key)).toEqual(["zed", "alice", "carol", "bob"]);
    }
  });
});

describe("disk level and attention", () => {
  it("warns by the member notice's rule (over 90 % or under 2 GiB free), errors at 95 % or under 2 GiB free", () => {
    expect(diskLevel(withDisk(84 * GIB, 100 * GIB))).toBe("ok");
    // 85-90 % used with plenty free: the detail shows no notice, so the card must not warn.
    expect(diskLevel(withDisk(85 * GIB, 100 * GIB))).toBe("ok");
    expect(diskLevel(withDisk(90 * GIB, 100 * GIB))).toBe("ok");
    expect(diskLevel(withDisk(91 * GIB, 100 * GIB))).toBe("warn");
    expect(diskLevel(withDisk(95 * GIB, 100 * GIB))).toBe("error");
    // 50 % used but only 1.5 GiB left of 3 GiB.
    expect(diskLevel(withDisk(1.5 * GIB, 3 * GIB))).toBe("error");
    expect(diskLevel(withDisk(98 * GIB, 100 * GIB))).toBe("error");
  });

  it("judges nothing without a reading or a total", () => {
    expect(diskLevel(m({}))).toBeNull();
    expect(diskLevel(withDisk(500 * GIB))).toBeNull();
    expect(diskLevel(withDisk(500 * GIB, 0))).toBeNull();
    expect(attentionReasons(withDisk(500 * GIB), NOW)).toEqual([]);
  });

  it("adds a disk reason to needs-attention, stopped members included", () => {
    expect(attentionReasons(m({ state: "stopped", disk: { used: 96 * GIB, total: 100 * GIB } }), NOW)).toEqual(["disk_error"]);
    expect(attentionReasons(withDisk(92 * GIB, 100 * GIB), NOW)).toEqual(["disk_warn"]);
    expect(attentionReasons(withDisk(88 * GIB, 100 * GIB), NOW)).toEqual([]);
  });

  it("sorts the fullest first and members with no ratio last", () => {
    const list = [
      m({ user_key: "none" }),
      m({ user_key: "ok", disk: { used: 10 * GIB, total: 100 * GIB } }),
      m({ user_key: "used-only", disk: { used: 90 * GIB } }),
      m({ user_key: "err", disk: { used: 96 * GIB, total: 100 * GIB } }),
      m({ user_key: "warn", disk: { used: 92 * GIB, total: 100 * GIB } }),
    ];
    expect(sortMembers(list, "disk", NOW).map((x) => x.user_key)).toEqual(["err", "warn", "ok", "none", "used-only"]);
  });
});

describe("boot-install failure", () => {
  it("flags a member whose last start failed an install, never one with nothing reported", () => {
    expect(attentionReasons(m({ state: "stopped", boot_failed: { claude: "no_space" } }), NOW)).toEqual(["boot_install"]);
    expect(attentionReasons(m({ state: "stopped", boot_failed: {} }), NOW)).toEqual([]);
    expect(attentionReasons(m({ state: "stopped" }), NOW)).toEqual([]);
  });

  it("does not treat a skipped upgrade as a failure", () => {
    const bf = { claude: "skipped_no_space", agy: "no_space" };
    expect(bootFailures(bf)).toEqual([["agy", "no_space"]]);
    expect(bootSkipped(bf)).toEqual(["claude"]);
    expect(attentionReasons(m({ state: "stopped", boot_failed: { claude: "skipped_no_space" } }), NOW)).toEqual([]);
    expect(attentionReasons(m({ state: "stopped", boot_failed: bf }), NOW)).toEqual(["boot_install"]);
  });

  it("lists the failures by tool", () => {
    expect(bootFailures({ rtk: "network", claude: "no_space" })).toEqual([
      ["claude", "no_space"],
      ["rtk", "network"],
    ]);
    expect(bootFailures(undefined)).toEqual([]);
  });
});

describe("nextMemberHomePressure", () => {
  const GIB = 1024 ** 3;
  const own = (used: number, total = 100 * GIB) => ({ home_own: true, disk_used: used, disk_total: total });

  it("never flags where the disk is not the member's own volume", () => {
    expect(nextMemberHomePressure(false, { home_own: false, disk_used: 99 * GIB, disk_total: 100 * GIB })).toBe(false);
    expect(nextMemberHomePressure(true, { disk_used: 99 * GIB, disk_total: 100 * GIB })).toBe(false);
  });

  it("raises past the member-notice threshold and re-arms only with headroom", () => {
    expect(nextMemberHomePressure(false, own(80 * GIB))).toBe(false);
    expect(nextMemberHomePressure(false, own(91 * GIB))).toBe(true);
    expect(nextMemberHomePressure(true, own(88 * GIB))).toBe(true); // between the two lines: stays
    expect(nextMemberHomePressure(true, own(60 * GIB))).toBe(false);
  });

  it("changes nothing without a reading (a stopped or unreachable workspace)", () => {
    expect(nextMemberHomePressure(true, { home_own: true })).toBe(true);
    expect(nextMemberHomePressure(false, { home_own: true })).toBe(false);
    expect(nextMemberHomePressure(false, null)).toBe(false);
  });
});

describe("homeFullFigures", () => {
  const GIB = 1024 ** 3;
  it("gives the numbers only when the poll carries both", () => {
    expect(homeFullFigures({ disk_used: 99 * GIB, disk_total: 100 * GIB })).toEqual({ used: 99 * GIB, total: 100 * GIB });
    expect(homeFullFigures({ disk_total: 100 * GIB })).toBeNull();
    expect(homeFullFigures({ disk_used: 5 })).toBeNull();
    expect(homeFullFigures({ disk_used: 5, disk_total: 0 })).toBeNull();
    expect(homeFullFigures(null)).toBeNull();
  });

  // 99% -> a poll with no statfs -> recovery: the flag holds through the gap, the notice has no
  // figures to print during it, and both clear once the headroom is back.
  it("holds the flag through a missing reading without printing NaN", () => {
    const own = (used: number) => ({ home_own: true, disk_used: used * GIB, disk_total: 100 * GIB });
    let flag = nextMemberHomePressure(false, own(99));
    expect(flag).toBe(true);
    const gap = { home_own: true };
    flag = nextMemberHomePressure(flag, gap);
    expect(flag).toBe(true);
    expect(homeFullFigures(gap)).toBeNull();
    flag = nextMemberHomePressure(flag, own(50));
    expect(flag).toBe(false);
  });
});

describe("memberStopAvailable", () => {
  it("offers Force-stop for running and for starting, not for stopped or unread", () => {
    expect(memberStopAvailable({ running: true })).toBe(true);
    expect(memberStopAvailable({ running: false, starting: true })).toBe(true);
    expect(memberStopAvailable({ running: false })).toBe(false);
    expect(memberStopAvailable({})).toBe(false);
    expect(memberStopAvailable(null)).toBe(false);
  });

  it("stays offered for a running workspace whose Agent is silent, even if the running read failed", () => {
    expect(memberStopAvailable({ running: false, agent_silent: true })).toBe(true);
    expect(memberStopAvailable({ running: true, agent_silent: true })).toBe(true);
  });
});

describe("diskFigureNote", () => {
  it("states the age of a host-side sample", () => {
    expect(diskFigureNote({ disk_used: 5, disk_observed_at: "2026-10-25T01:00:00Z" })).toEqual({
      kind: "observed",
      at: "2026-10-25T01:00:00Z",
    });
  });
  it("says pending, not 0, while nothing is measured yet", () => {
    expect(diskFigureNote({ disk_pending: true })).toEqual({ kind: "pending" });
  });
  it("has no note for the Agent's own reading or an opted-out poll", () => {
    expect(diskFigureNote({ disk_used: 5 })).toBeNull();
    expect(diskFigureNote({})).toBeNull();
    expect(diskFigureNote(null)).toBeNull();
  });
});
