import { describe, expect, it } from "vitest";
import { wsDiskTile, wsLoadTile } from "./wsTiles.ts";

describe("wsLoadTile", () => {
  it("renders from load1 + ncpu and normalizes to cores", () => {
    expect(wsLoadTile({ load1: 1.5, ncpu: 2 })).toEqual({
      load1: 1.5,
      ncpu: 2,
      norm: 0.75,
    });
  });
  it("renders nothing when load1 is absent (shared hosts: the CP drops it)", () => {
    expect(wsLoadTile({ running: true, mem_used: 1 })).toBeNull();
    expect(wsLoadTile(null)).toBeNull();
  });
  it("renders nothing without a usable core count", () => {
    expect(wsLoadTile({ load1: 1 })).toBeNull();
    expect(wsLoadTile({ load1: 1, ncpu: 0 })).toBeNull();
  });
  it("keeps a real zero load", () => {
    expect(wsLoadTile({ load1: 0, ncpu: 2 })?.norm).toBe(0);
  });
});

describe("wsDiskTile", () => {
  it("levels apply only on the member's own volume", () => {
    expect(wsDiskTile({ disk_used: 95, disk_total: 100, home_own: true })?.leveled).toBe(true);
    expect(wsDiskTile({ disk_used: 95, disk_total: 100 })?.leveled).toBe(false);
  });
  it("renders nothing when disk_total or disk_used is missing or zero", () => {
    expect(wsDiskTile({ disk_used: 5 })).toBeNull();
    expect(wsDiskTile({ disk_total: 5 })).toBeNull();
    expect(wsDiskTile({ disk_used: 0, disk_total: 0 })).toBeNull();
    expect(wsDiskTile(null)).toBeNull();
  });
  it("keeps zero usage", () => {
    expect(wsDiskTile({ disk_used: 0, disk_total: 10 })?.ratio).toBe(0);
  });
});

describe("non-finite and negative readings render nothing", () => {
  it("load", () => {
    for (const bad of [NaN, Infinity, -1]) expect(wsLoadTile({ load1: bad, ncpu: 2 })).toBeNull();
    for (const bad of [NaN, Infinity, -2]) expect(wsLoadTile({ load1: 1, ncpu: bad })).toBeNull();
  });
  it("disk", () => {
    for (const bad of [NaN, Infinity, -5]) expect(wsDiskTile({ disk_used: bad, disk_total: 10 })).toBeNull();
    for (const bad of [NaN, Infinity]) expect(wsDiskTile({ disk_used: 1, disk_total: bad })).toBeNull();
  });
});
