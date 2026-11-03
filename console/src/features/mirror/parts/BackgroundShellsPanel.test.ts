import { describe, expect, it } from "vitest";
import { ageMs, elapsedShort, mayBeStuck, parseBackgroundShells } from "./BackgroundShellsPanel.tsx";

const now = Date.parse("2026-11-03T15:00:00+09:00");
const shell = (o: Partial<{ startedAt: string; running: boolean; cpuSec: number }> = {}) => ({
  pid: 200,
  start: 5000,
  command: "cat > /tmp/x",
  startedAt: "2026-11-03T13:00:00+09:00",
  running: false,
  cpuSec: 0.02,
  ...o,
});

describe("parseBackgroundShells", () => {
  it("keeps well-formed entries and drops the rest", () => {
    expect(parseBackgroundShells(undefined)).toEqual([]);
    expect(parseBackgroundShells([shell(), { pid: "x" }, null, { pid: 1, start: 2 }])).toEqual([shell()]);
  });
});

describe("mayBeStuck", () => {
  it("flags a shell waiting 30 min or more with under 1 s of CPU (the #3279 cat)", () => {
    expect(mayBeStuck(shell(), now)).toBe(true);
  });
  it("does not flag a young, a running or a CPU-using shell", () => {
    expect(mayBeStuck(shell({ startedAt: "2026-11-03T14:40:00+09:00" }), now)).toBe(false);
    expect(mayBeStuck(shell({ running: true }), now)).toBe(false);
    expect(mayBeStuck(shell({ cpuSec: 12 }), now)).toBe(false);
  });
  it("does not guess without a start time", () => {
    expect(mayBeStuck(shell({ startedAt: "" }), now)).toBe(false);
    expect(ageMs(shell({ startedAt: "" }), now)).toBeNull();
  });
});

describe("elapsedShort", () => {
  it("folds durations", () => {
    expect(elapsedShort(45_000)).toBe("45s");
    expect(elapsedShort(12 * 60_000)).toBe("12m");
    expect(elapsedShort(2 * 3600_000)).toBe("2h");
    expect(elapsedShort(80 * 60_000)).toBe("1h20m");
  });
});
