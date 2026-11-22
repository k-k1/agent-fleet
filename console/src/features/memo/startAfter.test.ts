// A start time a note carries (ADR 0137, addendum 2026-11-22): the datetime-local round trip in the
// member's time zone, when a row shows it, and the words for a queue that waits for one.
import { afterEach, describe, expect, it } from "vitest";
import { setFormatPrefs } from "../../lib/intl.ts";
import { readMemoQueues, scheduledUntil, startAfterFromInput, startAfterInputValue, waitingWords } from "./queues.ts";

afterEach(() => setFormatPrefs("auto", "auto"));

describe("the start time input", () => {
  it("reads and writes a wall time of the member's zone, not the browser's", () => {
    setFormatPrefs("en-US", "Asia/Tokyo");
    expect(startAfterInputValue("2026-12-04T00:00:00.000000Z")).toBe("2026-12-04T09:00");
    expect(startAfterFromInput("2026-12-04T09:00")).toBe("2026-12-04T00:00:00.000Z");
    expect(startAfterInputValue(undefined)).toBe("");
    expect(startAfterFromInput("")).toBe("");
    // Unreadable is not "no start time": a value the browser takes but this cannot read is refused.
    expect(startAfterFromInput("tomorrow")).toBeNull();
    expect(startAfterFromInput("+12345-01-01T09:00")).toBeNull();
  });

  it("takes the offset of the day it names, across a DST change", () => {
    setFormatPrefs("en-US", "America/New_York");
    // Winter (UTC-5) and summer (UTC-4): the offset of now would be wrong for one of them.
    expect(startAfterFromInput("2026-01-15T09:00")).toBe("2026-01-15T14:00:00.000Z");
    expect(startAfterFromInput("2026-07-15T09:00")).toBe("2026-07-15T13:00:00.000Z");
    // The morning the clocks go forward (2026-03-08, 02:00 EST -> 03:00 EDT, at 07:00Z): 05:00 is
    // EDT, but the first guess (05:00Z) still reads EST, so one lookup alone lands an hour late.
    expect(startAfterFromInput("2026-03-08T05:00")).toBe("2026-03-08T09:00:00.000Z");
    expect(startAfterInputValue("2026-03-08T09:00:00.000Z")).toBe("2026-03-08T05:00");
  });

  it("reads and writes in an explicit zone when given one", () => {
    setFormatPrefs("en-US", "Asia/Tokyo");
    expect(startAfterFromInput("2026-12-04T09:00", "America/New_York")).toBe("2026-12-04T14:00:00.000Z");
    expect(startAfterInputValue("2026-12-04T14:00:00.000Z", "America/New_York")).toBe("2026-12-04T09:00");
  });

  it("refuses a wall time the clocks skip, and takes the later of one they repeat", () => {
    setFormatPrefs("en-US", "America/New_York");
    // 2026-03-08 02:00-02:59 does not exist there.
    expect(startAfterFromInput("2026-03-08T02:30")).toBeNull();
    // 2026-11-01 01:30 happens twice (EDT 05:30Z, then EST 06:30Z): wait for the second.
    expect(startAfterFromInput("2026-11-01T01:30")).toBe("2026-11-01T06:30:00.000Z");
    expect(startAfterFromInput("2026-11-01T00:30")).toBe("2026-11-01T04:30:00.000Z");
    expect(startAfterFromInput("2026-11-01T02:30")).toBe("2026-11-01T07:30:00.000Z");
  });
});

describe("scheduledUntil", () => {
  const now = Date.parse("2026-11-22T00:00:00Z");
  it("is the time while it is ahead, on a held or waiting note", () => {
    expect(scheduledUntil({ state: "pending", startAfter: "2026-12-04T00:00:00Z" }, now)).toBe("2026-12-04T00:00:00Z");
    expect(scheduledUntil({ state: "held", startAfter: "2026-12-04T00:00:00Z" }, now)).toBe("2026-12-04T00:00:00Z");
    expect(scheduledUntil({ state: "delivered", startAfter: "2026-12-04T00:00:00Z" }, now)).toBeNull();
    expect(scheduledUntil({ state: "pending", startAfter: "2026-11-01T00:00:00Z" }, now)).toBeNull();
    expect(scheduledUntil({ state: "pending" }, now)).toBeNull();
  });
});

describe("a queue that waits for start times", () => {
  const read = (waiting: unknown) => readMemoQueues({ queues: [{ categoryId: "c1", waiting }] })!.queues[0];
  const at = (iso: string) => `<${iso}>`;
  const kind = (k: string) => `kind:${k}`;
  it("names the reason and the next time", () => {
    expect(waitingWords(read({ reason: "scheduled", until: "2026-12-04T00:00:00Z", detail: { scheduled: 2 } }), at, kind)).toEqual({
      key: "memo.waiting_scheduled_at",
      params: { at: "<2026-12-04T00:00:00Z>", count: 2 },
    });
    expect(waitingWords(read({ reason: "scheduled", detail: { scheduled: 1 } }), at, kind)).toEqual({
      key: "memo.waiting_scheduled",
      params: { count: 1 },
    });
  });
  it("says start time or hand-back when both kinds wait", () => {
    expect(waitingWords(read({ reason: "deferred", until: "2026-12-04T00:00:00Z", detail: { deferred: 1, scheduled: 1 } }), at, kind)).toEqual({
      key: "memo.waiting_timed_at",
      params: { at: "<2026-12-04T00:00:00Z>", count: 2 },
    });
    expect(waitingWords(read({ reason: "deferred", until: "2026-12-04T00:00:00Z", detail: { deferred: 1 } }), at, kind)?.key).toBe("memo.waiting_deferred_at");
    expect(waitingWords(read({ reason: "below_threshold", detail: { needed: 3, waiting: 1, blocked: 1, scheduled: 2 } }), at, kind)).toEqual({
      key: "memo.waiting_below_threshold_blocked_timed",
      params: { needed: 3, count: 1, blocked: 1, timed: 2 },
    });
    expect(waitingWords(read({ reason: "below_threshold", detail: { needed: 3, waiting: 1, scheduled: 1, deferred: 1 } }), at, kind)?.key).toBe(
      "memo.waiting_below_threshold_timed",
    );
  });

  it("counts the timed ones beside the blocked ones", () => {
    expect(waitingWords(read({ reason: "prerequisites", detail: { blocked: 1, deferred: 1, scheduled: 2 } }), at, kind)).toEqual({
      key: "memo.waiting_prerequisites_timed",
      params: { count: 1, timed: 3 },
    });
  });
});
