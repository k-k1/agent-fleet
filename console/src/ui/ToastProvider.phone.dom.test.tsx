// On a phone the toast stack moves off the composer to below the top bars, and a tap outside it
// dismisses the timed toasts. jsdom has no layout, so where the bars end is pinned in
// toastPlacement.test.ts; this pins which stack a phone gets and what a tap takes away.
import { afterEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { ToastProvider, useToast } from "./ToastProvider.tsx";
import { MOBILE_QUERY } from "../lib/device.ts";

let host: HTMLDivElement;
let root: Root;
let fire: ReturnType<typeof useToast>;

function Grab() {
  fire = useToast();
  return null;
}

// useIsMobile() reads the media query at render time, so the device is decided before mounting.
const realMatchMedia = window.matchMedia;
function setPhone(phone: boolean) {
  window.matchMedia = ((q: string) => ({
    matches: phone && q === MOBILE_QUERY,
    media: q,
    onchange: null,
    addEventListener() {},
    removeEventListener() {},
    addListener() {},
    removeListener() {},
    dispatchEvent: () => false,
  })) as unknown as typeof window.matchMedia;
}

async function mount(phone: boolean) {
  setPhone(phone);
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () =>
    root.render(
      <ToastProvider>
        <Grab />
        <textarea className="composer" />
      </ToastProvider>,
    ),
  );
}

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  window.matchMedia = realMatchMedia;
  vi.useRealTimers();
});

const texts = () => [...document.querySelectorAll(".ui-toast-msg")].map((e) => e.textContent);
const tap = (el: Element) => el.dispatchEvent(new Event("pointerdown", { bubbles: true, composed: true }));

describe("ToastProvider on a phone", () => {
  it("puts the stack below the top bars instead of at the bottom", async () => {
    await mount(true);
    await act(async () => fire("queued", { kind: "info" }));
    const stack = document.querySelector(".ui-toasts") as HTMLElement;
    expect(stack.classList.contains("ui-toasts-phone")).toBe(true);
    // No bars in jsdom: the run ends at 0, and the stack keeps its 8px off it.
    expect(stack.style.top).toBe("8px");
  });

  it("keeps the desktop stack at the bottom", async () => {
    await mount(false);
    await act(async () => fire("queued", { kind: "info" }));
    const stack = document.querySelector(".ui-toasts") as HTMLElement;
    expect(stack.classList.contains("ui-toasts-phone")).toBe(false);
    expect(stack.style.top).toBe("");
  });

  it("dismisses timed toasts on a tap outside the stack, and keeps sticky ones", async () => {
    await mount(true);
    await act(async () => fire("timed", { kind: "info" }));
    await act(async () => fire("failed", { kind: "error" }));
    await act(async () => fire("update now", { kind: "info", duration: 0 }));
    await act(async () => tap(document.querySelector(".ui-toast-msg")!));
    expect(texts()).toEqual(["timed", "failed", "update now"]);
    await act(async () => tap(document.querySelector(".composer")!));
    expect(texts()).toEqual(["update now"]);
  });

  it("does not run onClose for a tap-away dismissal", async () => {
    await mount(true);
    const onClose = vi.fn();
    await act(async () => fire("timed", { kind: "info", onClose }));
    await act(async () => tap(document.querySelector(".composer")!));
    expect(texts()).toEqual([]);
    expect(onClose).not.toHaveBeenCalled();
  });

  it("leaves toasts alone on a desktop tap", async () => {
    await mount(false);
    await act(async () => fire("timed", { kind: "info" }));
    await act(async () => tap(document.querySelector(".composer")!));
    expect(texts()).toEqual(["timed"]);
  });
});
