// Live memory / CPU for the visible, running cards of the Members roster (#2389).
//
// The roster is one GET …/members; this is the only thing that may add per-member calls, and it
// is deliberately gentle: only members the caller marks wanted (running AND on screen), one
// request at a time per member, a small cap across all of them, a long interval, and a back-off
// instead of a retry on failure. Pausing (hidden tab) and stop() (unmount) cancel everything.
//
// The poller is a plain object owned by one MembersPanel, not module state: a module-scope
// schedule would leak between panels and between tests.
import { useEffect, useRef, useState } from "react";
import { api } from "../../../core/api/client.ts";

export type LiveReading = { mem_used: number; mem_max?: number; cpu_pct?: number };

export const LIVE_INTERVAL_MS = 15_000;
export const LIVE_MAX_CONCURRENT = 3;
// A card must stay wanted this long (per member) before it is fetched: scrolling fast past thirty cards would
// otherwise queue a request for each one that flashed through the viewport (measured: 27 of 30
// running cards fetched during one fast pass).
export const LIVE_DWELL_MS = 300;
// After n consecutive failures the next try waits interval × 2^n, n capped here (15 s → 2 min).
const MAX_BACKOFF_DOUBLINGS = 3;

type Entry = { nextAt: number; failures: number; ctl?: AbortController };

type Opts = { interval: number; max: number; dwell: number };

export class LivePoller {
  private entries = new Map<string, Entry>();
  // member → when it became wanted (continuously); a member is fetched only after `dwell` of that.
  private wanted = new Map<string, number>();
  private inflight = 0;
  private paused: boolean;
  private stopped = false;
  private timer: ReturnType<typeof setTimeout> | undefined;

  constructor(
    private load: (key: string, signal: AbortSignal) => Promise<LiveReading | null>,
    private onReading: (key: string, r: LiveReading | null) => void,
    paused: boolean,
    private opts: Opts = { interval: LIVE_INTERVAL_MS, max: LIVE_MAX_CONCURRENT, dwell: LIVE_DWELL_MS },
  ) {
    this.paused = paused;
  }

  // Removal is immediate (and aborts that member's request); only an addition waits out the dwell.
  setWanted(keys: Iterable<string>) {
    const now = Date.now();
    const next = new Map<string, number>();
    for (const k of keys) next.set(k, this.wanted.get(k) ?? now);
    for (const k of this.wanted.keys()) if (!next.has(k)) this.abort(k);
    this.wanted = next;
    this.pump();
  }

  setPaused(p: boolean) {
    this.paused = p;
    if (p) {
      this.clearTimer();
      for (const k of this.entries.keys()) this.abort(k);
    } else this.pump();
  }

  stop() {
    this.stopped = true;
    this.clearTimer();
    for (const k of this.entries.keys()) this.abort(k);
  }

  // Cancel the member's request in flight, if any. An abort is not a failure: no back-off, and the
  // slot it held is free at once.
  private abort(key: string) {
    const e = this.entries.get(key);
    if (!e?.ctl) return;
    e.ctl.abort();
    e.ctl = undefined;
    this.inflight--;
  }

  private clearTimer() {
    if (this.timer !== undefined) clearTimeout(this.timer);
    this.timer = undefined;
  }

  // Start every due wanted member up to the cap, then sleep until the earliest next due time.
  // Entries outlive a scroll-out, so scrolling a card away and back inside one interval does not
  // fetch again.
  private pump() {
    this.clearTimer();
    if (this.stopped || this.paused) return;
    const now = Date.now();
    let wake = Infinity;
    for (const [key, since] of this.wanted) {
      let e = this.entries.get(key);
      if (!e) this.entries.set(key, (e = { nextAt: 0, failures: 0 }));
      if (e.ctl) continue;
      const due = Math.max(e.nextAt, since + this.opts.dwell);
      if (due <= now && this.inflight < this.opts.max) this.start(key, e);
      else if (due > now) wake = Math.min(wake, due);
    }
    // Members left over because the cap was full are picked up by pump() when a request ends.
    if (wake !== Infinity) this.timer = setTimeout(() => this.pump(), Math.max(0, wake - now));
  }

  private start(key: string, e: Entry) {
    const ctl = new AbortController();
    e.ctl = ctl;
    this.inflight++;
    const done = (r: LiveReading | null, ok: boolean) => {
      if (e.ctl !== ctl) return; // aborted: already accounted for
      e.ctl = undefined;
      this.inflight--;
      e.failures = ok ? 0 : e.failures + 1;
      e.nextAt = Date.now() + this.opts.interval * 2 ** Math.min(e.failures, MAX_BACKOFF_DOUBLINGS);
      if (this.stopped) return;
      if (ok) this.onReading(key, r);
      this.pump();
    };
    this.load(key, ctl.signal).then(
      (r) => done(r, true),
      () => done(null, false),
    );
  }
}

// parseReading — null when the workspace is not running or the Agent had no memory figure yet
// (the card then keeps its placeholder).
export function parseReading(s: any): LiveReading | null {
  if (!s || s.error || s.running !== true || typeof s.mem_used !== "number") return null;
  return {
    mem_used: s.mem_used,
    mem_max: typeof s.mem_max === "number" && s.mem_max > 0 ? s.mem_max : undefined,
    cpu_pct: typeof s.cpu_pct === "number" ? s.cpu_pct : undefined,
  };
}

// useLiveStats — readings for the wanted keys. `wanted` must be only running, on-screen members.
export function useLiveStats(slug: string, wanted: string[]): Record<string, LiveReading> {
  const [readings, setReadings] = useState<Record<string, LiveReading>>({});
  const poller = useRef<LivePoller | null>(null);
  const wantedKey = wanted.slice().sort().join("\n");

  useEffect(() => {
    setReadings({});
    const p = new LivePoller(
      async (key, signal) => {
        const s = await api(`api/admin/tenants/${encodeURIComponent(slug)}/members/${encodeURIComponent(key)}/stats?disk=0`, { signal });
        if (s?.error) throw new Error("stats failed"); // counts as a failure: back off, keep the last reading
        return parseReading(s);
      },
      (key, r) =>
        setReadings((prev) => {
          if (!r) {
            if (!(key in prev)) return prev;
            const { [key]: _gone, ...rest } = prev;
            return rest;
          }
          const o = prev[key];
          return o && o.mem_used === r.mem_used && o.mem_max === r.mem_max && o.cpu_pct === r.cpu_pct
            ? prev
            : { ...prev, [key]: r };
        }),
      document.hidden,
    );
    poller.current = p;
    const onVis = () => p.setPaused(document.hidden);
    document.addEventListener("visibilitychange", onVis);
    return () => {
      document.removeEventListener("visibilitychange", onVis);
      p.stop();
      if (poller.current === p) poller.current = null;
    };
  }, [slug]);

  // Declared after the effect above so a strict-mode remount re-creates the poller first.
  useEffect(() => {
    poller.current?.setWanted(wantedKey ? wantedKey.split("\n") : []);
  }, [slug, wantedKey]);

  return readings;
}
