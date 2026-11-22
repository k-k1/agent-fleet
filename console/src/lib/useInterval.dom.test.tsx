import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { useInterval } from "./useInterval.ts";

(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true;

function Probe({ fn, ms }: { fn: () => void; ms: number | null }) {
  useInterval(fn, ms);
  return null;
}

describe("useInterval", () => {
  let root: Root;
  beforeEach(() => {
    vi.useFakeTimers();
    root = createRoot(document.createElement("div"));
  });
  afterEach(() => {
    act(() => root.unmount());
    vi.useRealTimers();
  });
  it("fires every period, not immediately", () => {
    const fn = vi.fn();
    act(() => root.render(<Probe fn={fn} ms={1000} />));
    expect(fn).not.toHaveBeenCalled();
    act(() => void vi.advanceTimersByTime(3000));
    expect(fn).toHaveBeenCalledTimes(3);
  });
  it("calls the latest callback without restarting the period", () => {
    const a = vi.fn();
    const b = vi.fn();
    act(() => root.render(<Probe fn={a} ms={1000} />));
    act(() => void vi.advanceTimersByTime(600));
    act(() => root.render(<Probe fn={b} ms={1000} />));
    act(() => void vi.advanceTimersByTime(400));
    expect(a).not.toHaveBeenCalled();
    expect(b).toHaveBeenCalledTimes(1);
  });
  it("is off while ms is null and stops on unmount", () => {
    const fn = vi.fn();
    act(() => root.render(<Probe fn={fn} ms={null} />));
    act(() => void vi.advanceTimersByTime(5000));
    expect(fn).not.toHaveBeenCalled();
    act(() => root.render(<Probe fn={fn} ms={1000} />));
    act(() => void vi.advanceTimersByTime(1000));
    expect(fn).toHaveBeenCalledTimes(1);
    act(() => root.unmount());
    act(() => void vi.advanceTimersByTime(5000));
    expect(fn).toHaveBeenCalledTimes(1);
    root = createRoot(document.createElement("div"));
  });
  it("stops a running timer when ms goes to null, and swaps periods without a stale timer", () => {
    const fn = vi.fn();
    act(() => root.render(<Probe fn={fn} ms={1000} />));
    act(() => void vi.advanceTimersByTime(1000));
    expect(fn).toHaveBeenCalledTimes(1);
    act(() => root.render(<Probe fn={fn} ms={null} />));
    act(() => void vi.advanceTimersByTime(10000));
    expect(fn).toHaveBeenCalledTimes(1);
    act(() => root.render(<Probe fn={fn} ms={1000} />));
    act(() => root.render(<Probe fn={fn} ms={4000} />));
    act(() => void vi.advanceTimersByTime(4000));
    expect(fn).toHaveBeenCalledTimes(2);
  });
});
