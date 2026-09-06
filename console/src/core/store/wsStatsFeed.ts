// wsStatsFeed — a per-tick time series of this workspace's own resource use, for the
// Machine tab's charts (the WS bar keeps its own sparklines and is untouched).
//
// WHY IT CANNOT REUSE THE WS BAR'S HISTORY. Nothing in the system produces one sample per
// tick. The CP only sends a stats frame when the payload CHANGED (events.go: roundStats
// floors memory to 8 MiB and rounds cpu_pct to an integer, then emit() diffs the bytes), and
// the WS bar additionally skips its repaint when the displayed values are unchanged. So an
// idle workspace adds no points at all: that history is a change log, not a time series, and
// a chart drawn from it stands still.
//
// SO THIS KEEPS ITS OWN CLOCK. Every SAMPLE_MS it appends the latest value the push stream
// delivered. A tick with no new frame is not missing data — the suppression above means the
// rounded value did not change, so repeating it is what actually happened. That reasoning
// holds only while the stream is LIVE; when it is not, the tick appends a GAP (null) instead
// of extending a line the data does not support.
//
// AND IT COSTS NO REQUESTS in the normal case. The fallback poll runs only while a viewer is
// mounted AND the push stream is down, because on ecs-ec2 every GET /api/workspace/stats is
// a DescribeVolumes + DescribeServices (the adapter's State()), and the events tick is
// already paying for one of those every 4 seconds — a second poller doubles that per user.
import { useSyncExternalStore } from "react";
import { api, getTenant } from "../api/client.ts";
import { onPush, pushHealthy } from "../push/events.ts";

/** One reading. cpu/mem are null for a GAP — a tick whose value could not be established.
 *  Charts must break the line there rather than joining across it. */
export interface StatSample {
  /** Wall clock (ms). The chart maps x by TIME, not by index, so a throttled background tab
   *  (timers drop to ~1/min) shows a gap instead of silently compressing the axis. */
  t: number;
  /** Percent, one core = 100% (the docker stats convention), so a 2-vCPU box tops out at 200. */
  cpu: number | null;
  memUsed: number | null;
  memMax: number | null;
  diskUsed: number | null;
  diskTotal: number | null;
  /** A process in this container was OOM-killed around this sample (the CP's oom_recent).
   *  Carried per sample rather than read live so the section can still say "it happened,
   *  and it happened HERE" after the flag itself has expired. */
  oom: boolean;
}

const SAMPLE_MS = 4000; // the CP's own event tick; sampling faster would only repeat values
const CAP = 900; // 1 hour of samples — the WS bar's 60 (4 minutes) is what this exists to lift

let samples: StatSample[] = [];
let version = 0;
let latest: any = null; // the last stats payload seen (push or poll)
let latestAt = 0;
let tenant = "";
let timer = 0;
let viewers = 0;
let polling = false;
const listeners = new Set<() => void>();

function emit(): void {
  version++;
  listeners.forEach((fn) => fn());
}

function reset(): void {
  samples = [];
  latest = null;
  latestAt = 0;
  emit();
}

/** Whether the last reading can still be believed. A live push stream means "unchanged",
 *  because the CP would have sent a frame otherwise; a dead one means "unknown". */
function believable(now: number): boolean {
  if (!latest) return false;
  if (pushHealthy()) return true;
  // Polling fallback: trust our own reading for a couple of intervals, then admit ignorance.
  return now - latestAt < 3 * SAMPLE_MS;
}

// now is injectable so a test can lay out a realistic series in time; production always
// passes the real clock.
function sample(now = Date.now()): void {
  // A tenant switch is a different workspace: the old series would silently continue as if
  // it were this one's.
  const t = getTenant();
  if (t !== tenant) {
    tenant = t;
    reset();
  }
  const d = believable(now) ? latest : null;
  const running = !!(d && d.running);
  samples.push({
    t: now,
    cpu: running && typeof d.cpu_pct === "number" ? d.cpu_pct : null,
    memUsed: running && typeof d.mem_used === "number" ? d.mem_used : null,
    memMax: running && typeof d.mem_max === "number" ? d.mem_max : null,
    diskUsed: running && typeof d.disk_used === "number" ? d.disk_used : null,
    diskTotal: running && typeof d.disk_total === "number" ? d.disk_total : null,
    oom: !!(running && d.oom_recent),
  });
  if (samples.length > CAP) samples = samples.slice(-CAP);
  emit();

  // Only reach for the network when the push channel cannot answer and somebody is looking.
  const hidden = typeof document !== "undefined" && document.hidden;
  if (viewers > 0 && !pushHealthy() && !hidden && !polling) {
    polling = true;
    api("api/workspace/stats")
      .then((res: any) => {
        if (res && !res.error) {
          latest = res;
          latestAt = Date.now();
        }
      })
      .catch(() => {
        /* leave the last reading to expire on its own (believable) */
      })
      .finally(() => {
        polling = false;
      });
  }
}

/** Register the feed against the push channel and start its clock. Called once from
 *  wirePushApply(); returns the cleanup so StrictMode's double-invoke is harmless. */
export function wireWsStatsFeed(): () => void {
  tenant = getTenant();
  const un = onPush("stats", (d: any) => {
    if (!d || d.error) return;
    latest = d;
    latestAt = Date.now();
  });
  timer = window.setInterval(sample, SAMPLE_MS);
  return () => {
    un();
    window.clearInterval(timer);
    timer = 0;
    reset();
  };
}

/** The series, for a chart. Being mounted marks a VIEWER, which is what permits the
 *  fallback poll — nothing polls for a chart nobody is looking at. */
export function useWsStatsSeries(): StatSample[] {
  // The snapshot is the VERSION, not the array: the buffer is mutated in place on every
  // tick, so an array identity check would never fire (and a fresh copy per tick would
  // allocate 900 objects every 4 seconds for nothing).
  useSyncExternalStore(subscribeViewer, () => version, () => 0);
  return samples;
}

function subscribeViewer(fn: () => void): () => void {
  viewers++;
  listeners.add(fn);
  return () => {
    viewers--;
    listeners.delete(fn);
  };
}

// --- test seam -------------------------------------------------------------------------
// The series is module state on purpose (one clock for the whole app); these let a test
// drive it without a DOM timer.
export const __test = {
  reset: () => {
    samples = [];
    latest = null;
    latestAt = 0;
    version = 0;
    viewers = 0;
    tenant = getTenant();
  },
  sample,
  setLatest: (d: any, at = Date.now()) => {
    latest = d;
    latestAt = at;
  },
  samples: () => samples,
  addViewer: (n: number) => {
    viewers += n;
  },
};
