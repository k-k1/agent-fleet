// A keyed toast replaced in place keeps its React key, and must also get a fresh lifetime: the
// first toast's timer, left running, removed the replacement moments after it appeared.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { ToastProvider, useToast } from "./ToastProvider.tsx";

let host: HTMLDivElement;
let root: Root;
let fire: ReturnType<typeof useToast>;

function Grab() {
  fire = useToast();
  return null;
}

beforeEach(async () => {
  vi.useFakeTimers();
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
  vi.useRealTimers();
});

const texts = () => [...document.querySelectorAll(".ui-toast-msg")].map((e) => e.textContent);

describe("ToastProvider", () => {
  it("gives a replaced keyed toast its own full duration", async () => {
    await act(async () => fire("one", { kind: "info", key: "k", duration: 8000 }));
    await act(async () => vi.advanceTimersByTime(7900));
    await act(async () => fire("two", { kind: "info", key: "k", duration: 8000 }));
    await act(async () => vi.advanceTimersByTime(200)); // past the first toast's deadline
    expect(texts()).toEqual(["two"]);
    await act(async () => vi.advanceTimersByTime(7900));
    expect(texts()).toEqual([]);
  });

  it("still expires an unkeyed toast on its own", async () => {
    await act(async () => fire("a", { kind: "info" }));
    await act(async () => vi.advanceTimersByTime(4100));
    expect(texts()).toEqual([]);
  });

  it("stacks a top-placed toast apart from the default bottom stack", async () => {
    await act(async () => fire("bottom", { kind: "info" }));
    await act(async () => fire("top", { kind: "info", placement: "top" }));
    const top = document.querySelector(".ui-toasts.ui-toasts-top");
    expect(top?.textContent).toContain("top");
    expect(top?.textContent).not.toContain("bottom");
    expect(document.querySelector(".ui-toasts:not(.ui-toasts-top)")?.textContent).toContain("bottom");
  });
});
