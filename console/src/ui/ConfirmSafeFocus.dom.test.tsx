// A confirm raised by program output (safeFocus) must not be approvable by a keystroke already
// in flight: focus starts on Cancel, so Enter / Space activate Cancel.
import { afterEach, describe, expect, it, vi } from "vitest";

// jsdom defines ontouchstart, which reads as a touch device and skips initial focus.
vi.mock("../lib/device.ts", async (orig) => ({ ...(await orig<object>()), coarsePointer: () => false }));

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { ConfirmProvider } from "./ConfirmProvider.tsx";
import { askConfirm } from "./confirmBridge.ts";

let host: HTMLDivElement;
let root: Root;
async function mount() {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => root.render(<ConfirmProvider><div /></ConfirmProvider>));
}
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
});
const btn = (label: string) =>
  [...document.querySelectorAll("button")].find((b) => b.textContent === label) as HTMLButtonElement;

describe("ConfirmProvider safeFocus", () => {
  it("focuses Cancel, so an in-flight Enter / Space cannot approve", async () => {
    await mount();
    let result: boolean | undefined;
    await act(async () => {
      askConfirm({ title: "t", body: "b", confirmLabel: "Allow", safeFocus: true }).then((v) => (result = v));
    });
    const active = document.activeElement as HTMLElement;
    expect(active.tagName).toBe("BUTTON");
    expect(active).not.toBe(btn("Allow"));
    await act(async () => active.click()); // what Enter / Space do on a focused button
    expect(result).toBe(false);
  });

  it("without safeFocus the confirm button is focused", async () => {
    await mount();
    await act(async () => {
      void askConfirm({ title: "t", body: "b", confirmLabel: "Go" });
    });
    expect(document.activeElement).toBe(btn("Go"));
  });
});
