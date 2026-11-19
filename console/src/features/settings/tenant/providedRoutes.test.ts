// The provided-route month's reset (ADR 0129 D4): the first of the next month, 00:00 UTC.
import { describe, it, expect } from "vitest";
import { parseProvidedTenant, resetOf, sourceState, type ProvidedTenantView } from "./providedRoutes.ts";

describe("resetOf", () => {
  it.each([
    ["2026-11", "2026-12-01T00:00:00.000Z"],
    ["2026-12", "2027-01-01T00:00:00.000Z"],
    ["2027-01", "2027-02-01T00:00:00.000Z"],
  ])("%s resets at %s", (month, at) => {
    expect(resetOf(month)?.toISOString()).toBe(at);
  });

  it("shows no reset for a month it cannot read", () => {
    for (const bad of ["", "2026-13", "2026-00", "2026-1", "2026-11-01", "Nov 2026"]) expect(resetOf(bad), bad).toBeNull();
  });
});

describe("source state", () => {
  const base = {
    granted: true,
    ceiling_micro: null,
    members_only: false,
    member_default_micro: 0,
    month: "2026-11",
    spent_micro: 0,
    reserved_micro: 0,
    members: [],
    reservations: [],
    floor_micro: 0,
  };
  const st = (extra: object) => sourceState({ ...base, ...extra } as ProvidedTenantView);

  it("reads the two fields, and an older CP's absence as unknown, not off", () => {
    expect(st({})).toBe("unknown");
    expect(st({ source_registered: true })).toBe("unknown");
    expect(st({ source_active: true })).toBe("unknown");
    expect(st({ source_registered: true, source_active: true })).toBe("on");
    expect(st({ source_registered: true, source_active: false })).toBe("off");
    expect(st({ source_registered: false, source_active: false })).toBe("unregistered");
    expect(st({ source_registered: false, source_active: true })).toBe("unregistered");
  });

  it("refuses a view whose source fields are not booleans", () => {
    expect(parseProvidedTenant(base)).not.toBeNull();
    expect(parseProvidedTenant({ ...base, source_registered: true, source_active: false })).not.toBeNull();
    expect(parseProvidedTenant({ ...base, source_registered: "yes" })).toBeNull();
    expect(parseProvidedTenant({ ...base, source_active: 1 })).toBeNull();
  });
});
