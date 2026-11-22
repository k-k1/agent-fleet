import { useEffect, useRef } from "react";

/** Run `fn` every `ms` while mounted; `ms === null` switches the timer off (gating a poll on a
 *  condition). The latest `fn` is always the one called, so callers need no stable callback and a
 *  changed closure does not restart the period. The timer is cleared on unmount and whenever `ms`
 *  changes. It does not fire immediately and does not pause while the tab is hidden: callers do
 *  their own first read, exactly as they did with a bare setInterval. */
export function useInterval(fn: () => void, ms: number | null): void {
  const latest = useRef(fn);
  latest.current = fn;
  useEffect(() => {
    if (ms === null) return;
    const id = setInterval(() => latest.current(), ms);
    return () => clearInterval(id);
  }, [ms]);
}
