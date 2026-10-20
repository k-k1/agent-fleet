// The background memory review's queue as the Console reads it (ADR 0117): the status that carries
// the count for the nav and the notification, and the calls the approval screen makes. Counts and
// states only come from the status; the proposals themselves come from a member-only route.

import { useCallback, useEffect, useRef, useState } from "react";
import { api, apiJSON, isTransientErr } from "../../../core/api/client.ts";
import type { ReviewStatus } from "./memoryTypes.ts";

export const REVIEW_STATUS_PATH = "api/agents/memory-review";
// The queue grows while Settings is open (a review finishes in the background), so the count is
// read again now and then; visible tabs only.
const POLL_MS = 30_000;

/** A status shape the Agent really sent, or null: an older Agent answers 404 and a stopped
 *  workspace answers an error, neither of which is "no proposals". */
export function readReviewStatus(res: unknown): ReviewStatus | null {
  const r = res as Partial<ReviewStatus> & { error?: unknown };
  if (!r || r.error || typeof r.pending !== "number") return null;
  return { mode: String(r.mode ?? "off"), enabled: !!r.enabled, pending: r.pending, releasePending: r.releasePending };
}

/** Reads the review's status while `active`, again when `reload` changes, on `refresh()`, and
 *  every 30 s. null until the first answer, and whenever the Agent gave none. `poll` counts the
 *  answers received: a screen showing the queue re-reads it when it moves, so a count that grew
 *  (or a proposal swapped for another at the same count) is not left beside a stale list. */
export function useReviewStatus(
  active: boolean,
  reload: number,
): { status: ReviewStatus | null; poll: number; refresh: () => void } {
  const [status, setStatus] = useState<ReviewStatus | null>(null);
  const [poll, setPoll] = useState(0);
  const [tick, setTick] = useState(0);
  const refresh = useCallback(() => setTick((n) => n + 1), []);
  const live = useRef(true);
  useEffect(() => {
    live.current = true;
    return () => {
      live.current = false;
    };
  }, []);
  useEffect(() => {
    if (!active) {
      setStatus(null);
      return;
    }
    let cancelled = false;
    const read = () => {
      if (document.hidden) return;
      api(REVIEW_STATUS_PATH)
        .then((res) => {
          if (cancelled || isTransientErr(res)) return;
          setStatus(readReviewStatus(res));
          setPoll((n) => n + 1);
        })
        .catch(() => {});
    };
    read();
    const t = window.setInterval(read, POLL_MS);
    return () => {
      cancelled = true;
      window.clearInterval(t);
    };
  }, [active, reload, tick]);
  return { status, poll, refresh };
}

// The ids go in the query as well as the body: the CP audit ledger reads the URL only, never the body.
export const listProposals = () => apiJSON("api/agents/memory-review/proposals/list", "POST", {});
export const approveProposal = (id: string) =>
  apiJSON("api/agents/memory-review/proposals/approve?id=" + encodeURIComponent(id), "POST", { id });
export const rejectProposal = (id: string) =>
  apiJSON("api/agents/memory-review/proposals/reject?id=" + encodeURIComponent(id), "POST", { id });
export const rejectAllProposals = (run: string) =>
  apiJSON("api/agents/memory-review/proposals/reject-all" + (run ? "?run=" + encodeURIComponent(run) : ""), "POST", { run });
export const revertRun = (run: string) =>
  apiJSON("api/agents/memory-review/runs/revert?run=" + encodeURIComponent(run), "POST", { run });
