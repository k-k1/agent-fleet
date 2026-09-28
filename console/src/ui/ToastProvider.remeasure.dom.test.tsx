// A toast that is up while the page under it changes (a pane or studio tab switch that brings in
// or removes a header) has to move with it. jsdom has no layout, so the measurement is stubbed
// and what is pinned is that a DOM change triggers a new one.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MOBILE_QUERY } from "../lib/device.ts";

const measured = vi.hoisted(() => ({ y: 100, calls: 0 }));
vi.mock("./toastPlacement.ts", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./toastPlacement.ts")>()),
  measureChromeBottom: () => (measured.calls++, measured.y),
}));

const { ToastProvider, useToast } = await import("./ToastProvider.tsx");

let host: HTMLDivElement;
let root: Root;
let fire: ReturnType<typeof useToast>;

function Grab() {
  fire = useToast();
  return null;
}

const realMatchMedia = window.matchMedia;
beforeEach(async () => {
  window.matchMedia = ((q: string) => ({
    matches: q === MOBILE_QUERY,
    media: q,
    onchange: null,
    addEventListener() {},
    removeEventListener() {},
    addListener() {},
    removeListener() {},
    dispatchEvent: () => false,
  })) as unknown as typeof window.matchMedia;
  measured.y = 100;
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () =>
    root.render(
      <ToastProvider>
        <Grab />
      </ToastProvider>,
    ),
  );
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  window.matchMedia = realMatchMedia;
});

const frame = () => new Promise((r) => requestAnimationFrame(() => r(null)));
const stackTop = () => (document.querySelector(".ui-toasts") as HTMLElement).style.top;

describe("ToastProvider on a phone, while the page changes", () => {
  it("re-measures when a header appears under a toast that is already up", async () => {
    await act(async () => fire("update now", { kind: "info", duration: 0 }));
    expect(stackTop()).toBe("108px");
    measured.y = 249;
    const head = document.createElement("header");
    head.className = "view-head";
    await act(async () => {
      document.body.appendChild(head);
      await frame();
      await frame();
    });
    expect(stackTop()).toBe("257px");
    head.remove();
  });

  it("re-measures when a class change moves the bars", async () => {
    const pane = document.createElement("div");
    pane.innerHTML = '<header class="view-head"></header>';
    document.body.appendChild(pane);
    await act(async () => fire("queued", { kind: "info", duration: 0 }));
    measured.y = 177;
    await act(async () => {
      pane.className = "pane active";
      await frame();
      await frame();
    });
    expect(stackTop()).toBe("185px");
    pane.remove();
  });

  it("does not measure for content that changes under it", async () => {
    // A terminal's DOM renderer rewrites its rows every frame; measuring for each would force a
    // layout per frame.
    const rows = document.createElement("div");
    rows.className = "xterm-rows";
    document.body.appendChild(rows);
    await act(async () => fire("update now", { kind: "info", duration: 0 }));
    const before = measured.calls;
    await act(async () => {
      for (let i = 0; i < 5; i++) {
        rows.innerHTML = `<div style="width:${i}px">row ${i}</div>`;
        rows.setAttribute("class", "xterm-rows r" + i);
        await frame();
      }
      await frame();
    });
    expect(measured.calls).toBe(before);
    rows.remove();
  });
});
