import { afterEach, describe, expect, it } from "vitest";
import { clearScrollPos, loadScrollPos, saveScrollPos } from "../scrollMemory.ts";
import { scrollMemoryRef } from "./useScrollMemory.ts";

function scrollable(): HTMLElement {
  const el = document.createElement("div");
  let top = 0;
  Object.defineProperty(el, "scrollTop", { get: () => top, set: (v: number) => (top = v), configurable: true });
  Object.defineProperty(el, "scrollHeight", { get: () => 5000, configurable: true });
  Object.defineProperty(el, "clientHeight", { get: () => 100, configurable: true });
  return el;
}

afterEach(() => clearScrollPos());

describe("scrollMemoryRef restore option", () => {
  it("restores the recorded position by default", () => {
    saveScrollPos("k", 800);
    const el = scrollable();
    const detach = scrollMemoryRef("k")(el);
    expect(el.scrollTop).toBe(800);
    if (detach) detach();
  });

  it("restore: false leaves the position to its owner and still records it", () => {
    saveScrollPos("k", 800);
    const el = scrollable();
    const detach = scrollMemoryRef("k", { restore: false })(el);
    expect(el.scrollTop).toBe(0);
    el.scrollTop = 300;
    el.dispatchEvent(new Event("scroll"));
    expect(loadScrollPos("k")).toBe(300);
    if (detach) detach();
  });
});
