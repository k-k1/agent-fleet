import { describe, expect, it } from "vitest";
import { GCP_REGIONS, GCP_REGION_CODES, gcpZonesOf } from "./gcpRegions.ts";
import { tools as en } from "./i18n/locales/en/tools.ts";
import { tools as ja } from "./i18n/locales/ja/tools.ts";

describe("GCP_REGIONS", () => {
  it("names every listed region in both catalogues", () => {
    for (const code of GCP_REGION_CODES) {
      const key = `gcp.region.${code}` as keyof typeof ja;
      expect(ja[key], code).toBeTruthy();
      expect(en[key], code).toBeTruthy();
    }
    expect(new Set(GCP_REGION_CODES).size).toBe(GCP_REGIONS.length);
  });

  it("keeps each region's own zone suffixes", () => {
    expect(gcpZonesOf("us-central1")).toEqual(["us-central1-a", "us-central1-b", "us-central1-c", "us-central1-f"]);
    expect(gcpZonesOf("europe-west1")).toEqual(["europe-west1-b", "europe-west1-c", "europe-west1-d"]);
    expect(gcpZonesOf("us-central1")).toBe(gcpZonesOf("us-central1"));
    expect(gcpZonesOf("")).toEqual([]);
    expect(gcpZonesOf("us-east7")).toEqual([]);
  });
});
