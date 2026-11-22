// useApiLoad / useApiOnce — the shared "read from the Control Plane" hooks of the settings
// tabs. They carry the rules each tab used to repeat inline:
//   • a transient failure (the Agent answering 5xx right after a start) is retried, never
//     settled as an error;
//   • an answer for a superseded request (deps changed, unmounted) is dropped;
//   • the reply is handed over whole, `Reply<T>`, so the caller decides what an error looks like.
// The request and its callback are paired by the effect that started it (the closure of the
// render whose deps changed), never through a ref written during render: a render that suspends
// or is discarded must not hand its callback another render's answer.
import { useEffect } from "react";
import { isTransientErr } from "../../../core/api/client.ts";
import { useRetryLoad } from "../../../lib/retryLoad.ts";
import type { Reply } from "../api.ts";

/** Read on mount and whenever `deps` change; retry transient failures with backoff. */
export function useApiLoad<T>(
  fetch: () => Promise<Reply<T>>,
  onReply: (reply: Reply<T>) => void,
  deps: unknown[],
): void {
  useRetryLoad(async (signal) => {
    const res = await fetch();
    if (signal.aborted) return true;
    if (isTransientErr(res)) return false;
    onReply(res);
    return true;
  }, deps);
}

/**
 * Read once per `deps` change, with no retry. `onFail` runs when the request rejects or when
 * `onReply` throws (the old `.then().catch()` call sites did the same), `onReply` for every
 * answer, error replies included. Nothing runs after the deps changed or the component
 * unmounted. A null `fetch` reads nothing.
 */
export function useApiOnce<T>(
  fetch: (() => Promise<Reply<T>>) | null,
  onReply: (reply: Reply<T>) => void,
  onFail: () => void,
  deps: unknown[],
): void {
  useEffect(() => {
    if (!fetch) return;
    let live = true;
    fetch()
      .then((res) => live && onReply(res))
      .catch(() => live && onFail());
    return () => {
      live = false;
    };
    // The deps are the caller's: the closure of the render they changed in is the one that runs.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps);
}
