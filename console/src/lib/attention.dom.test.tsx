// Wiring of the interaction beacon (docs/log/75 P3). It touches the DOM, so it lives in the dom
// project; the pure logic (shouldBeacon's truth table) is in attention.test.ts.
import { describe, expect, it, vi, afterEach } from "vitest";
import { ATTENTION_INTERVAL_MS, shouldBeacon, wireAttentionBeacon } from "./attention.ts";

const attention = vi.fn(async () => null);
vi.mock("../core/api/client.ts", () => ({
  workspaceAttention: () => attention(),
}));

describe("shouldBeacon", () => {
  const now = 1_000_000;

  it("sends only when visible, really interacted with, and past the interval", () => {
    expect(shouldBeacon(true, true, now, now - ATTENTION_INTERVAL_MS)).toBe(true);
  });

  it("does not send from a background tab (this is what stops a forgotten tab keeping it warm)", () => {
    expect(shouldBeacon(true, false, now, now - ATTENTION_INTERVAL_MS)).toBe(false);
  });

  it("treats a synthetic event as not being human interaction", () => {
    expect(shouldBeacon(false, true, now, now - ATTENTION_INTERVAL_MS)).toBe(false);
  });

  it("does not send within the interval (no POST per scroll)", () => {
    expect(shouldBeacon(true, true, now, now - 1000)).toBe(false);
    expect(shouldBeacon(true, true, now, now - ATTENTION_INTERVAL_MS + 1)).toBe(false);
  });
});

describe("wireAttentionBeacon", () => {
  let un: (() => void) | null = null;

  afterEach(() => {
    un?.();
    un = null;
    attention.mockClear();
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  const visible = (v: "visible" | "hidden") =>
    Object.defineProperty(document, "visibilityState", { value: v, configurable: true });

  // jsdom's isTrusted is own and non-configurable, so it cannot be faked. Tests that imitate real
  // interaction wire with requireTrusted:false; that synthetic events are rejected is pinned by a
  // separate case, on the default.
  const wireForGestures = () => wireAttentionBeacon({ requireTrusted: false });
  const gesture = (e: Event) => window.dispatchEvent(e);

  it("does not send just for being opened - counting starts at the first interaction", () => {
    vi.useFakeTimers();
    visible("visible");
    un = wireAttentionBeacon();
    vi.advanceTimersByTime(10 * ATTENTION_INTERVAL_MS);
    expect(attention).not.toHaveBeenCalled();
  });

  it("sends on real interaction and folds further interactions within the interval", () => {
    vi.useFakeTimers();
    visible("visible");
    un = wireForGestures();
    vi.advanceTimersByTime(ATTENTION_INTERVAL_MS + 1);

    // Scrolling (i.e. reading) counts as interaction too. It involves no keystrokes, so without
    // it someone who is only reading looks absent.
    gesture(new WheelEvent("wheel"));
    expect(attention).toHaveBeenCalledTimes(1);
    gesture(new WheelEvent("wheel"));
    gesture(new MouseEvent("pointerdown"));
    expect(attention).toHaveBeenCalledTimes(1); // once per 60 seconds

    vi.advanceTimersByTime(ATTENTION_INTERVAL_MS + 1);
    gesture(new KeyboardEvent("keydown"));
    expect(attention).toHaveBeenCalledTimes(2);
  });

  it("ignores synthetic events (isTrusted=false)", () => {
    vi.useFakeTimers();
    visible("visible");
    un = wireAttentionBeacon();
    vi.advanceTimersByTime(ATTENTION_INTERVAL_MS + 1);
    window.dispatchEvent(new WheelEvent("wheel")); // jsdom default = synthetic
    expect(attention).not.toHaveBeenCalled();
  });

  it("does not send from a background tab", () => {
    vi.useFakeTimers();
    visible("hidden");
    un = wireForGestures();
    vi.advanceTimersByTime(ATTENTION_INTERVAL_MS + 1);
    gesture(new WheelEvent("wheel"));
    expect(attention).not.toHaveBeenCalled();
  });

  it("stops picking up events once unsubscribed", () => {
    vi.useFakeTimers();
    visible("visible");
    un = wireForGestures();
    vi.advanceTimersByTime(ATTENTION_INTERVAL_MS + 1);
    un();
    un = null;
    gesture(new WheelEvent("wheel"));
    expect(attention).not.toHaveBeenCalled();
  });
});
