import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { useGuardedPoll, type ReadTicket } from "./readGuard.ts";

(
  globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }
).IS_REACT_ACT_ENVIRONMENT = true;

type Read = { subject: string; t: ReadTicket };
let reads: Read[] = [];

function Probe({ subject, ms }: { subject: string; ms: number | null }) {
  useGuardedPoll((t) => reads.push({ subject, t }), ms, [subject]);
  return null;
}

describe("useGuardedPoll", () => {
  let root: Root;
  beforeEach(() => {
    vi.useFakeTimers();
    reads = [];
    root = createRoot(document.createElement("div"));
  });
  afterEach(() => {
    act(() => root.unmount());
    vi.useRealTimers();
  });

  it("reads at once, then every period", () => {
    act(() => root.render(<Probe subject="a" ms={1000} />));
    expect(reads).toHaveLength(1);
    act(() => void vi.advanceTimersByTime(2000));
    expect(reads).toHaveLength(3);
  });

  it("on a change of subject drops the reads in flight and reads the new one at once, without restarting the period", () => {
    act(() => root.render(<Probe subject="a" ms={1000} />));
    act(() => void vi.advanceTimersByTime(600));
    act(() => root.render(<Probe subject="b" ms={1000} />));
    expect(reads.map((r) => r.subject)).toEqual(["a", "b"]);
    expect(reads[0].t.current()).toBe(false);
    expect(reads[0].t.signal.aborted).toBe(true);
    act(() => void vi.advanceTimersByTime(400)); // the original period, not 1000 after the switch
    expect(reads.map((r) => r.subject)).toEqual(["a", "b", "b"]);
    expect(reads[1].t.current()).toBe(true);
  });

  it("a tick racing the new subject's first read: whichever started later wins", () => {
    act(() => root.render(<Probe subject="a" ms={1000} />));
    act(() => void vi.advanceTimersByTime(900));
    act(() => root.render(<Probe subject="b" ms={1000} />));
    act(() => void vi.advanceTimersByTime(100));
    const [, first, tick] = reads;
    expect(tick.subject).toBe("b");
    expect(tick.t.land()).toBe(true);
    expect(first.t.land()).toBe(false);
  });

  it("is off while ms is null, dropping what is in flight", () => {
    act(() => root.render(<Probe subject="a" ms={1000} />));
    act(() => root.render(<Probe subject="a" ms={null} />));
    expect(reads[0].t.current()).toBe(false);
    act(() => void vi.advanceTimersByTime(5000));
    expect(reads).toHaveLength(1);
  });

  it("drops the reads in flight on unmount", () => {
    act(() => root.render(<Probe subject="a" ms={1000} />));
    act(() => root.unmount());
    expect(reads[0].t.current()).toBe(false);
    root = createRoot(document.createElement("div"));
  });
});
