import { beforeEach, describe, expect, it } from "vitest";
import { clearLocalState, setUser } from "./client.ts";
import { foldKey, readFold, writeFold } from "../../lib/foldStore.ts";

beforeEach(() => {
  localStorage.clear();
  setUser("alice");
});

describe("fold flags survive logout per (user, tenant)", () => {
  it("keeps the signed-in user's folds and clears everything else", () => {
    writeFold("af-proj-repo", false);
    writeFold("af-section-files", true);
    localStorage.setItem("af-draft-1", "text");
    localStorage.setItem("af-proj-legacy", "0"); // legacy unscoped key
    clearLocalState();
    setUser("alice");
    expect(readFold("af-proj-repo")).toBe(false);
    expect(readFold("af-section-files")).toBe(true);
    expect(localStorage.getItem("af-draft-1")).toBeNull();
    expect(localStorage.getItem("af-proj-legacy")).toBeNull();
  });

  it("never shows the previous user's folds to the next account", () => {
    writeFold("af-proj-repo", false);
    clearLocalState();
    setUser("bob");
    expect(readFold("af-proj-repo")).toBeNull();
  });

  it("reads the legacy key once, and a later toggle cannot be undone by it", () => {
    localStorage.setItem("af-shared-wc-1", "0");
    expect(readFold("af-shared-wc-1")).toBe(false);
    writeFold("af-shared-wc-1", true);
    expect(localStorage.getItem("af-shared-wc-1")).toBeNull();
    expect(readFold("af-shared-wc-1")).toBe(true);
  });

  it("leaves non-fold keys alone", () => {
    expect(foldKey("af-layout")).toBe("af-layout");
  });
});
