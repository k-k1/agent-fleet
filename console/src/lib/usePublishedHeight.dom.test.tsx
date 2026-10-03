// Sticky tiers in the left rail pin below bands whose height varies (the repo row wraps to two
// lines on touch). These tests pin the contract the CSS relies on: the variable lands on the
// band's parent, follows resizes, appears when the band mounts after its owner, and goes away
// with the band.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { usePublishedHeight } from "./usePublishedHeight.ts";

let root: Root | null = null;
let host: HTMLDivElement;
const observers: Array<() => void> = [];

beforeEach(() => {
  // jsdom has no layout: read the height from data-h so a test can "resize" the band.
  vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockImplementation(function (this: HTMLElement) {
    return Number(this.dataset.h ?? 0);
  });
  vi.stubGlobal(
    "ResizeObserver",
    class {
      constructor(cb: () => void) {
        observers.push(cb);
      }
      observe() {}
      disconnect() {}
    },
  );
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  host.remove();
  observers.length = 0;
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

let setShown: (v: boolean) => void = () => {};
let setH: (v: number) => void = () => {};

function Harness({ enabled = true, initiallyShown = true }: { enabled?: boolean; initiallyShown?: boolean }) {
  const [shown, show] = useState(initiallyShown);
  const [h, height] = useState(70);
  setShown = show;
  setH = height;
  const ref = usePublishedHeight<HTMLDivElement>("--band-h", enabled);
  return <div className="owner">{shown && <div ref={ref} data-h={h} />}</div>;
}

const owner = () => host.querySelector<HTMLElement>(".owner")!;
const value = () => owner().style.getPropertyValue("--band-h");

describe("usePublishedHeight", () => {
  it("publishes the band's height on its parent and follows a resize", () => {
    act(() => root!.render(<Harness />));
    expect(value()).toBe("70px");
    act(() => setH(36));
    act(() => observers.forEach((cb) => cb()));
    expect(value()).toBe("36px");
  });

  it("publishes when the band mounts after its owner", () => {
    act(() => root!.render(<Harness initiallyShown={false} />));
    expect(value()).toBe("");
    act(() => setShown(true));
    expect(value()).toBe("70px");
  });

  it("removes the variable when the band unmounts", () => {
    act(() => root!.render(<Harness />));
    act(() => setShown(false));
    expect(value()).toBe("");
  });

  it("leaves the variable unset when disabled", () => {
    act(() => root!.render(<Harness enabled={false} />));
    expect(value()).toBe("");
  });
});
