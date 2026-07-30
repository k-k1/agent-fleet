import { useEffect } from "react";

// useRetryLoad runs `load` on mount and whenever `deps` change, retrying with capped
// exponential backoff whenever load reports a TRANSIENT failure. That failure is the state
// a workspace is in for a moment right after it starts: the CP answers a proxied request
// with a plain-text 502 ("workspace agent unreachable"), which api() resolves — NOT throws
// — as { error: http_5xx } (see isTransientErr). Without a retry, a pane that mounted
// during that window commits an empty / "not found" state and never recovers even once the
// agent is up. This also re-runs when the tab regains focus (until a terminal result
// lands), covering a pane that was already mounted when the WS came up.
//
// `load(signal)` MUST:
//   • return true once it has a terminal result (success OR a genuine error) — stops retrying;
//   • return false to request a retry (a transient failure);
//   • check `signal.aborted` before committing state (deps changed / component unmounted).
export function useRetryLoad(load: (signal: AbortSignal) => Promise<boolean>, deps: unknown[]): void {
  useEffect(() => {
    const ac = new AbortController();
    let timer = 0;
    let tries = 0;
    let settled = false;
    let inFlight = false; // a load() is awaiting — onVis must not start a second chain
    const run = () => {
      inFlight = true;
      load(ac.signal)
        .then((done) => {
          inFlight = false;
          if (ac.signal.aborted) return;
          if (done) {
            settled = true;
            return;
          }
          const delay = Math.min(5000, 700 * 2 ** Math.min(tries, 3));
          tries++;
          timer = window.setTimeout(run, delay);
        })
        .catch(() => {
          // load owns its error handling; a stray throw shouldn't wedge the loop — retry.
          inFlight = false;
          if (ac.signal.aborted) return;
          const delay = Math.min(5000, 700 * 2 ** Math.min(tries, 3));
          tries++;
          timer = window.setTimeout(run, delay);
        });
    };
    const onVis = () => {
      // Skip while a load is already in flight: re-entering run() here would fork a
      // second retry chain (both settle and each schedules its own timer). The
      // in-flight load schedules the next attempt itself, so nothing is lost.
      if (!document.hidden && !ac.signal.aborted && !settled && !inFlight) {
        tries = 0;
        window.clearTimeout(timer);
        run();
      }
    };
    run();
    document.addEventListener("visibilitychange", onVis);
    return () => {
      ac.abort();
      window.clearTimeout(timer);
      document.removeEventListener("visibilitychange", onVis);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps);
}
