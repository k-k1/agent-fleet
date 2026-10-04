import { useEffect } from "react";
import type { MirrorState } from "./useMirrorState.ts";

// How long the "working" indicator is held after a turn reads idle while its reply is
// still not in the transcript (the idle→reply-renders gap). Long enough to cover the
// jsonl-write / poll-cadence lag, short enough that a genuinely reply-less turn (e.g. an
// interrupt) doesn't leave a phantom spinner. See `finalizing`.
const FINALIZE_GRACE_MS = 8000;

/** useFinalizeHold drives MirrorState's `finalizing` from the polled status and replyPending. */
export function useFinalizeHold({ st, replyPending }: { st: MirrorState; replyPending: boolean }) {
  const { status, bgBusy, setFinalizing, finalizingRef, wasWorkingRef, tickRef } = st;
  // Hold the "working" indicator across the idle→reply-renders gap (see `finalizing`).
  // finalizingRef is set SYNCHRONOUSLY alongside the state so the poll loop's next-tick
  // cadence sees it immediately; entering the hold also kicks a fast re-poll, because the
  // tick that first read idle already scheduled the slow (3s) interval before this ran.
  const setFinalize = (on: boolean) => {
    finalizingRef.current = on;
    setFinalizing(on);
  };
  useEffect(() => {
    if (status === "working" || bgBusy) {
      wasWorkingRef.current = true; // a turn is (or was just) running
      setFinalize(false);
      return;
    }
    if (!replyPending) {
      // The reply has landed (or there's nothing to wait for): clear, and re-arm so the
      // bridge only ever applies to a turn we actually watched run.
      wasWorkingRef.current = false;
      setFinalize(false);
      return;
    }
    if (!wasWorkingRef.current) {
      // replyPending but we never saw work this cycle (a plain history view whose last
      // turn is an interrupted, reply-less prompt) — don't invent a spinner.
      setFinalize(false);
      return;
    }
    if (!finalizingRef.current) {
      setFinalize(true);
      tickRef.current?.(); // re-poll now instead of waiting out the slow interval
    }
    // Safety valve: an interrupted turn can end with no reply at all, so never hold
    // forever — drop the indicator after a grace even if nothing lands.
    const id = setTimeout(() => {
      wasWorkingRef.current = false;
      setFinalize(false);
    }, FINALIZE_GRACE_MS);
    return () => clearTimeout(id);
    // setFinalize / refs are stable enough; re-run only on the signals that change the hold.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [status, bgBusy, replyPending]);
}
