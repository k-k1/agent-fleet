import { describe, it, expect } from "vitest";
import {
  type ScheduleDTO,
  readScheduleList,
  statusTone,
  statusIcon,
  runStatusLabelKey,
  isManualRun,
  scheduleTitle,
  specSummary,
  formatInterval,
  sortSchedules,
} from "./read.ts";

const base: ScheduleDTO = { id: "sch_1", spec_kind: "cron", spec: "0 9 * * *", enabled: true };

describe("statusTone", () => {
  it("maps scheduler status tokens to the four tones", () => {
    expect(statusTone("fired")).toBe("ok");
    expect(statusTone("fired_noop")).toBe("ok");
    expect(statusTone("skipped_quota")).toBe("warn");
    expect(statusTone("skipped_stopped")).toBe("warn");
    expect(statusTone("error:boom")).toBe("danger");
    expect(statusTone("")).toBe("muted");
    expect(statusTone(undefined)).toBe("muted");
  });
});

describe("statusIcon", () => {
  it("picks the codicon per tone", () => {
    expect(statusIcon("fired")).toBe("pass-filled");
    expect(statusIcon("error:x")).toBe("error");
    expect(statusIcon("skipped_quota")).toBe("circle-slash");
    expect(statusIcon("")).toBe("circle-outline");
  });
});

describe("runStatusLabelKey", () => {
  it("maps a run outcome to its friendly label key by tone", () => {
    expect(runStatusLabelKey("fired")).toBe("sched.status_ok");
    expect(runStatusLabelKey("fired_rotated")).toBe("sched.status_ok");
    expect(runStatusLabelKey("skipped_overlap")).toBe("sched.status_skip");
    expect(runStatusLabelKey("error:boom")).toBe("sched.status_fail");
    expect(runStatusLabelKey("")).toBe("sched.status_pending");
    expect(runStatusLabelKey(undefined)).toBe("sched.status_pending");
  });
});

describe("isManualRun", () => {
  it("is true only for a run-now trigger", () => {
    expect(isManualRun("manual")).toBe(true);
    expect(isManualRun("scheduled")).toBe(false);
    expect(isManualRun("")).toBe(false);
    expect(isManualRun(undefined)).toBe(false);
  });
});

describe("scheduleTitle", () => {
  it("prefers the natural-language label", () => {
    expect(scheduleTitle({ ...base, spec_label: "毎朝9時レビュー" })).toBe("毎朝9時レビュー");
  });
  it("falls back to a spec summary when unlabeled", () => {
    expect(scheduleTitle({ ...base, spec_label: "" })).toBe("0 9 * * *");
    expect(scheduleTitle({ ...base, spec_label: "   " })).toBe("0 9 * * *");
  });
});

describe("specSummary", () => {
  it("renders each kind", () => {
    expect(specSummary({ ...base, spec_kind: "cron", spec: "*/15 * * * *" })).toBe("*/15 * * * *");
    // interval goes through i18n (default locale ja), which renders it as "every <interval>".
    expect(specSummary({ ...base, spec_kind: "interval", spec: "3600" })).toBe("1h ごと");
    expect(specSummary({ ...base, spec_kind: "once", spec: "2026-07-24T09:00:00Z" })).toBe("2026-07-24T09:00:00Z");
  });
});

describe("formatInterval", () => {
  it("compacts seconds into d/h/m", () => {
    expect(formatInterval("3600")).toBe("1h");
    expect(formatInterval("5400")).toBe("1h 30m");
    expect(formatInterval("90000")).toBe("1d 1h");
    expect(formatInterval("300")).toBe("5m");
    expect(formatInterval("45")).toBe("45s");
  });
  it("passes odd input through", () => {
    expect(formatInterval("nope")).toBe("nope");
  });
});

describe("sortSchedules", () => {
  it("enabled before paused, then by soonest next_run", () => {
    const a: ScheduleDTO = { ...base, id: "a", enabled: true, next_run: "2026-07-24T09:00:00Z" };
    const b: ScheduleDTO = { ...base, id: "b", enabled: true, next_run: "2026-07-23T09:00:00Z" };
    const c: ScheduleDTO = { ...base, id: "c", enabled: false, next_run: "" };
    const d: ScheduleDTO = { ...base, id: "d", enabled: true, next_run: "" };
    const order = sortSchedules([a, c, b, d]).map((s) => s.id);
    // enabled with next_run soonest-first (b, a), then enabled w/o next_run (d), then paused (c).
    expect(order).toEqual(["b", "a", "d", "c"]);
  });
  it("does not mutate the input array", () => {
    const arr = [base];
    const out = sortSchedules(arr);
    expect(out).not.toBe(arr);
  });
});

// How the GET /api/schedules response is read. api() resolves a CP error as {error} rather
// than throwing, so reading "not an array" as "empty" turns a 401/5xx into "nothing yet".
describe("readScheduleList", () => {
  it("adopts an array payload (including a genuinely empty list)", () => {
    expect(readScheduleList([base])).toEqual({ items: [base], error: null });
    expect(readScheduleList([])).toEqual({ items: [], error: null });
  });

  it("never reads an {error} payload as an empty list", () => {
    const err = { code: "unauthenticated", message: "no gateway identity" };
    const r = readScheduleList({ error: err });
    expect(r.items).toBeNull(); // keep the previous rows: nothing must look deleted
    expect(r.error).toEqual(err); // the caller renders the reason via errText
  });

  it("still fails (items=null) for a shape that is neither (old CP / proxy page)", () => {
    for (const res of [null, undefined, {}, "boom", 0]) {
      expect(readScheduleList(res).items).toBeNull();
    }
  });
});
