// sameRuns keeps the held array only while no field of any row changed: a run is written in steps,
// and a Google Cloud run's identity (project, account) arrives without touching account or endedAt.
import { describe, expect, it } from "vitest";
import { sameRuns } from "./cloudRunsModel.ts";
import type { CloudRun } from "./cloudRunsModel.ts";

describe("sameRuns", () => {
  const r: CloudRun = { id: "1", provider: "gcp", startedAt: "2026-10-30T12:00:00Z", profile: "dev", program: "gcloud" };
  it("keeps the held rows when nothing changed", () => {
    const held = [r];
    expect(sameRuns(held, [{ ...r }])).toBe(held);
  });
  it("takes the new rows when only a Google Cloud identity was added", () => {
    const next = [{ ...r, project: "p1", principal: "me@example.com" }];
    expect(sameRuns([r], next)).toBe(next);
  });
});
