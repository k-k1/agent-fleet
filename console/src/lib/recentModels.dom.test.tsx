import { beforeEach, describe, expect, it } from "vitest";
import { RECENT_MODELS_MAX, readRecentModels, rememberRecentModel } from "./recentModels.ts";

beforeEach(() => localStorage.clear());

describe("recentModels", () => {
  it("keeps the newest first and moves a repeat to the front", () => {
    rememberRecentModel("claude", "a");
    rememberRecentModel("claude", "b");
    rememberRecentModel("claude", "a");
    expect(readRecentModels("claude")).toEqual(["a", "b"]);
  });
  it("caps the list", () => {
    for (const m of ["a", "b", "c", "d", "e"]) rememberRecentModel("claude", m);
    expect(readRecentModels("claude")).toEqual(["e", "d", "c"]);
    expect(readRecentModels("claude")).toHaveLength(RECENT_MODELS_MAX);
  });
  it("is per agent and ignores the default (empty) model", () => {
    rememberRecentModel("claude", "a");
    rememberRecentModel("codex", "");
    expect(readRecentModels("codex")).toEqual([]);
    expect(readRecentModels("claude")).toEqual(["a"]);
  });
  it("survives corrupt storage", () => {
    localStorage.setItem("af.recent-models.claude", "{not json");
    expect(readRecentModels("claude")).toEqual([]);
  });
});
