// useWsBarFold's wiring: the planner is covered by wsBarFold.test.ts; what needs a tree is the
// round trip through React for the usage fold (fold → chips move → measure the saving →
// unfold later from that saving) and the resize path. jsdom has no layout, so the bar's
// width and the content's width are supplied by the test.
import { describe, it, expect, afterEach, beforeEach } from "vitest";
import { act, useRef } from "react";
import { createRoot, type Root } from "react-dom/client";
import { useWsBarFold } from "./wsBarFold.ts";

let host: HTMLDivElement | null = null;
let root: Root | null = null;
let avail = 0;
let roCallbacks: (() => void)[] = [];
const OrigRO = globalThis.ResizeObserver;

// Content widths: labels save 200, ⋯ saves 300, folded usage chips save 250.
const USAGE = 250;
function measure(bar: HTMLElement) {
  let w = 1200;
  if (bar.hasAttribute("data-fold-labels")) w -= 200;
  if (bar.hasAttribute("data-fold-more")) w -= 300;
  if (bar.dataset.usage === "folded") w -= USAGE;
  return w;
}

let seen: { foldUsage: boolean; foldMore: boolean } | null = null;
function Bar({ enabled = true }: { enabled?: boolean }) {
  const ref = useRef<HTMLDivElement>(null);
  const r = useWsBarFold(ref, enabled, measure);
  seen = r;
  return <div ref={ref} className="wsbar" data-usage={r.foldUsage ? "folded" : "inline"} />;
}

const bar = () => host!.querySelector<HTMLDivElement>(".wsbar")!;
async function mount(width: number, enabled = true) {
  avail = width;
  await act(async () => root!.render(<Bar enabled={enabled} />));
}
async function resize(width: number) {
  avail = width;
  await act(async () => roCallbacks.forEach((cb) => cb()));
}
const attrs = () =>
  ["data-fold-labels", "data-fold-more", "data-fold-tight"].filter((a) => bar().hasAttribute(a)).join(" ");

beforeEach(() => {
  Object.defineProperty(HTMLElement.prototype, "clientWidth", { configurable: true, get: () => avail });
  roCallbacks = [];
  globalThis.ResizeObserver = class {
    constructor(cb: () => void) {
      roCallbacks.push(cb);
    }
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver;
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  host = root = seen = null;
  globalThis.ResizeObserver = OrigRO;
  delete (HTMLElement.prototype as { clientWidth?: number }).clientWidth;
});

describe("useWsBarFold", () => {
  it("folds nothing on a wide bar", async () => {
    await mount(1300);
    expect(attrs()).toBe("");
    expect(seen).toEqual({ foldUsage: false, foldMore: false });
  });

  it("steps through labels and ⋯ before touching the usage chips", async () => {
    await mount(1000);
    expect(attrs()).toBe("data-fold-labels");
    await resize(700);
    expect(attrs()).toBe("data-fold-labels data-fold-more");
    expect(seen).toEqual({ foldUsage: false, foldMore: true });
  });

  it("folds the usage chips, then unfolds them once their saving fits again", async () => {
    await mount(600); // 700 at MORE does not fit; folded it is 450
    expect(seen).toEqual({ foldUsage: true, foldMore: true });
    expect(bar().dataset.usage).toBe("folded");
    expect(attrs()).toBe("data-fold-labels data-fold-more");
    // 450 + 250 saving = 700 needed unfolded; slack keeps it folded at exactly 700…
    await resize(700);
    expect(seen!.foldUsage).toBe(true);
    // …and lets it out with room to spare, at the cheapest step that then fits.
    await resize(1250);
    expect(seen).toEqual({ foldUsage: false, foldMore: false });
    expect(attrs()).toBe("");
  });

  it("goes TIGHT only when the usage fold is not enough", async () => {
    await mount(300);
    expect(seen!.foldUsage).toBe(true);
    expect(attrs()).toBe("data-fold-labels data-fold-more data-fold-tight");
  });

  it("clears every fold when the bar leaves desktop mode", async () => {
    await mount(300);
    await mount(300, false);
    expect(attrs()).toBe("");
    expect(seen).toEqual({ foldUsage: false, foldMore: false });
  });
});
