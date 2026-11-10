import { beforeEach, describe, expect, it } from "vitest";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { clearLocalState, setTenant, setUser } from "./client.ts";
import { usePersistedOpen } from "../../lib/usePersistedOpen.ts";
import { foldKey, readFold, writeFold } from "../../lib/foldStore.ts";

beforeEach(() => {
  localStorage.clear();
  setUser("alice");
  setTenant("t1");
});

describe("fold flags survive logout per (user, tenant)", () => {
  it("keeps the signed-in user's folds and clears everything else", () => {
    writeFold("af-proj-repo", false);
    writeFold("af-section-files", true);
    localStorage.setItem("af-draft-1", "text");
    localStorage.setItem("af-proj-legacy", "0"); // legacy unscoped key
    clearLocalState();
    setUser("alice");
    setTenant("t1");
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

  it("separates tenants and users, and survives logout per scope", () => {
    writeFold("af-proj-repo", false);
    clearLocalState();
    setUser("alice");
    setTenant("t2");
    expect(readFold("af-proj-repo")).toBeNull();
    setUser("bob");
    setTenant("t1");
    expect(readFold("af-proj-repo")).toBeNull();
    setUser("alice");
    expect(readFold("af-proj-repo")).toBe(false);
  });

  it("wipes folds written before whoami resolved (empty user segment)", () => {
    setUser("");
    writeFold("af-proj-repo", false);
    clearLocalState();
    setUser("bob");
    expect(readFold("af-proj-repo")).toBeNull();
    expect(Object.keys(localStorage).filter((k) => k.startsWith("af-proj-"))).toEqual([]);
  });

  it("does not offer a legacy value to another account switched to without logout", () => {
    localStorage.setItem("af-proj-old", "0");
    setUser("bob");
    expect(readFold("af-proj-old")).toBeNull();
  });

  it("scopes af-files-home too, and keeps it", () => {
    writeFold("af-files-home", true);
    clearLocalState();
    setUser("alice");
    setTenant("t1");
    expect(readFold("af-files-home")).toBe(true);
  });

  it("strips NUL from identity so scopes cannot collide", () => {
    setUser("a\0b");
    writeFold("af-proj-r", false);
    setUser("a");
    setTenant("b\0t1");
    expect(readFold("af-proj-r")).toBeNull();
  });

  it("re-reads a mounted fold when the tenant changes", () => {
    writeFold("af-proj-repo", false);
    const el = document.createElement("div");
    const root = createRoot(el);
    let open: boolean | undefined;
    const C = () => {
      open = usePersistedOpen("af-proj-repo", true).open;
      return null;
    };
    (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
    act(() => root.render(<C />));
    expect(open).toBe(false);
    act(() => setTenant("t2"));
    expect(open).toBe(true);
    act(() => root.unmount());
  });
});
