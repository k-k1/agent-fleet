import { describe, expect, it } from "vitest";
import { acceptedStencilNames, isStencilName, MAX_STENCIL_SETS, stencilUrl } from "./drawioStencilRelay.ts";
import manifest from "../../../../control-plane/assets/drawio-stencils.json";

describe("stencil relay gate", () => {
  it("accepts every key of the CP manifest, so no real diagram loses its icons", () => {
    const keys = Object.keys(manifest.sets);
    expect(keys.length).toBeGreaterThan(100);
    for (const k of keys) {
      expect(isStencilName(k), k).toBe(true);
      expect(stencilUrl(k), k).not.toBeNull();
    }
  });

  it("keeps the resolved URL under the stencils prefix", () => {
    expect(stencilUrl("rack/f5.xml")).toMatch(/\/api\/drawio\/stencils\/rack\/f5\.xml$/);
    for (const bad of ["../a.xml", "a/../../b.xml", "//host/x.xml", "x.xml/../../y.xml", "%2e%2e/a.xml", "a.xml\n"]) {
      expect(stencilUrl(bad), bad).toBeNull();
    }
  });

  it("caps the count and drops duplicates and non-strings", () => {
    const many = Array.from({ length: MAX_STENCIL_SETS + 30 }, (_, i) => `s${i}.xml`);
    expect(acceptedStencilNames(many)).toHaveLength(MAX_STENCIL_SETS);
    expect(acceptedStencilNames(["a.xml", "a.xml", 1, {}])).toEqual(["a.xml"]);
    expect(acceptedStencilNames("a.xml")).toEqual([]);
  });
});
