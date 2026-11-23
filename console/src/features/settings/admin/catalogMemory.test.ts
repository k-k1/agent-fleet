import { afterEach, describe, expect, it } from "vitest";
import type { EngineRow } from "./engineTypes.ts";
import {
  beginEnginesEdit,
  beginEnginesRead,
  catalogKey,
  clearCatalogMemory,
  loadEngines,
  loadObjects,
  loadRegistered,
  loadSearch,
  loadShell,
  saveEngines,
  saveEnginesRead,
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

// The engine list is one slot written by every screen's useEngineRows, each ordering its own reads.
// The order that keeps a stale answer out of the slot lives beside it.
describe("engine cache write order across screens", () => {
  const eng = (key: string, mode: string) => ({ key, mode }) as unknown as EngineRow;
  const seed = () => saveEngines({ rows: [eng("a", "ondemand"), eng("b", "ondemand")], isSuper: true, sources: ["hf"] });
  const modes = () => loadEngines()?.rows.map((e) => `${e.key}:${(e as unknown as { mode: string }).mode}`);

  it("keeps an edited row when a read begun before the edit answered saves its older copy", () => {
    seed();
    const read = beginEnginesRead(); // screen B
    beginEnginesEdit("a")({ mode: "on" } as Partial<EngineRow>); // screen A's edit answers
    saveEnginesRead(read, [eng("a", "ondemand"), eng("b", "ondemand")], true, ["hf"]);
    expect(modes()).toEqual(["a:on", "b:ondemand"]);
  });

  it("still lands the other rows of that read", () => {
    seed();
    const read = beginEnginesRead();
    beginEnginesEdit("a")({ mode: "on" } as Partial<EngineRow>);
    saveEnginesRead(read, [eng("a", "ondemand"), eng("b", "on")], true, ["hf"]);
    expect(modes()).toEqual(["a:on", "b:on"]);
  });

  it("takes a read begun after the edit answered", () => {
    seed();
    beginEnginesEdit("a")({ mode: "on" } as Partial<EngineRow>);
    saveEnginesRead(beginEnginesRead(), [eng("a", "on"), eng("b", "ondemand")], true, ["hf"]);
    expect(modes()).toEqual(["a:on", "b:ondemand"]);
  });

  it("does not let an older read overwrite a newer one's answer", () => {
    seed();
    const older = beginEnginesRead();
    const newer = beginEnginesRead();
    saveEnginesRead(newer, [eng("a", "on"), eng("b", "on")], true, ["hf"]);
    saveEnginesRead(older, [eng("a", "ondemand"), eng("b", "ondemand")], false, []);
    expect(modes()).toEqual(["a:on", "b:on"]);
    expect(loadEngines()?.isSuper).toBe(true);
  });

  it("drops an edit's cache write when a read begun after the edit began has landed the row", () => {
    seed();
    const edit = beginEnginesEdit("a");
    saveEnginesRead(beginEnginesRead(), [eng("a", "ondemand"), eng("b", "ondemand")], true, ["hf"]); // may hold a later state
    edit({ mode: "on" } as Partial<EngineRow>);
    expect(modes()).toEqual(["a:ondemand", "b:ondemand"]);
  });

  it("writes nothing from a stale read when an edit claimed a row the cache does not hold", () => {
    const read = beginEnginesRead();
    beginEnginesEdit("a")({ mode: "on" } as Partial<EngineRow>); // nothing cached to patch
    saveEnginesRead(read, [eng("a", "ondemand")], true, ["hf"]);
    expect(loadEngines()).toBeNull();
  });
});
