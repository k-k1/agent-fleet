import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const calls: { device: string; session: string; tenant?: string }[] = [];
let key: string | null = "k";
let probeFails = 0;
let postGate: Promise<void> | null = null;
vi.mock("./pushApi.ts", () => ({
  probePushKey: () => (probeFails-- > 0 ? Promise.reject(new Error("502")) : Promise.resolve(key)),
  postViewing: async (device: string, session: string, tenant?: string) => {
    if (postGate && session === "sess-one") await postGate;
    calls.push({ device, session, tenant } as any);
  },
}));
vi.mock("../../core/api/client.ts", () => ({ getTenant: () => "t1" }));
let session = "sess-one";
vi.mock("../../layout/store.ts", () => ({ useLayoutStore: (sel: (s: any) => unknown) => sel({ layout: {} }) }));
vi.mock("../../layout/ops.ts", () => ({ activePane: () => (session ? { session } : null) }));

import { VIEWING_BEAT_MS, PROBE_RETRY_MS } from "./usePushViewing.ts";
let usePushViewing: typeof import("./usePushViewing.ts").usePushViewing;

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
    probeFails = 0;
    postGate = null;
    session = "sess-one";
    vi.spyOn(document, "hasFocus").mockReturnValue(true);
    Object.defineProperty(document, "visibilityState", { value: "visible", configurable: true });
  });
  afterEach(() => {
    act(() => root?.unmount());
    root = null;
    vi.useRealTimers();
  });

  beforeEach(async () => {
    vi.resetModules();
    usePushViewing = (await import("./usePushViewing.ts")).usePushViewing;
  });
  it("retries the probe after a transient failure and then reports", async () => {
    probeFails = 1;
    render(<Host />);
    await flush();
    expect(calls).toEqual([]);
    await act(async () => void (await vi.advanceTimersByTimeAsync(PROBE_RETRY_MS)));
    expect(calls.map((c) => c.session)).toEqual(["sess-one"]);
  });
  it("lands the clearing report after a slow on-screen one, never before it", async () => {
    let release!: () => void;
    postGate = new Promise<void>((r) => (release = r));
    render(<Host />);
    await flush();
    Object.defineProperty(document, "visibilityState", { value: "hidden", configurable: true });
    act(() => void document.dispatchEvent(new Event("visibilitychange")));
    release();
    await flush();
    await flush();
    expect(calls.map((c) => c.session)).toEqual(["sess-one", ""]);
  });
  it("pins every report to the tenant it was made for", async () => {
    render(<Host />);
    await flush();
    expect(calls[0].tenant).toBe("t1");
  });
  it("reports the on-screen session and repeats while it stays visible", async () => {
    render(<Host />);
    await flush();
    expect(calls.map((c) => c.session)).toEqual(["sess-one"]);
    act(() => void vi.advanceTimersByTime(VIEWING_BEAT_MS));
    await flush();
    expect(calls.map((c) => c.session)).toEqual(["sess-one", "sess-one"]);
  });
  it("clears the entry when the tab is hidden, and reports nothing while hidden", async () => {
    render(<Host />);
    await flush();
    Object.defineProperty(document, "visibilityState", { value: "hidden", configurable: true });
    act(() => void document.dispatchEvent(new Event("visibilitychange")));
    await flush();
    expect(calls.at(-1)?.session).toBe("");
    const n = calls.length;
    act(() => void vi.advanceTimersByTime(VIEWING_BEAT_MS * 2));
    await flush();
    expect(calls.length).toBe(n);
  });
  it("clears the entry on unmount", async () => {
    const r = render(<Host />);
    await flush();
    r.unmount();
    await flush();
    expect(calls.at(-1)?.session).toBe("");
  });
  it("sends nothing on a deployment without Web Push", async () => {
    key = null;
    render(<Host />);
    await flush();
    act(() => void vi.advanceTimersByTime(VIEWING_BEAT_MS * 2));
    expect(calls).toEqual([]);
  });
});
