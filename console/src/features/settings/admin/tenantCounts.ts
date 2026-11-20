// The admin Overview's count of tenants by what restricts them (#4446 item 3), as
// GET /api/admin/route-policy/tenant-counts answers it (routepolicysrv tenantCountsWire). Numbers
// only. Anything that is not three plain counts reads as null, which the page words as "could not
// be read": a zero must never come from a damaged answer.
export interface TenantCounts {
  tenants: number;
  withCap: number;
  withOwnRestriction: number;
}

const isCount = (v: unknown): v is number => typeof v === "number" && Number.isInteger(v) && v >= 0;

export function parseTenantCounts(d: unknown): TenantCounts | null {
  if (!d || typeof d !== "object") return null;
  const o = d as Record<string, unknown>;
  if (!isCount(o.tenants) || !isCount(o.withCap) || !isCount(o.withOwnRestriction)) return null;
  // A tenant is counted in a layer only where it exists.
  if (o.withCap > o.tenants || o.withOwnRestriction > o.tenants) return null;
  return { tenants: o.tenants, withCap: o.withCap, withOwnRestriction: o.withOwnRestriction };
}
