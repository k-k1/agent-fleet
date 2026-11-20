import { describe, it, expect } from "vitest";
import { parseTenantCounts } from "./tenantCounts.ts";

describe("parseTenantCounts", () => {
  it("reads three counts", () => {
    expect(parseTenantCounts({ tenants: 4, withCap: 2, withOwnRestriction: 1 })).toEqual({
      tenants: 4,
      withCap: 2,
      withOwnRestriction: 1,
    });
  });
  it("is null for anything else, so a damaged answer never reads as zero", () => {
    for (const bad of [
      null,
      undefined,
      "x",
      [],
      {},
      { error: { code: "forbidden" } },
      { tenants: "4", withCap: 0, withOwnRestriction: 0 },
      { tenants: 4, withCap: -1, withOwnRestriction: 0 },
      { tenants: 4, withCap: 1.5, withOwnRestriction: 0 },
      { tenants: 4, withCap: 5, withOwnRestriction: 0 },
      { tenants: 4, withCap: 0 },
    ])
      expect(parseTenantCounts(bad), JSON.stringify(bad)).toBeNull();
  });
});
