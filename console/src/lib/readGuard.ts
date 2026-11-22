import { useEffect, useRef, useState } from "react";
import { useInterval } from "./useInterval.ts";

/** One read handed out by a ReadGuard. Check it after every await, before the answer touches
 *  state: a fixed-period poll no longer restarts when its subject changes, so an answer can
 *  arrive for a subject that is gone, or after a read that started later has already landed. */
export interface ReadTicket {
  /** Aborted when the guard is reset (the subject changed) or its owner unmounts. */
  readonly signal: AbortSignal;
  /** The subject this read was started for (tenant, folder, mode, conversation) is still the
   *  current one. */
  current(): boolean;
  /** current(), and no read started after this one has landed in `lane`. */
  fresh(lane?: string): boolean;
  /** fresh(lane), and if so record this read as the latest one landed in `lane`. */
  land(lane?: string): boolean;
}

/**
 * Generation guard for reads whose answers can come back out of order.
 *
 * `reset()` starts a new subject: every read begun before it can no longer land. Within one
 * subject a read lands only if no read started after it has landed, so a slow older answer
 * cannot overwrite a newer one, while an answer slower than the poll period still shows.
 *
 * Lanes keep separate "latest landed" marks for parts of the state that different answers own.
 * A failure that lands the error banner must not also stop an older successful read from
 * landing the list: that was a switch at a tick leaving the previous tenant's rows on screen
 * until the next poll (#4815).
 */
export class ReadGuard {
  private gen = 0;
  // Never reset, so numbers stay ordered across generations.
  private seq = 0;
  private landed = new Map<string, number>();
  private ac = new AbortController();

  reset(): void {
    this.gen++;
    this.landed.clear();
    this.ac.abort();
    this.ac = new AbortController();
  }

  begin(): ReadTicket {
    const g = this.gen;
    const n = ++this.seq;
    const signal = this.ac.signal;
    const current = () => g === this.gen;
    const fresh = (lane = "") => current() && n >= (this.landed.get(lane) ?? 0);
    const land = (lane = "") => {
      if (!fresh(lane)) return false;
      this.landed.set(lane, n);
      return true;
    };
    return { signal, current, fresh, land };
  }
}

/** A ReadGuard for the component's lifetime, reset on unmount so nothing lands after it. */
export function useReadGuard(): ReadGuard {
  const [guard] = useState(() => new ReadGuard());
  useEffect(() => () => guard.reset(), [guard]);
  return guard;
}

/**
 * Poll `read` every `ms` behind a ReadGuard. A change of `subject` resets the guard and reads at
 * once; the period itself is not restarted, which the guard makes safe. `ms === null` switches
 * the poll off: no first read, no ticks, and reads still in flight are dropped (the guard is
 * reset). `subject` must keep the same length across renders, like any dependency list.
 * Returns the guard so one-off reads (a retry button, an event) can take tickets from it too.
 */
export function useGuardedPoll(
  read: (t: ReadTicket) => void,
  ms: number | null,
  subject: readonly unknown[],
): ReadGuard {
  const guard = useReadGuard();
  const latest = useRef(read);
  latest.current = read;
  const on = ms !== null;
  useEffect(() => {
    guard.reset();
    if (on) latest.current(guard.begin());
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [guard, on, ...subject]);
  useInterval(() => latest.current(guard.begin()), ms);
  return guard;
}
