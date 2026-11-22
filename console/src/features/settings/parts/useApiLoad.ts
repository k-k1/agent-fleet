// useApiLoad / useApiOnce — the shared "read from the Control Plane" hooks of the settings
// tabs. They carry the three rules each tab used to repeat inline:
//   • a transient failure (the Agent answering 5xx right after a start) is retried, never
//     settled as an error;
//   • an answer for a superseded request (deps changed, unmounted) is dropped;
//   • the reply is handed over whole, `Reply<T>`, so the caller decides what an error looks like.
import { useEffect, useRef } from "react";
import { isTransientErr } from "../../../core/api/client.ts";
import { useRetryLoad } from "../../../lib/retryLoad.ts";
import type { Reply } from "../api.ts";

/**
 * Read on mount and whenever `deps` change; retry transient failures with backoff.
 * `fetch` and `onReply` are read through refs, so they may close over the current render
 * without being listed in `deps`.
 */
export function useApiLoad<T>(
  fetch: () => Promise<Reply<T>>,
  onReply: (reply: Reply<T>) => void,
  deps: unknown[],
): void {
  const fetchRef = useRef(fetch);
  const replyRef = useRef(onReply);
  fetchRef.current = fetch;
  replyRef.current = onReply;
  useRetryLoad(async (signal) => {
    const res = await fetchRef.current();
    if (signal.aborted) return true;
    if (isTransientErr(res)) return false;
    replyRef.current(res);
    return true;
  }, deps);
}

/**
 * Read once per `deps` change, with no retry. `onFail` runs when the request itself rejects
 * (network down), `onReply` for every answer, error replies included. Nothing runs after the
 * deps changed or the component unmounted. A null `fetch` reads nothing.
 */
export function useApiOnce<T>(
  fetch: (() => Promise<Reply<T>>) | null,
  onReply: (reply: Reply<T>) => void,
  onFail: () => void,
  deps: unknown[],
): void {
  const fetchRef = useRef(fetch);
  const replyRef = useRef(onReply);
  const failRef = useRef(onFail);
  fetchRef.current = fetch;
  replyRef.current = onReply;
  failRef.current = onFail;
  useEffect(() => {
    const read = fetchRef.current;
    if (!read) return;
    let live = true;
    read()
      .then((res) => live && replyRef.current(res))
      .catch(() => live && failRef.current());
    return () => {
      live = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps);
}
