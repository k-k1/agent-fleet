import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const calls: { device: string; session: string }[] = [];
let key: string | null = "k";
vi.mock("./pushApi.ts", () => ({
  getPushKey: () => Promise.resolve(key),
  postViewing: (device: string, session: string) => {
    calls.push({ device, session });
    return Promise.resolve();
  },
}));
let session = "sess-one";
vi.mock("../../layout/store.ts", () => ({ useLayoutStore: (sel: (s: any) => unknown) => sel({ layout: {} }) }));
vi.mock("../../layout/ops.ts", () => ({ activePane: () => (session ? { session } : null) }));

import { usePushViewing, VIEWING_BEAT_MS } from "./usePushViewing.ts";

function Host() {
  usePushViewing(true);
  return null;
}
let root: Root | null = null;
function render(el: React.ReactElement) {
  const host = document.createElement("div");
  root = createRoot(host);
  act(() => root!.render(el));
  return { unmount: () => act(() => root!.unmount()) };
}
const flush = () => act(async () => void (await Promise.resolve()));

describe("usePushViewing", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    calls.length = 0;
    key = "k";
    session = "sess-one";
    vi.spyOn(document, "hasFocus").mockReturnValue(true);
    Object.defineProperty(document, "visibilityState", { value: "visible", configurable: true });
  });
  afterEach(() => {
    act(() => root?.unmount());
    root = null;
    vi.useRealTimers();
  });

  it("reports the on-screen session and repeats while it stays visible", async () => {
    render(<Host />);
    await flush();
    expect(calls.map((c) => c.session)).toEqual(["sess-one"]);
    act(() => void vi.advanceTimersByTime(VIEWING_BEAT_MS));
    expect(calls.map((c) => c.session)).toEqual(["sess-one", "sess-one"]);
  });
  it("clears the entry when the tab is hidden, and reports nothing while hidden", async () => {
    render(<Host />);
    await flush();
    Object.defineProperty(document, "visibilityState", { value: "hidden", configurable: true });
    act(() => void document.dispatchEvent(new Event("visibilitychange")));
    expect(calls.at(-1)?.session).toBe("");
    const n = calls.length;
    act(() => void vi.advanceTimersByTime(VIEWING_BEAT_MS * 2));
    expect(calls.length).toBe(n);
  });
  it("clears the entry on unmount", async () => {
    const r = render(<Host />);
    await flush();
    r.unmount();
    expect(calls.at(-1)?.session).toBe("");
  });
  it("sends nothing on a deployment without Web Push", async () => {
    key = null;
    vi.resetModules();
    const { usePushViewing: hook } = await import("./usePushViewing.ts");
    function H2() {
      hook(true);
      return null;
    }
    render(<H2 />);
    await flush();
    act(() => void vi.advanceTimersByTime(VIEWING_BEAT_MS * 2));
    expect(calls).toEqual([]);
    void usePushViewing;
  });
});
