// The "about Y reclaimable" figure of the low-disk notice (#3880, phase 5 of #3079), taken from
// the last home-breakdown walk (/cleanup/home, #3092/#3093) and nothing else.
//
// NOTHING HERE WALKS. The 4 s stats path must never start the Agent's whole-home walk, so the
// figure comes only from a finished result: one the Settings › Machine section published, or
// the Agent's own copy of its last walk (GET /cleanup/home never starts one), read once when
// the notice is raised.
import { useSyncExternalStore } from "react";
import { api } from "../api/client.ts";

/** How old a walk may be and still back the figure. A walk is minutes of disk reads, so it is
 *  never repeated for the notice; but build output regrows within a build, so an hour-old walk
 *  can promise space that is no longer there. 30 minutes is the span of one working stint. */
export const RECENT_WALK_MS = 30 * 60 * 1000;

/** How far ahead of this browser's clock a walk's timestamp may be and still count as "now": the
 *  Agent and the browser disagree by seconds, not minutes. Beyond it the stamp is untrustworthy
 *  and the walk is not recent (a day-ahead Agent clock must not stretch "30 minutes" to a day). */
export const CLOCK_SKEW_MS = 5 * 60 * 1000;

/** The part of the /cleanup/home result the estimate reads. */
export interface WalkResult {
  measured_at: string;
  categories: { key: string; parts?: { name: string; bytes: number; reclaimable?: boolean }[] }[];
  repos: { bytes: number; regenerable: number; unjudged?: boolean }[];
}

/** What the cleanup behind the notice's button can take, as the walk saw it: the build output
 *  in working copies plus the tool-cache rows the walk marks reclaimable. The Gradle rows are
 *  left out: the walk sizes them whole, but the cleanup removes only the versions no project
 *  pins, so counting them would promise more than it gives back. Still an estimate (the
 *  button's modal re-measures and lists the exact items); never an upper bound on dormancy. */
export function reclaimBytes(u: WalkResult): number {
  let n = 0;
  for (const r of u.repos) if (!r.unjudged && r.regenerable > 0) n += r.regenerable;
  for (const c of u.categories) {
    if (c.key !== "tool_cache") continue;
    for (const p of c.parts ?? []) if (p.reclaimable && !p.name.startsWith("gradle-")) n += p.bytes;
  }
  return n;
}

interface Held {
  bytes: number;
  measuredAt: number;
  /** The walk's own timestamp string: what marks it spent after a cleanup. */
  stamp: string;
}

let held: Held | null = null;
// Walks a cleanup has already used up. Keyed by the Agent's timestamp rather than a local
// clock so a skewed browser clock cannot revive a figure the cleanup just made false.
const spent = new Set<string>();
let version = 0;
const listeners = new Set<() => void>();

function emit(): void {
  version++;
  listeners.forEach((fn) => fn());
}

/** Offers a finished walk. Ignored when it is older than what is held, already spent, or
 *  unreadable. */
export function publishWalk(u: WalkResult | null | undefined): void {
  if (!u || typeof u.measured_at !== "string" || spent.has(u.measured_at)) return;
  const at = Date.parse(u.measured_at);
  if (!isFinite(at) || (held && at <= held.measuredAt)) return;
  held = { bytes: reclaimBytes(u), measuredAt: at, stamp: u.measured_at };
  emit();
}

/** A cleanup ran (or may have; callers invalidate on an uncertain outcome too): the held walk no longer describes the disk, and neither does the Agent's
 *  copy of it (it keeps the last result until a new walk finishes). */
export function markCleaned(): void {
  if (held) spent.add(held.stamp);
    held = null;
  emit();
}

/** The figure to show, or null: no walk, a walk older than RECENT_WALK_MS, or nothing in it. */
export function reclaimEstimate(now = Date.now()): number | null {
  if (!held || held.bytes <= 0) return null;
  const age = now - held.measuredAt;
  return age >= -CLOCK_SKEW_MS && age <= RECENT_WALK_MS ? held.bytes : null;
}

/** When the held walk stops being recent (ms epoch), for a view to re-check at. */
export function reclaimExpiresAt(): number | null {
  return held ? held.measuredAt + RECENT_WALK_MS : null;
}

/** Reads the Agent's last finished walk once (a plain GET: it never starts one) and offers it.
 *  Called when the notice is raised, never per stats tick. */
export async function loadCachedWalk(): Promise<void> {
  try {
    const res = await api("api/cleanup/home");
    if (res && !res.error && res.result) publishWalk(res.result);
  } catch {
    /* no figure is the same as the notice without one */
  }
}

export function useReclaimEstimate(): number | null {
  useSyncExternalStore(
    (fn) => {
      listeners.add(fn);
      return () => listeners.delete(fn);
    },
    () => version,
    () => 0,
  );
  return reclaimEstimate();
}

export const __test = {
  reset: () => {
    held = null;
    spent.clear();
    version = 0;
  },
};
