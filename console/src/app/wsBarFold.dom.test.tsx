// useWsBarFold's wiring: the planner is covered by wsBarFold.test.ts; what needs a tree is the
// round trip through React for the usage fold (fold → chips move → measure the saving →
// unfold later from that saving) and the resize path. jsdom has no layout, so the bar's
// width and the content's width are supplied by the test.
import { describe, it, expect, afterEach, beforeEach } from "vitest";
import { act, useRef } from "react";
import { createRoot, type Root } from "react-dom/client";
import { useWsBarFold, useWsBarPhoneFold, type PhoneFoldStep } from "./wsBarFold.ts";

let host: HTMLDivElement | null = null;
let root: Root | null = null;
let avail = 0;
let roCallbacks: (() => void)[] = [];
const OrigRO = globalThis.ResizeObserver;

// Content widths: labels save 200, ⋯ saves 300, folded usage chips save `usage` (250 unless a
// test pins a chip back onto the bar). `base` is the unfolded content.
let usage = 250;
let base = 1200;
function measure(bar: HTMLElement) {
  let w = base;
  if (bar.hasAttribute("data-fold-labels")) w -= 200;
  if (bar.hasAttribute("data-fold-more")) w -= 300;
  if (bar.dataset.usage === "folded") w -= usage;
  return w;
}

let seen: { foldUsage: boolean; foldMore: boolean; noteUsageLayout: (key: string) => void } | null = null;
function Bar({ enabled = true }: { enabled?: boolean }) {
  const ref = useRef<HTMLDivElement>(null);
  const r = useWsBarFold(ref, enabled, measure);
  seen = r;
  return (
    <div ref={ref} className="wsbar" data-usage={r.foldUsage ? "folded" : "inline"}>
      <span className="chip">label</span>
    </div>
  );
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

const folds = () => ({ foldUsage: seen!.foldUsage, foldMore: seen!.foldMore });

beforeEach(() => {
  usage = 250;
  base = 1200;
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
    expect(folds()).toEqual({ foldUsage: false, foldMore: false });
  });

  it("steps through labels and ⋯ before touching the usage chips", async () => {
    await mount(1000);
    expect(attrs()).toBe("data-fold-labels");
    await resize(700);
    expect(attrs()).toBe("data-fold-labels data-fold-more");
    expect(folds()).toEqual({ foldUsage: false, foldMore: true });
  });

  it("folds the usage chips, then unfolds them once their saving fits again", async () => {
    await mount(600); // 700 at MORE does not fit; folded it is 450
    expect(folds()).toEqual({ foldUsage: true, foldMore: true });
    expect(bar().dataset.usage).toBe("folded");
    expect(attrs()).toBe("data-fold-labels data-fold-more");
    // 450 + 250 saving = 700 needed unfolded; slack keeps it folded at exactly 700…
    await resize(700);
    expect(seen!.foldUsage).toBe(true);
    // …and lets it out with room to spare, at the cheapest step that then fits.
    await resize(1250);
    expect(folds()).toEqual({ foldUsage: false, foldMore: false });
    expect(attrs()).toBe("");
  });

  it("re-learns the saving when the chips that would come back change", async () => {
    await mount(600); // folded, saving 250
    expect(seen!.foldUsage).toBe(true);
    // A chip gets pinned back onto the bar: folding now saves only 74, so the folded bar is
    // 626 at MORE and the unfolded one still 700.
    usage = 74;
    await resize(800);
    // 626 + the stale 250 + slack does not fit 800, though the real 700 + slack does.
    expect(seen!.foldUsage).toBe(true);
    await act(async () => seen!.noteUsageLayout("claude,codex"));
    expect(folds()).toEqual({ foldUsage: false, foldMore: true });
    expect(bar().dataset.usage).toBe("inline");
  });

  it("re-settles when content changes inside the bar, without any resize", async () => {
    await mount(1000);
    expect(attrs()).toBe("data-fold-labels");
    // A profile label shrinks inside a chip — nothing changes size from the bar's side.
    base = 900;
    await act(async () => {
      bar().querySelector(".chip")!.textContent = "short";
      await Promise.resolve();
      await Promise.resolve();
    });
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
    expect(folds()).toEqual({ foldUsage: false, foldMore: false });
  });
});

// useWsBarPhoneFold: the phone bar's step follows the width it has. As above, jsdom has no layout:
// the bar's width and the content's width are supplied, so this proves the wiring (attribute set
// before the measure, step returned, cleared when the phone layout ends) and NOT that the real
// buttons add up to those widths — that is measured in a browser (see the PR for #1650).
let phoneSeen: PhoneFoldStep = 0;
function phoneMeasure(bar: HTMLElement) {
  const s = Number(bar.getAttribute("data-fold-phone") || 0);
  return 500 - s * 80; // 500 / 420 / 340
}
function PhoneBar({ enabled = true }: { enabled?: boolean }) {
  const ref = useRef<HTMLDivElement>(null);
  phoneSeen = useWsBarPhoneFold(ref, enabled, phoneMeasure);
  return <div ref={ref} className="wsbar" />;
}
async function mountPhone(width: number, enabled = true) {
  avail = width;
  await act(async () => root!.render(<PhoneBar enabled={enabled} />));
}

describe("useWsBarPhoneFold", () => {
  it("folds nothing when the bar fits", async () => {
    await mountPhone(500);
    expect(phoneSeen).toBe(0);
    expect(bar().hasAttribute("data-fold-phone")).toBe(false);
  });

  it("steps up as the bar narrows and back down as it widens", async () => {
    await mountPhone(430);
    expect(phoneSeen).toBe(1);
    expect(bar().getAttribute("data-fold-phone")).toBe("1");
    await resize(390);
    expect(phoneSeen).toBe(2);
    expect(bar().getAttribute("data-fold-phone")).toBe("2");
    await resize(520);
    expect(phoneSeen).toBe(0);
    expect(bar().hasAttribute("data-fold-phone")).toBe(false);
  });

  it("stops at the last step when even that does not fit", async () => {
    await mountPhone(200);
    expect(phoneSeen).toBe(2);
  });

  it("leaves the bar alone outside the phone layout", async () => {
    await mountPhone(200, false);
    expect(phoneSeen).toBe(0);
    expect(bar().hasAttribute("data-fold-phone")).toBe(false);
  });
});
