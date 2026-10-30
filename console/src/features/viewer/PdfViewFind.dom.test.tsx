// Focus of the PDF find bar when it is opened through the af-own-find hand-over (#2972).
//
// The hand-over is a native event, so React renders its update as a normal-priority task and a
// requestAnimationFrame can run before the bar exists. rAF is stubbed to run immediately to pin
// that worst-case ordering; focus must not depend on it.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { OWN_FIND_EVENT } from "../../lib/ownFind.ts";

// A load that never settles keeps the view in its mounted, not-failed state without pdf.js.
vi.mock("./pdfjs.ts", () => ({
  loadPdfjs: () => new Promise(() => {}),
  documentAssetParams: () => ({ cMapPacked: true }),
}));

import { PdfView } from "./PdfView.tsx";

let host: HTMLDivElement;
let root: Root;

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback) => {
    cb(0);
    return 0;
  });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
});

function mount() {
  act(() => root.render(<PdfView src="/x.pdf" />));
  return host.querySelector<HTMLElement>(".pdfview")!;
}

const findInput = () => host.querySelector<HTMLInputElement>(".pdfview-find input");

describe("PdfView find bar focus", () => {
  it("focuses the input when opened through af-own-find", () => {
    const view = mount();
    expect(findInput()).toBeNull();
    act(() => {
      view.dispatchEvent(new Event(OWN_FIND_EVENT));
    });
    const input = findInput();
    expect(input).not.toBeNull();
    expect(document.activeElement).toBe(input);
  });

  it("focuses the input on Ctrl+F", () => {
    const view = mount();
    act(() => {
      view.dispatchEvent(new KeyboardEvent("keydown", { key: "f", ctrlKey: true, bubbles: true, cancelable: true }));
    });
    expect(document.activeElement).toBe(findInput());
  });

  it("re-focuses and selects when the bar is already open", () => {
    const view = mount();
    act(() => {
      view.dispatchEvent(new Event(OWN_FIND_EVENT));
    });
    const input = findInput()!;
    act(() => {
      input.value = "";
    });
    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
    act(() => {
      setter.call(input, "abc");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    act(() => input.blur());
    expect(document.activeElement).not.toBe(input);
    act(() => {
      view.dispatchEvent(new Event(OWN_FIND_EVENT));
    });
    expect(document.activeElement).toBe(input);
    expect([input.selectionStart, input.selectionEnd]).toEqual([0, 3]);
  });
});
