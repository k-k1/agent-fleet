// Google Cloud regions and their zones, offered by the region and zone pickers in Settings >
// Google Cloud. Static for the same reason as awsRegions.ts: the pickers have to work before
// any login, and a profile carries no credential to list regions with. A region or zone
// missing here is still usable through the pickers' "Other" entry, so the CP and the Agent
// must keep accepting any value of the right shape — never validate against this list.
//
// Source: https://cloud.google.com/compute/docs/regions-zones ("Available regions and
// zones" table, page last updated 2026-09-30, read 2026-10-02). Zone suffixes are recorded
// per region because they are not uniformly a b c (us-central1 has a b c f, us-east1 and
// europe-west1 have b c d).
// Each region has a display name under the i18n key `gcp.region.<code>`.
export const GCP_REGIONS = [
  { code: "africa-south1", zones: ["a", "b", "c"] },
  { code: "asia-east1", zones: ["a", "b", "c"] },
  { code: "asia-east2", zones: ["a", "b", "c"] },
  { code: "asia-northeast1", zones: ["a", "b", "c"] },
  { code: "asia-northeast2", zones: ["a", "b", "c"] },
  { code: "asia-northeast3", zones: ["a", "b", "c"] },
  { code: "asia-south1", zones: ["a", "b", "c"] },
  { code: "asia-south2", zones: ["a", "b", "c"] },
  { code: "asia-southeast1", zones: ["a", "b", "c"] },
  { code: "asia-southeast2", zones: ["a", "b", "c"] },
  { code: "asia-southeast3", zones: ["a", "b", "c"] },
  { code: "australia-southeast1", zones: ["a", "b", "c"] },
  { code: "australia-southeast2", zones: ["a", "b", "c"] },
  { code: "europe-north1", zones: ["a", "b", "c"] },
  { code: "europe-north2", zones: ["a", "b", "c"] },
  { code: "europe-central2", zones: ["a", "b", "c"] },
  { code: "europe-southwest1", zones: ["a", "b", "c"] },
  { code: "europe-west1", zones: ["b", "c", "d"] },
  { code: "europe-west2", zones: ["a", "b", "c"] },
  { code: "europe-west3", zones: ["a", "b", "c"] },
  { code: "europe-west4", zones: ["a", "b", "c"] },
  { code: "europe-west6", zones: ["a", "b", "c"] },
  { code: "europe-west8", zones: ["a", "b", "c"] },
  { code: "europe-west9", zones: ["a", "b", "c"] },
  { code: "europe-west10", zones: ["a", "b", "c"] },
  { code: "europe-west12", zones: ["a", "b", "c"] },
  { code: "me-central1", zones: ["a", "b", "c"] },
  { code: "me-central2", zones: ["a", "b", "c"] },
  { code: "me-west1", zones: ["a", "b", "c"] },
  { code: "northamerica-northeast1", zones: ["a", "b", "c"] },
  { code: "northamerica-northeast2", zones: ["a", "b", "c"] },
  { code: "northamerica-south1", zones: ["a", "b", "c"] },
  { code: "southamerica-east1", zones: ["a", "b", "c"] },
  { code: "southamerica-west1", zones: ["a", "b", "c"] },
  { code: "us-central1", zones: ["a", "b", "c", "f"] },
  { code: "us-east1", zones: ["b", "c", "d"] },
  { code: "us-east4", zones: ["a", "b", "c"] },
  { code: "us-east5", zones: ["a", "b", "c"] },
  { code: "us-south1", zones: ["a", "b", "c"] },
  { code: "us-west1", zones: ["a", "b", "c"] },
  { code: "us-west2", zones: ["a", "b", "c"] },
  { code: "us-west3", zones: ["a", "b", "c"] },
  { code: "us-west4", zones: ["a", "b", "c"] },
] as const satisfies readonly { code: string; zones: readonly string[] }[];

export type GcpRegion = (typeof GCP_REGIONS)[number]["code"];

export const GCP_REGION_CODES: readonly GcpRegion[] = GCP_REGIONS.map((r) => r.code);

const zonesByRegion = new Map<string, readonly string[]>(
  GCP_REGIONS.map((r) => [r.code, r.zones.map((s) => `${r.code}-${s}`)]),
);

const NO_ZONES: readonly string[] = [];

/** The full zone names (`<region>-<suffix>`) of a listed region; none for any other value.
 *  The same array comes back for the same region, so it can be a stable picker option list. */
export function gcpZonesOf(region: string): readonly string[] {
  return zonesByRegion.get(region.trim()) ?? NO_ZONES;
}
