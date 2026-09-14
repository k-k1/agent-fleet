import { describe, expect, it } from "vitest";
import { fleetProvider, fleetProviders, resolveFleetProvider, type ImagegenProvider, type ImagegenStatus } from "./wire.ts";

// ADR 0082 P0's regression (fixed in fleetProvider itself, pinned again here so a future
// reader does not reach for id-matching by habit): `fleet` decides, never the id's spelling.
// Every fixture below is deliberately keyed the way a real deployment's default row is
// ("image", not "comfy") for the same reason.
describe("fleetProvider / fleetProviders", () => {
  const st = (providers: ImagegenProvider[]): ImagegenStatus => ({ enabled: true, ready: true, providers });

  it("fleetProvider finds the first row whose `fleet` flag is set, never by id", () => {
    expect(fleetProvider(st([{ id: "codex" }, { id: "image", fleet: true }]))?.id).toBe("image");
    expect(fleetProvider(st([{ id: "codex" }]))).toBeNull();
    expect(fleetProvider(null)).toBeNull();
  });

  it("fleetProviders is every ready fleet row, vendor routes excluded", () => {
    const rows = fleetProviders(st([{ id: "codex" }, { id: "image", fleet: true }, { id: "comfy-lan", fleet: true }]));
    expect(rows.map((p) => p.id)).toEqual(["image", "comfy-lan"]);
  });

  it("fleetProviders is empty, not undefined, for a status with none", () => {
    expect(fleetProviders(st([{ id: "codex" }, { id: "agy" }]))).toEqual([]);
    expect(fleetProviders(null)).toEqual([]);
  });
});

// resolveFleetProvider is the studio pane's answer to ADR 0082's unresolved question 2: which
// of N fleet rows drives the form. Pure logic, deliberately extracted out of GenerateForm/
// ImagegenView so this can be pinned without a DOM render — a controlled <select> whose value
// matches no <option> falls back to showing the first option ANYWAY (a jsdom/browser quirk),
// which would silently pass a render test even if this function's own fallback broke.
describe("resolveFleetProvider（ADR 0082 未解決 2）", () => {
  const image: ImagegenProvider = { id: "image", fleet: true, kind: "comfy" };
  const lan: ImagegenProvider = { id: "comfy-lan", fleet: true, kind: "comfy" };

  it("no explicit choice: the first row, exactly what every single-engine deployment already saw", () => {
    expect(resolveFleetProvider([image, lan], "")).toBe(image);
    expect(resolveFleetProvider([image], "")).toBe(image);
  });

  it("an explicit, still-valid choice wins over the first row", () => {
    expect(resolveFleetProvider([image, lan], "comfy-lan")).toBe(lan);
  });

  it("a stale choice (the row was removed, or a different deployment's draft) reads as no choice", () => {
    expect(resolveFleetProvider([image, lan], "gone")).toBe(image);
  });

  it("zero rows resolves to null, not a throw", () => {
    expect(resolveFleetProvider([], "image")).toBeNull();
  });
});
