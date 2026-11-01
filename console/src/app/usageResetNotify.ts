// Subscription-limit reset notification (WsBar usage chips). When a limit window the
// user was actually constrained by (utilization reached ARM_PCT) rolls over, tell them
// "you can resume now" — via a browser Notification and, when text-to-speech is on, a
// short TTS announcement. Reuses the data the usage chip already has: every reading
// carries resetsAt per window. The Control Plane owns the armed/reset state so PC and
// phone cannot disagree; this hook only submits observations and schedules one precise
// re-check at the reset instant while the tab is open.
import { useEffect, useRef } from "react";
import { apiJSON } from "../core/api/client.ts";

// Utilization (0–100) at/above which we consider the window "constrained" and arm a
// reset notification for it. Below this a reset is routine (happens every window) and
// notifying would be noise — the value is only "you were blocked, now you're not".
const ARM_PCT = 90;

interface Src {
  endpoint: string;
  // The agent kind, which is also the wire value: the API refuses a `source` that is not
  // "claude" or "codex" (control-plane/notification.go). This used to be the chip's DISPLAY
  // name lowercased, which happened to match — a label edit would have turned every
  // observation into a 400 with nothing in the UI to show for it.
  kind: string;
}
interface Win {
  pct: number;
  resetsAt: string;
  stale?: boolean;
  derived?: boolean;
}

// The chip reading as far as this hook reads it. observedAt and limitEpoch are what the Agent
// stamps for the limit board (ADR 0111 decision 6): the provider's own observation time and the
// opaque login epoch. The Agent leaves both out when the reading predates the account now
// signed in.
export interface UsageForNotify {
  fiveHour?: Win;
  sevenDay?: Win;
  observedAt?: string;
  limitEpoch?: string;
  // When the Agent first saw this login: it orders epochs, so a post that sat in a sleeping tab
  // across a re-login is refused rather than bringing the previous account back.
  limitEpochSince?: string;
}

// Agent kinds the API accepts as an observation `source` (observeUsage in
// control-plane/notification.go). Any other kind (muse) keeps its chip but posts nothing:
// the CP answers it with 400 bad_source. Keep in step with the CP's list.
const OBSERVABLE_KINDS: ReadonlySet<string> = new Set(["claude", "codex"]);

// usageObservationBody is the POST /api/notifications/usage-observations body, or null when
// there is nothing to send.
//
// A stale window is not an observation: its 0% and its reset instant are both extrapolated
// from a reading that predates the window (the agent flags this when the capture went quiet).
// Submitting it would let a DEAD capture look like a reset and fire "the limit has cleared"
// while the user is still blocked.
//
// observedAt is passed through as the Agent reported it and never filled in here: the time
// this tab sends is not the time the provider observed anything, and the CP keeps a window
// without one off the limit board.
export function usageObservationBody(kind: string, usage: UsageForNotify | null) {
  if (!usage || !OBSERVABLE_KINDS.has(kind)) return null;
  const windows = [
    { key: "5h", w: usage.fiveHour },
    { key: "7d", w: usage.sevenDay },
  ]
    .filter((x) => x.w?.resetsAt && !x.w.stale)
    .map(({ key, w }) => ({
      windowKey: key,
      percent: w!.pct,
      resetsAt: w!.resetsAt,
      ...(usage.observedAt ? { observedAt: usage.observedAt } : {}),
      ...(w!.derived ? { derived: true } : {}),
    }));
  if (!windows.length) return null;
  return {
    source: kind,
    windows,
    ...(usage.limitEpoch ? { limitEpoch: usage.limitEpoch, limitEpochSince: usage.limitEpochSince } : {}),
  };
}

// useUsageResetNotify tracks one agent's two windows and fires once each time a
// constrained window resets. `refresh` (the chip's forced re-read) is called via a
// timer at the reset instant so the notification is prompt while the tab is open;
// closed-tab resets are caught on the next reading by the resetsAt-moved compare.
export function useUsageResetNotify(
  src: Src,
  usage: UsageForNotify | null,
  refresh: () => void,
) {
  const timers = useRef<ReturnType<typeof setTimeout>[]>([]);
  // refresh gets a fresh identity each render; hold it in a ref so it isn't an effect
  // dep (the effect should run on real reading changes, not every parent re-render).
  const refreshRef = useRef(refresh);
  refreshRef.current = refresh;

  useEffect(() => {
    // Clear any timers from the previous reading before (maybe) re-arming.
    for (const t of timers.current) clearTimeout(t);
    timers.current = [];
    if (!usage) return;

    const windows: { key: string; w?: Win }[] = [
      { key: "5h", w: usage.fiveHour },
      { key: "7d", w: usage.sevenDay },
    ];

    const body = usageObservationBody(src.kind, usage);
    if (body) void apiJSON("api/notifications/usage-observations", "POST", body);

    for (const { w } of windows) {
      if (!w || !w.resetsAt) continue;
      const reset = new Date(w.resetsAt).getTime();

      // While the tab is open, re-check right at the reset instant so the notice is
      // prompt instead of waiting for the slow 5-min poll. The forced refresh yields a
      // reading whose resetsAt has moved, which the compare above then acts on. Skip
      // absurd delays (setTimeout clamps huge values); the weekly window can be days
      // out — the poll/on-return path still covers it.
      if (w.pct >= ARM_PCT) {
        const delay = reset - Date.now() + 5000;
        if (delay > 0 && delay < 26 * 3600 * 1000) {
          timers.current.push(setTimeout(() => refreshRef.current(), delay));
        }
      }
    }

    return () => {
      for (const t of timers.current) clearTimeout(t);
      timers.current = [];
    };
  }, [usage, src.endpoint, src.kind]);
}
