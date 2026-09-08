import { describe, it, expect } from "vitest";
import { authResolved, carryEnd, endOf, footTime } from "./turnTime.ts";

describe("endOf", () => {
  it("takes the agent-reported end when the row IS a whole turn (opencode/copilot)", () => {
    expect(endOf({ ts: "2026-08-04T10:00:00Z", endTs: "2026-08-04T10:07:00Z" })).toBe("2026-08-04T10:07:00Z");
  });
  it("falls back to the row's own ts when the turn spans many rows (claude/codex)", () => {
    expect(endOf({ ts: "2026-08-04T10:00:00Z" })).toBe("2026-08-04T10:00:00Z");
  });
  it("is empty when the agent records no time at all (cursor/kiro/agy)", () => {
    expect(endOf({})).toBe("");
  });
});

describe("carryEnd", () => {
  it("advances the block's end to the last folded row, keeping the start", () => {
    // One claude turn: thinking -> tool call -> final text. The footer used to show 10:00 (the
    // first row) here.
    const block = { ts: "2026-08-04T10:00:00Z", endTs: "2026-08-04T10:00:00Z" };
    carryEnd(block, { ts: "2026-08-04T10:03:00Z" });
    carryEnd(block, { ts: "2026-08-04T10:07:30Z" });
    expect(block.endTs).toBe("2026-08-04T10:07:30Z");
    expect(block.ts).toBe("2026-08-04T10:00:00Z"); // the ordering key stays on the first row
  });
  it("keeps the previous end when the folded row carries no time", () => {
    const block = { ts: "2026-08-04T10:00:00Z", endTs: "2026-08-04T10:03:00Z" };
    carryEnd(block, {});
    expect(block.endTs).toBe("2026-08-04T10:03:00Z");
  });
  it("gives a timeless block an end as soon as one folded row has a time", () => {
    const block: { ts?: string; endTs?: string } = {};
    carryEnd(block, { ts: "2026-08-04T10:03:00Z" });
    expect(footTime(block)).toBe("2026-08-04T10:03:00Z");
  });
});

describe("footTime", () => {
  it("shows the end, not the start", () => {
    expect(footTime({ ts: "2026-08-04T10:00:00Z", endTs: "2026-08-04T10:07:00Z" })).toBe("2026-08-04T10:07:00Z");
  });
  it("falls back to the start while a turn is still running (no end yet)", () => {
    expect(footTime({ ts: "2026-08-04T10:00:00Z" })).toBe("2026-08-04T10:00:00Z");
  });
  it("renders nothing when the agent records no time", () => {
    expect(footTime({})).toBe("");
  });
});

// The rule the mirror's auth error block flips on (docs/log/47 §4-11). The two moments come
// from the same Workspace — the Agent stats the credential file, the agent writes the
// transcript — so this really is a comparison and not a guess across clocks.
describe("authResolved", () => {
  const turnEnd = "2026-08-14T03:00:00Z";

  it("says yes when the login in force was written after the turn it killed", () => {
    expect(authResolved("2026-08-14T03:00:01Z", turnEnd)).toBe(true);
  });

  it("says no while the login is the one that failed", () => {
    expect(authResolved("2026-08-14T02:59:59Z", turnEnd)).toBe(false);
    expect(authResolved(turnEnd, turnEnd)).toBe(false); // not newer = not renewed
  });

  it("says no when either side is missing or unparsable (keep offering the fix)", () => {
    // Failing this way costs the reader a look at a settings card that turns out to be fine.
    // The other way tells them a broken login has been dealt with.
    expect(authResolved(undefined, turnEnd)).toBe(false);
    expect(authResolved("2026-08-14T04:00:00Z", undefined)).toBe(false);
    expect(authResolved("", turnEnd)).toBe(false);
    expect(authResolved("not-a-time", turnEnd)).toBe(false);
    expect(authResolved("2026-08-14T04:00:00Z", "not-a-time")).toBe(false);
  });

  it("compares instants, not text (a local-offset stamp against a UTC one)", () => {
    // The Agent formats RFC3339 in the container's local zone; the transcript can carry Z.
    expect(authResolved("2026-08-14T12:30:00+09:00", "2026-08-14T03:00:00Z")).toBe(true);
    expect(authResolved("2026-08-14T11:30:00+09:00", "2026-08-14T03:00:00Z")).toBe(false);
  });
});
