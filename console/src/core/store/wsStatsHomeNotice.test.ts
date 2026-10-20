// The home-disk notice: raised once per crossing, only for a member's own volume.
import { describe, it, expect, beforeEach, vi } from "vitest";

const toast = vi.fn(() => true);
vi.mock("../../ui/toast.ts", () => ({ toast: (...a: unknown[]) => toast(...(a as [])) }));
vi.mock("../api/client.ts", () => ({ api: vi.fn(), getTenant: () => "t" }));
vi.mock("../push/events.ts", () => ({ onPush: vi.fn(), pushHealthy: () => true }));

import { __test } from "./wsStatsFeed.ts";

const GIB = 1024 ** 3;
const frame = (usedGiB: number, extra: Record<string, unknown> = {}) => ({
  running: true, home_own: true, disk_used: usedGiB * GIB, disk_total: 100 * GIB, ...extra,
});
const tick = (d: unknown) => {
  __test.setLatest(d);
  __test.sample();
};

beforeEach(() => {
  toast.mockClear();
  toast.mockImplementation(() => true);
  __test.reset();
});

describe("home disk notice", () => {
  it("is raised once when usage crosses the line, not on every sample", () => {
    tick(frame(50));
    tick(frame(98));
    tick(frame(98.5));
    tick(frame(99));
    expect(toast).toHaveBeenCalledTimes(1);
  });
  it("re-arms only after real headroom and then fires again", () => {
    tick(frame(98));
    tick(frame(97.5)); // 2.5 GiB free: still in pressure
    tick(frame(50)); // cleared
    tick(frame(99)); // a new crossing
    expect(toast).toHaveBeenCalledTimes(2);
  });
  it("stays quiet when the volume is not the member's own", () => {
    tick(frame(99, { home_own: undefined }));
    tick(frame(99, { home_own: false }));
    expect(toast).not.toHaveBeenCalled();
  });
  it("does not count a gap in the stream as a recovery", () => {
    tick(frame(98));
    tick(null);
    tick(frame(98));
    expect(toast).toHaveBeenCalledTimes(1);
  });
  it("tries again when no toast host took the first call", () => {
    toast.mockImplementationOnce(() => false);
    tick(frame(98));
    tick(frame(98));
    expect(toast).toHaveBeenCalledTimes(2);
  });
});
