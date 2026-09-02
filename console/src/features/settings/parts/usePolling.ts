import { useCallback, useEffect, useRef } from "react";

// usePolling factors out the device/OAuth poll skeleton shared by the GitHub,
// Bitbucket and Codex connect flows: an alive-ref that stops ticks after the row
// unmounts, a deadline, and the recursive setTimeout. Each caller supplies `step`
// (one poll iteration → whether it's terminal + the next delay) and `onExpire`, so
// the provider-specific request/handling stays local while the timing mechanism lives
// here once instead of being copy-pasted per card.
export interface PollStep {
  stop: boolean; // true = terminal (connected / failed) → stop ticking
  nextMs?: number; // delay before the next tick (falls back to firstDelayMs)
}

export function usePolling() {
  const alive = useRef(true);
  useEffect(
    () => () => {
      alive.current = false;
    },
    [],
  );
  return useCallback(
    (opts: { deadlineMs: number; firstDelayMs: number; step: () => Promise<PollStep>; onExpire: () => void }) => {
      const deadline = Date.now() + opts.deadlineMs;
      const tick = async () => {
        if (!alive.current) return;
        if (Date.now() > deadline) {
          opts.onExpire();
          return;
        }
        const r = await opts.step();
        if (r.stop || !alive.current) return;
        setTimeout(tick, r.nextMs ?? opts.firstDelayMs);
      };
      setTimeout(tick, opts.firstDelayMs);
    },
    [],
  );
}
