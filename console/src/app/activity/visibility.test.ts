import { describe, expect, it } from "vitest";
import { canResetOff, hiddenWaiting, menuEntries, resetOff, shownViews, toggleOff } from "./visibility.ts";
import type { RailView } from "../../core/store/leftRail.ts";

const ALL: RailView[] = ["sessions", "shared", "terminals", "assistant", "workitems", "memo", "autorun", "memory", "schedules", "files"];
const NO_SCHED = ALL.filter((v) => v !== "schedules");

describe("shownViews", () => {
  it("drops the views turned off and keeps the rest in bar order", () => {
    expect(shownViews(NO_SCHED, ["shared", "memory"], "sessions")).toEqual(["sessions", "terminals", "assistant", "workitems", "memo", "autorun", "files"]);
  });
  it("keeps the view on screen even when its icon is stored as off", () => {
    expect(shownViews(NO_SCHED, ["shared"], "shared")).toContain("shared");
  });
  it("never ends empty while a view is usable", () => {
    expect(shownViews(["sessions", "files"], ["sessions", "files"], "assistant")).toEqual(["sessions"]);
    expect(shownViews([], [], "sessions")).toEqual([]);
  });
  it("ignores ids this build does not know", () => {
    expect(shownViews(NO_SCHED, ["future-view"], "sessions")).toEqual(NO_SCHED);
  });
});

const find = (e: ReturnType<typeof menuEntries>, id: RailView) => e.find((x) => x.id === id)!;

describe("menuEntries", () => {
  it("checks what is shown and locks only the view on screen", () => {
    const e = menuEntries(ALL, NO_SCHED, ["memory"], "sessions");
    expect(find(e, "sessions")).toMatchObject({ checked: true, lock: "current" });
    expect(find(e, "memory")).toMatchObject({ checked: false, lock: null });
    expect(find(e, "files")).toMatchObject({ checked: true, lock: null });
  });
  it("lists a view the deployment cannot use as unchecked and locked", () => {
    expect(find(menuEntries(ALL, NO_SCHED, [], "sessions"), "schedules")).toEqual({ id: "schedules", checked: false, lock: "unavailable" });
  });
  it("locks the last icon", () => {
    // Nothing usable is on screen and only Files is left on.
    const e = menuEntries(["sessions", "files"], ["sessions", "files"], ["sessions"], "assistant");
    expect(find(e, "files")).toMatchObject({ checked: true, lock: "last" });
    expect(find(e, "sessions")).toMatchObject({ checked: false, lock: null });
  });
  it("lets an icon that is off be turned on", () => {
    expect(find(menuEntries(ALL, NO_SCHED, ["files"], "sessions"), "files").lock).toBeNull();
  });
});

describe("toggleOff", () => {
  it("adds and removes one id, keeping ids it does not know", () => {
    expect(toggleOff(["future"], "files", false)).toEqual(["future", "files"]);
    expect(toggleOff(["future", "files"], "files", true)).toEqual(["future"]);
    expect(toggleOff(["files"], "files", false)).toEqual(["files"]);
  });
});

describe("hiddenWaiting", () => {
  const shown: RailView[] = ["sessions"];
  it("names a hidden view with a waiting badge, and nothing else", () => {
    const out = hiddenWaiting(NO_SCHED, shown, {
      memo: { tone: "waiting", count: 2 },
      workitems: { tone: "news", count: 3 },
      assistant: { tone: "unread" },
      autorun: { tone: "failed" },
      sessions: { tone: "waiting", count: 1 },
    });
    expect(out.map((o) => o.id)).toEqual(["memo"]);
  });
  it("ignores a view that is not usable", () => {
    expect(hiddenWaiting(NO_SCHED, shown, { schedules: { tone: "waiting", count: 1 } })).toEqual([]);
  });
});

describe("resetOff", () => {
  it("clears the ids this build knows and keeps the rest", () => {
    expect(resetOff(["future", "files", "memory"])).toEqual(["future"]);
    expect(resetOff(["future"])).toEqual(["future"]);
    expect(resetOff([])).toEqual([]);
  });
  it("is worth offering only when a known id is off", () => {
    expect(canResetOff(["future"])).toBe(false);
    expect(canResetOff(["future", "files"])).toBe(true);
    expect(canResetOff([])).toBe(false);
  });
});

describe("a folded left pane (nothing on screen)", () => {
  it("lets any icon go, down to the last one", () => {
    expect(shownViews(NO_SCHED, ["sessions"], null)).not.toContain("sessions");
    expect(find(menuEntries(ALL, NO_SCHED, [], null), "sessions").lock).toBeNull();
    const only = menuEntries(["sessions", "files"], ["sessions", "files"], ["files"], null);
    expect(find(only, "sessions")).toMatchObject({ checked: true, lock: "last" });
  });
});
