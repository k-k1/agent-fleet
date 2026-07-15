import { describe, it, expect, beforeEach, vi } from "vitest";
import { readDraft, clearDraft, moveDraft } from "./draft.ts";

// vitest runs in a node environment (vite.config.js), so stub the DOM storage.
const store = new Map<string, string>();
vi.stubGlobal("localStorage", {
  getItem: (k: string) => store.get(k) ?? null,
  setItem: (k: string, v: string) => void store.set(k, v),
  removeItem: (k: string) => void store.delete(k),
});

describe("draft storage helpers", () => {
  beforeEach(() => store.clear());

  it("readDraft returns the stored draft, '' when absent or key is null", () => {
    expect(readDraft("k")).toBe("");
    localStorage.setItem("k", "text");
    expect(readDraft("k")).toBe("text");
    expect(readDraft(null)).toBe("");
  });

  it("clearDraft removes the draft and tolerates null", () => {
    localStorage.setItem("k", "text");
    clearDraft("k");
    expect(readDraft("k")).toBe("");
    clearDraft(null); // no throw
  });

  it("moveDraft re-keys a draft (promotion): from is cleared, to holds the text", () => {
    localStorage.setItem("asst", "typing…");
    moveDraft("asst", "conv");
    expect(readDraft("asst")).toBe("");
    expect(readDraft("conv")).toBe("typing…");
  });

  it("moveDraft with no source text leaves the target untouched", () => {
    localStorage.setItem("conv", "keep");
    moveDraft("asst", "conv");
    expect(readDraft("conv")).toBe("keep");
  });

  it("moveDraft to null just discards the source", () => {
    localStorage.setItem("asst", "typing…");
    moveDraft("asst", null);
    expect(readDraft("asst")).toBe("");
  });
});
