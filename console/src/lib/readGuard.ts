import { useLayoutEffect, useRef, useState } from "react";
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

/** The subject of a guard whose owner unmounted: no begin() matches it. */
const GONE = Symbol("gone");

/**
 * Generation guard for reads whose answers can come back out of order.
 *
 * `reset(subject)` starts a new subject: every read begun before it can no longer land.
 * `begin(subject)` hands out a ticket for the subject the CALLER was made for, and that ticket
 * is dead from the start when the guard has moved on: a callback that closed over the folder
 * being left (a rename's follow-up refresh, a backoff timer not yet cleared) must not take a
 * ticket of the folder now on screen and land its listing there. Subjects compare with
 * Object.is, so pass a key, not a fresh array.
 *
 * Within one subject a read lands only if no read started after it has landed, so a slow older
 * answer cannot overwrite a newer one, while an answer slower than the poll period still shows.
 *
 * Lanes keep separate "latest landed" marks for parts of the state that different answers own.
 * A failure that lands the error banner must not also stop an older successful read from
 * landing the list: that was a switch at a tick leaving the previous tenant's rows on screen
 * until the next poll (#4815).
 *
 * Reset from a layout effect, never during render: a render React discards would otherwise
 * kill the reads of the subject still on screen.
 */
export class ReadGuard {
  private gen = 0;
  // Never reset, so numbers stay ordered across generations.
  private seq = 0;
  private landed = new Map<string, number>();
  private ac = new AbortController();
  private subject: unknown = undefined;

  reset(subject?: unknown): void {
    // Everything new is in place before the old signal fires: an abort listener that begins
    // the next read must get a live ticket and a live signal.
    const old = this.ac;
    this.gen++;
    this.subject = subject;
    this.landed.clear();
    this.ac = new AbortController();
    old.abort();
  }

  begin(subject?: unknown): ReadTicket {
    const live = Object.is(subject, this.subject);
    const g = live ? this.gen : -1;
    const n = ++this.seq;
    const signal = live ? this.ac.signal : AbortSignal.abort();
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

/**
 * A ReadGuard for the component's lifetime. Unmount (a layout cleanup, so before any passive
 * cleanup or timer can run) leaves it on a subject nothing matches; the setup re-arms it, since
 * StrictMode and a Suspense boundary that hides and re-shows the tree run the cleanup on a
 * component that stays mounted.
 *
 * One-off reads (a retry button, an event) take their ticket when they begin, before the first
 * await, with the subject their callback was made for, and check it after every await. A
 * ticket taken later, after a confirm dialog say, belongs to whatever subject is current by
 * then: pass the subject so a callback that outlived its own gets a dead ticket.
 */
export function useReadGuard(): ReadGuard {
  const [guard] = useState(() => new ReadGuard());
  useLayoutEffect(() => {
    guard.reset();
    return () => guard.reset(GONE);
  }, [guard]);
  return guard;
}

/**
 * Poll `read` every `ms` behind a ReadGuard. A change of `subject` resets the guard and reads at
 * once; the period itself is not restarted, which the guard makes safe. `ms === null` switches
 * the poll off: no first read, no ticks, and reads still in flight are dropped. `subject` must
 * keep the same length across renders, like any dependency list.
 *
 * The callback, the on/off state and the reset are all taken at commit (layout phase), so a
 * tick never runs a render that was not committed, and the previous subject's reads are dead
 * from the commit that replaced it.
 * Returns the guard so one-off reads can take tickets from it too (see useReadGuard). Its
 * subject is unnamed, so a one-off read must take its ticket before its first await: a ticket
 * begun later cannot tell that the subject it was meant for has gone.
 */
export function useGuardedPoll(
  read: (t: ReadTicket) => void,
  ms: number | null,
  subject: readonly unknown[],
): ReadGuard {
  const guard = useReadGuard();
  const committed = useRef(read);
  const enabled = useRef(false);
  const on = ms !== null;
  useLayoutEffect(() => {
    committed.current = read;
  });
  useLayoutEffect(() => {
    guard.reset();
    enabled.current = on;
    if (on) committed.current(guard.begin());
    return () => {
      enabled.current = false;
      guard.reset(GONE);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [guard, on, ...subject]);
  // The interval's own timer is cleared in a passive cleanup, after the commit that switched the
  // poll off; `enabled` closes that window.
  useInterval(() => {
    if (enabled.current) committed.current(guard.begin());
  }, ms);
  return guard;
}
