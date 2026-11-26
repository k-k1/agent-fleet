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

  it("cancels pending expiry timers when unmounted with a toast showing", async () => {
    // Other code (the error toast's log) may hold timers of its own, so count relative to a baseline.
    const base = vi.getTimerCount();
    await act(async () => fire("a", { kind: "info", duration: 4000 }));
    await act(async () => fire("b", { kind: "info", duration: 8000 }));
    expect(vi.getTimerCount()).toBe(base + 2);
    await act(async () => root.unmount());
    expect(vi.getTimerCount()).toBe(base);
    expect(() => vi.advanceTimersByTime(10000)).not.toThrow();
    // afterEach unmounts again; re-create so it has a root to unmount.
    root = createRoot(host);
  });

  it("still expires an unkeyed toast on its own", async () => {
    await act(async () => fire("a", { kind: "info" }));
    await act(async () => vi.advanceTimersByTime(4100));
    expect(texts()).toEqual([]);
  });
});
