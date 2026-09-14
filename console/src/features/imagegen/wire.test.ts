import { describe, expect, it } from "vitest";
import { fleetProvider, fleetProviders, isPreAdr0082Status, resolveFleetProvider, type ImagegenProvider, type ImagegenStatus } from "./wire.ts";

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

// isPreAdr0082Status is the settings screen's generation check: an Agent build old enough to
// predate `kind` must be read as "no live answer" (fall back to the static list), never as "this
// deployment declares zero fleet rows" — which is what happens if the check reads `fleet`
// instead (a current Agent with nothing ready also omits `fleet` on every entry, Go's
// `omitempty`, so `fleet` alone cannot tell the two shapes apart).
describe("isPreAdr0082Status", () => {
  it("providers with no `kind` at all — the pre-ADR-0082 shape — reads as old", () => {
    expect(isPreAdr0082Status([{ id: "codex" }, { id: "agy" }])).toBe(true);
    expect(isPreAdr0082Status([{ id: "image", fleet: true }])).toBe(true);
  });

  it("a current Agent stamps `kind` on EVERY entry, vendor routes included — reads as current", () => {
    expect(isPreAdr0082Status([{ id: "codex", kind: "codex" }, { id: "agy", kind: "agy" }])).toBe(false);
    expect(isPreAdr0082Status([{ id: "image", fleet: true, kind: "comfy" }, { id: "codex", kind: "codex" }])).toBe(false);
  });

  it("one entry missing `kind` among others that have it is NOT the old shape (a mixed answer never happens, but must not false-positive)", () => {
    expect(isPreAdr0082Status([{ id: "image", fleet: true, kind: "comfy" }, { id: "codex" }])).toBe(false);
  });

  it("zero providers is not the old shape — 'nothing is ready' is a real, different answer", () => {
    expect(isPreAdr0082Status([])).toBe(false);
  });
});
