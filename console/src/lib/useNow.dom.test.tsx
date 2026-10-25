import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { useNow } from "./useNow.ts";

(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true;

function Probe() {
  return <span id="n">{useNow(15000)}</span>;
}

describe("useNow", () => {
  let root: Root;
  let el: HTMLDivElement;
  beforeEach(() => {
    vi.useFakeTimers();
    el = document.createElement("div");
    root = createRoot(el);
  });
  afterEach(() => {
    act(() => root.unmount());
    vi.useRealTimers();
  });
  it("advances with the clock without any prop or state change from outside", () => {
    act(() => root.render(<Probe />));
    const first = Number(el.textContent);
    act(() => {
      vi.advanceTimersByTime(3 * 60 * 1000);
    });
    expect(Number(el.textContent) - first).toBeGreaterThanOrEqual(3 * 60 * 1000);
  });
});
