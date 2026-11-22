import { describe, expect, it } from "vitest";
import * as registry from "./modelFallbacks.ts";

describe("modelFallbacks", () => {
  it("documents every pinned id it exports", () => {
    const ids = (Object.values(registry) as unknown[]).filter((v): v is string => typeof v === "string");
    expect(ids.length).toBeGreaterThan(0);
    expect(registry.MODEL_FALLBACKS.map((e) => e.id).sort()).toEqual([...ids].sort());
    for (const e of registry.MODEL_FALLBACKS) {
      for (const field of [e.id, e.kind, e.owner, e.source, e.whyNotDiscovered]) expect(field.trim()).not.toBe("");
    }
  });
});
