import { describe, expect, it } from "vitest";
import { groupRegistered, registeredGroupKey, registeredNeedsMeta, registeredTitle } from "./registeredGroups.ts";
import type { EngineModel, EngineRow } from "./engineTypes.ts";

const model = (over: Partial<EngineModel> & { id: string }): EngineModel =>
  ({ enabled: false, ...over }) as EngineModel;

const imageEngine = { key: "image", base_models: ["sdxl", "sd15", "flux1"] } as EngineRow;
const llmEngine = { key: "llm" } as EngineRow;

describe("registeredTitle", () => {
  it("falls back to the id, and says so, when no page was ever read", () => {
    expect(registeredTitle(model({ id: "abyssorangemix2_hard_8832" })))
      .toEqual({ title: "abyssorangemix2_hard_8832", version: "", bare: true });
  });

  it("draws the publisher's name with the version beside it", () => {
    expect(registeredTitle(model({ id: "meinamix_5038", display_name: "MeinaMix", version_name: "Meina V11" })))
      .toEqual({ title: "MeinaMix", version: "Meina V11", bare: false });
  });

  // Civitai's default version name is the model's own, and drawing "MeinaMix MeinaMix" is worse
  // than drawing no version at all.
  it("drops a version name that only repeats the title", () => {
    expect(registeredTitle(model({ id: "x", display_name: "MeinaMix", version_name: "MeinaMix" })).version).toBe("");
  });
});

describe("registeredNeedsMeta", () => {
  it("is true only for a row carrying neither a name nor a picture", () => {
    expect(registeredNeedsMeta(model({ id: "a" }))).toBe(true);
    // Hugging Face publishes a repository id and (measured) no thumbnail: named is enough.
    expect(registeredNeedsMeta(model({ id: "b", display_name: "unsloth/Qwen3-GGUF" }))).toBe(false);
    expect(registeredNeedsMeta(model({ id: "c", preview_url: "https://image.civitai.com/x/y/z.jpeg" }))).toBe(false);
  });
});

describe("registeredGroupKey", () => {
  it("files an image row under its family", () => {
    expect(registeredGroupKey(model({ id: "a", base_model: "sdxl" }), true)).toBe("sdxl");
  });

  it("files an LLM adapter under the model it was trained against", () => {
    expect(registeredGroupKey(model({ id: "a", kind: "lora", base_model: "qwen3" }), false)).toBe("qwen3");
  });

  // A GGUF declares no family at all, so the publisher is the only category it has — and it is a
  // real one: two people's quantisations of the same weights are different rows.
  it("files a GGUF under the owner of its repository", () => {
    expect(registeredGroupKey(model({ id: "a", display_name: "unsloth/Qwen3-Coder-30B-GGUF" }), false)).toBe("unsloth");
    expect(registeredGroupKey(model({ id: "a", display_name: "Qwen3-local" }), false)).toBe("");
    expect(registeredGroupKey(model({ id: "a" }), false)).toBe("");
  });
});

describe("groupRegistered", () => {
  it("puts the rows that declare nothing first, then the engine's own families in its order", () => {
    const rows = [
      model({ id: "d", base_model: "flux1" }),
      model({ id: "b", base_model: "sdxl" }),
      model({ id: "a" }),
      model({ id: "c", base_model: "sd15" }),
    ];
    expect(groupRegistered(rows, imageEngine, true).map((group) => group.key))
      .toEqual(["", "sdxl", "sd15", "flux1"]);
  });

  it("keeps a family the provider no longer lists, after the declared ones, alphabetically", () => {
    const rows = [
      model({ id: "a", base_model: "pony" }),
      model({ id: "b", base_model: "sdxl" }),
      model({ id: "c", base_model: "anima" }),
    ];
    expect(groupRegistered(rows, imageEngine, true).map((group) => group.key))
      .toEqual(["sdxl", "anima", "pony"]);
  });

  it("draws no empty group for a family the engine declares and nobody has taken in", () => {
    const groups = groupRegistered([model({ id: "a", base_model: "sdxl" })], imageEngine, true);
    expect(groups).toHaveLength(1);
    expect(groups[0].models.map((row) => row.id)).toEqual(["a"]);
  });

  it("groups an LLM catalogue by publisher and keeps the unnamed rows first", () => {
    const rows = [
      model({ id: "a", display_name: "unsloth/Qwen3-GGUF" }),
      model({ id: "b" }),
      model({ id: "c", display_name: "bartowski/Qwen2.5-GGUF" }),
      model({ id: "d", display_name: "unsloth/Gemma-GGUF" }),
    ];
    const groups = groupRegistered(rows, llmEngine, false);
    expect(groups.map((group) => group.key)).toEqual(["", "bartowski", "unsloth"]);
    expect(groups[2].models.map((row) => row.id)).toEqual(["a", "d"]);
  });
});
