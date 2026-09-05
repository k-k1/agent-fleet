import { describe, it, expect, afterEach } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { SelectionFloat } from "./SelectionFloat.tsx";

// Why this file exists: the failure it guards against cannot be observed by any test. The
// browser's native selection menu (Copy / Share / Select all) is not part of the DOM and
// headless Chromium never draws it, so a control hidden underneath it looks perfectly visible
// to jsdom, to a CDP screenshot and to the eye of a desktop reviewer. Only the DECISION is
// checkable: on a coarse pointer nothing is placed in the band above the selection.

let root: Root | null = null;
let host: HTMLDivElement | null = null;
const realMatchMedia = window.matchMedia;

function render(coarse: boolean) {
  // primaryCoarsePointer() reads only this; the domSetup stub answers false to every query.
  window.matchMedia = ((q: string) => ({
    matches: coarse && q.includes("coarse"),
    media: q,
    addEventListener() {},
    removeEventListener() {},
  })) as unknown as typeof window.matchMedia;
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  act(() =>
    root!.render(
      <SelectionFloat x={120} y={400} className="sel-pill-group">
        <button type="button">paint</button>
      </SelectionFloat>,
    ),
  );
  return host.querySelector<HTMLElement>(".sel-float")!;
}

afterEach(() => {
  if (root) act(() => root!.unmount());
  host?.remove();
  root = null;
  host = null;
  window.matchMedia = realMatchMedia;
});

describe("SelectionFloat", () => {
  it("floats at the selection for a fine pointer", () => {
    const el = render(false);
    expect(el.className).toContain("sel-pill-group");
    expect(el.classList.contains("sel-float-docked")).toBe(false);
    // jsdom reports zero size, so placeFixed clamps both axes to its 8px margin — what matters
    // is that a position was written at all, i.e. the floating branch ran.
    expect(el.style.top).not.toBe("");
    expect(el.style.left).not.toBe("");
  });

  it("docks, and writes no top of its own, for a coarse pointer", () => {
    const el = render(true);
    expect(el.classList.contains("sel-float-docked")).toBe(true);
    // The docked position comes from the stylesheet (bottom edge). An inline `top` would put the
    // group back over the selection — exactly where the native menu is — and would beat the
    // stylesheet while doing it.
    expect(el.style.top).toBe("");
    expect(el.style.left).toBe("");
  });
});
