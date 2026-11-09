import { afterEach, describe, expect, it } from "vitest";
import { clearFileModes, loadFileMode, saveFileMode } from "./fileModeMemory.ts";
import type { FileModeState } from "./fileMode.ts";

const SPLIT: FileModeState = { kind: "markdown", md: "split", renderer: "normal" };
const none = { targetLine: undefined, openMode: undefined };

afterEach(() => clearFileModes());

describe("fileModeMemory", () => {
  it("returns the mode saved under the same request", () => {
    saveFileMode("k", none, SPLIT);
    expect(loadFileMode("k", none)).toEqual(SPLIT);
  });

  it("forgets nothing for a missing key and ignores a null key", () => {
    saveFileMode(null, none, SPLIT);
    expect(loadFileMode("k", none)).toBeNull();
    expect(loadFileMode(null, none)).toBeNull();
  });

  it("treats a different request as new", () => {
    saveFileMode("k", { targetLine: 4, openMode: undefined }, SPLIT);
    expect(loadFileMode("k", { targetLine: 4, openMode: undefined })).toEqual(SPLIT);
    expect(loadFileMode("k", { targetLine: 5, openMode: undefined })).toBeNull();
    expect(loadFileMode("k", { targetLine: 4, openMode: "edit" })).toBeNull();
    expect(loadFileMode("k", none)).toBeNull();
  });

  it("drops the least recently saved entry past the cap", () => {
    for (let i = 0; i < 201; i++) saveFileMode(`k${i}`, none, SPLIT);
    expect(loadFileMode("k0", none)).toBeNull();
    expect(loadFileMode("k1", none)).toEqual(SPLIT);
    saveFileMode("k1", none, SPLIT); // re-saving moves it to the back
    saveFileMode("k201", none, SPLIT);
    expect(loadFileMode("k1", none)).toEqual(SPLIT);
    expect(loadFileMode("k2", none)).toBeNull();
  });
});
