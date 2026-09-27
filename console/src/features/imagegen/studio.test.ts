import { describe, expect, it } from "vitest";
import { STUDIO_AGENT_FIELDS, studioNeedsMask } from "./wire.ts";

// Pure readers of the studio wire (ADR 0100). Kept out of wire.test.ts's fixtures and away from
// api.ts, which the node project cannot load.
describe("studioNeedsMask", () => {
  it("is op=inpaint with no mask, and nothing else", () => {
    expect(studioNeedsMask({ op: "inpaint" })).toBe(true);
    expect(studioNeedsMask({ op: "inpaint", mask: "  " })).toBe(true);
    expect(studioNeedsMask({ op: "inpaint", mask: "generated/console/inputs/m.png" })).toBe(false);
    expect(studioNeedsMask({ op: "edit" })).toBe(false);
    expect(studioNeedsMask({})).toBe(false);
  });
});

describe("STUDIO_AGENT_FIELDS", () => {
  it("leaves the member's fields to the member (decision 4)", () => {
    for (const k of ["model", "seed", "seed_policy", "jobs", "count", "out_dir", "label", "mask"]) {
      expect(STUDIO_AGENT_FIELDS as readonly string[]).not.toContain(k);
    }
  });
});
