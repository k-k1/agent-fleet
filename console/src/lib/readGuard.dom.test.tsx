import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  act,
  StrictMode,
  Suspense,
  startTransition,
  useLayoutEffect,
} from "react";
import { createRoot, type Root } from "react-dom/client";
import {
  useGuardedPoll,
  useReadGuard,
  type ReadGuard,
  type ReadTicket,
} from "./readGuard.ts";

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

  it("under StrictMode only the last setup's first read is live", () => {
    act(() =>
      root.render(
        <StrictMode>
          <Probe subject="a" ms={1000} />
        </StrictMode>,
      ),
    );
    expect(reads.length).toBeGreaterThanOrEqual(2);
    expect(reads.slice(0, -1).every((r) => !r.t.current())).toBe(true);
    expect(reads.at(-1)!.t.current()).toBe(true);
  });

  it("never runs a render that was not committed, and keeps the shown subject's reads live", async () => {
    const never = new Promise<never>(() => {});
    function Hold({ on }: { on: boolean }) {
      if (on) throw never;
      return null;
    }
    const tree = (subject: string) => (
      <Suspense fallback={null}>
        <Probe subject={subject} ms={1000} />
        <Hold on={subject === "b"} />
      </Suspense>
    );
    act(() => root.render(tree("a")));
    expect(reads).toHaveLength(1);
    // A transition to "b" suspends: React keeps "a" on screen and never commits "b".
    await act(async () => startTransition(() => root.render(tree("b"))));
    act(() => void vi.advanceTimersByTime(1000));
    expect(reads.map((r) => r.subject)).toEqual(["a", "a"]);
    expect(reads[0].t.current()).toBe(true);
  });
});

describe("useReadGuard", () => {
  it("is live again after StrictMode's cleanup and re-setup, and dead after a real unmount", () => {
    let guard: ReadGuard | undefined;
    const setupTickets: ReadTicket[] = [];
    function Owner() {
      const g = useReadGuard();
      guard = g;
      useLayoutEffect(() => {
        setupTickets.push(g.begin());
      }, [g]);
      return null;
    }
    const root = createRoot(document.createElement("div"));
    act(() =>
      root.render(
        <StrictMode>
          <Owner />
        </StrictMode>,
      ),
    );
    expect(setupTickets).toHaveLength(2);
    expect(setupTickets[0].current()).toBe(false);
    expect(setupTickets[1].current()).toBe(true);
    const later = guard!.begin(); // e.g. a button pressed after the commit
    expect(later.current()).toBe(true);
    expect(later.signal.aborted).toBe(false);
    act(() => root.unmount());
    expect(later.current()).toBe(false);
    expect(guard!.begin().current()).toBe(false);
  });

  it("keeps a read a child began in its layout effect during the owner's first commit", () => {
    let child: ReadTicket | undefined;
    function Child({ guard }: { guard: ReadGuard }) {
      useLayoutEffect(() => {
        child = guard.begin();
      }, [guard]);
      return null;
    }
    function Owner() {
      const g = useReadGuard();
      return <Child guard={g} />;
    }
    const root = createRoot(document.createElement("div"));
    act(() => root.render(<Owner />));
    expect(child!.current()).toBe(true);
    expect(child!.signal.aborted).toBe(false);
    act(() => root.unmount());
    expect(child!.current()).toBe(false);
  });
});
