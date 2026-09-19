import { afterEach, describe, expect, it } from "vitest";
import {
  catalogKey,
  clearCatalogMemory,
  loadEngines,
  loadObjects,
  loadRegistered,
  loadSearch,
  loadShell,
  saveEngines,
  saveObjects,
  saveRegistered,
  saveSearch,
  saveShell,
  type CatalogSearchMemory,
} from "./catalogMemory.ts";

const page = (name: string): CatalogSearchMemory => ({
  source: "civitai", sort: "newest", family: "", query: name, submittedQuery: name,
  hits: [{ source: "civitai", ref: "22", name }], cursor: "next",
});

afterEach(() => clearCatalogMemory());

describe("catalogue memory keys", () => {
  // Every part of the key names something that makes it a DIFFERENT list. Two of them were the
  // faults this memory has to avoid: one pane deciding what another shows, and the LoRA tab
  // showing the checkpoints' page.
  it("tells panes, engines, faces and kinds apart", () => {
    const keys = [
      catalogKey("pane-a", "image", "search", "model"),
      catalogKey("pane-b", "image", "search", "model"),
      catalogKey("pane-a", "llm", "search", "model"),
      catalogKey("pane-a", "image", "registered", "model"),
      catalogKey("pane-a", "image", "search", "lora"),
    ];
    expect(new Set(keys).size).toBe(keys.length);
  });

  // The pane the settings panel opens has no id. Those mounts share one memory, which is a
  // property worth stating: it is why tests have to clear it between cases.
  it("folds a missing pane id onto one key", () => {
    expect(catalogKey(undefined, "image", "search", "model")).toBe(catalogKey("", "image", "search", "model"));
  });
});

describe("catalogue memory contents", () => {
  it("hands a page back to the same key and nothing to another", () => {
    const key = catalogKey("pane-a", "image", "search", "model");
    saveSearch(key, page("flux"));
    expect(loadSearch(key)?.hits?.[0].name).toBe("flux");
    expect(loadSearch(catalogKey("pane-b", "image", "search", "model"))).toBeNull();
  });

  it("keeps the registered face's filter, including an open family of its own", () => {
    const key = catalogKey("pane-a", "image", "registered", "model");
    // 🔴 `""` is the group of rows that declare NO family and `null` is "all of them" — the two
    // must survive as themselves, because merging them is what drew both chips selected.
    saveRegistered(key, { query: "meina", sort: "enabled", family: "" });
    expect(loadRegistered(key)).toEqual({ query: "meina", sort: "enabled", family: "" });
    saveRegistered(key, { query: "", sort: "name", family: null });
    expect(loadRegistered(key)?.family).toBeNull();
  });

  it("remembers which face a pane was on, and the bucket per engine", () => {
    saveShell("pane-a", { engineKey: "llm", view: "registered", kind: "lora" });
    expect(loadShell("pane-a")).toEqual({ engineKey: "llm", view: "registered", kind: "lora" });
    saveObjects("image", {
      objects: [{ key: "image/checkpoints/a.safetensors", state: "present", role_dir: "checkpoints", placement: "ok" }],
      checkedAt: "t",
    });
    expect(loadObjects("image")?.objects).toHaveLength(1);
    // The bucket belongs to the engine, not to whoever looked at it.
    expect(loadObjects("llm")).toBeNull();
  });

  it("drops the oldest entry rather than growing without a bound", () => {
    const first = catalogKey("pane-0", "image", "search", "model");
    saveSearch(first, page("first"));
    for (let i = 1; i <= 40; i++) saveSearch(catalogKey(`pane-${i}`, "image", "search", "model"), page(`p${i}`));
    expect(loadSearch(first)).toBeNull();
    expect(loadSearch(catalogKey("pane-40", "image", "search", "model"))?.hits?.[0].name).toBe("p40");
  });

  it("forgets everything on clear, engine list included", () => {
    const key = catalogKey("pane-a", "image", "search", "model");
    saveSearch(key, page("flux"));
    saveShell("pane-a", { engineKey: "image", view: "search", kind: "model" });
    saveEngines({ rows: [{ key: "image" }], isSuper: true, sources: ["civitai"] });
    expect(loadEngines()?.rows).toHaveLength(1);
    clearCatalogMemory();
    expect(loadSearch(key)).toBeNull();
    expect(loadShell("pane-a")).toBeNull();
    expect(loadEngines()).toBeNull();
  });
});
